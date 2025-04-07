package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

// 判断一个IP地址是否在指定的范围内
func isInRange(ipStr, startStr, endStr string) bool {
	ip := net.ParseIP(ipStr)
	start := net.ParseIP(startStr)
	end := net.ParseIP(endStr)

	// 确保有效的IP地址
	if ip == nil || start == nil || end == nil {
		return false
	}

	// 比较IP地址大小
	return bytes.Compare(ip, start) >= 0 && bytes.Compare(ip, end) <= 0
}

// 检查域名是否符合后缀匹配规则
func getForwardMethodForHost(proxy_upstream, host, port, protocol string) (upstreamHost, method string) {
	direct_upstream := host + ":" + port
	// 遍历映射规则
	for _, rule := range domainForwardMap.Rules {
		//全局直连 用于纯粹的转发http流量
		if rule.DomainPattern == "*" && rule.ForwardMethod == "direct" {
			upstreamHost = direct_upstream
			logrus.Infof("全局直连规则: protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)
			method = rule.ForwardMethod
			return
		}

		// 如果是通配符匹配（例如 *.douyu.cn）
		if strings.HasPrefix(rule.DomainPattern, "*.") {

			domainSuffix := rule.DomainPattern[2:]
			// 检查域名后缀是否匹配,和*.domain相同的也能匹配上
			if strings.HasSuffix(host, domainSuffix) {

				if rule.ForwardMethod == "direct" {
					upstreamHost = direct_upstream
				} else if rule.ForwardMethod == "block" {
					upstreamHost = ""
					method = rule.ForwardMethod
					logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)
					return
				} else {
					upstreamHost = proxy_upstream
				}
				logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)
				return upstreamHost, rule.ForwardMethod
			}
		} else if host == rule.DomainPattern {
			if rule.ForwardMethod == "direct" {
				upstreamHost = direct_upstream
			} else {
				upstreamHost = proxy_upstream
			}
			// 精确匹配域名
			logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)

			return upstreamHost, rule.ForwardMethod
		}
	}

	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || (strings.HasPrefix(host, "172.") && isInRange(host, "172.16.0.0", "172.31.255.255")) {
		// 172.16.0.0 - 172.31.255.255 直连
		// 如果 host 是以 192.168. 或 10. 开头的内网 IP，使用直连规则
		upstreamHost = direct_upstream
		logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, "direct", upstreamHost)
		return upstreamHost, "direct"
	}

	// 所有的ip地址当作域名的域名，匹配为direct
	// 除了1.1.1.1和8.8.8.8是proxy模式
	// 新增IP地址判断逻辑
	if ip := net.ParseIP(host); ip != nil {
		if host == "1.1.1.1" || host == "8.8.8.8" {
			upstreamHost = proxy_upstream
			method = "proxy"
			logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, method, upstreamHost)
			return
		} else {
			upstreamHost = direct_upstream
			method = "direct"
			logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, method, upstreamHost)
			return
		}
	}

	// 默认使用代理
	logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, "proxy", proxy_upstream)
	return proxy_upstream, "proxy"
}

func handleConnectRequest(conn net.Conn) {
	reqLine, body, err := readRequestHeaderAndBody(conn)
	if err != nil {
		log.Printf("Failed to read request: %v", err)
		return
	}

	// 解析出目标主机和端口
	// 格式为 CONNECT www.google.com:443 HTTP/1.1
	parts := strings.Split(reqLine, " ")
	if len(parts) < 3 {
		logrus.Errorf("Invalid CONNECT request format,reqLine: %s", reqLine)
		return
	}

	method := parts[0]
	target := parts[1]
	// 根据请求方法处理
	switch method {
	case "CONNECT":
		handleConnectRequest_https(conn, target, reqLine)
		//除了CONNECT其余的都是http的协议，转给http的上游

	default:
		//为了给 fastgpt 的https get 做适配
		// 如果是 HTTPS 请求（GET等），则模拟 CONNECT 请求进行隧道处理
		if isHTTPS(reqLine) {
			// 使用 CONNECT 方法来处理隧道建立
			method = "CONNECT" // 强制转换为 CONNECT

			// 然后调用 https 请求的处理函数
			handleConnectRequest_https(conn, target, reqLine)
			return
		}
		req, err := createHTTPRequest(reqLine, body)
		if err != nil {
			log.Printf("Failed to create HTTP request: %v", err)
			return
		}
		handleConnectRequest_http(conn, req)
	}
}

var domainForwardMap Config
var proxyAddr *string

func loglevel_set(loglevel *string) {
	if *loglevel == "info" || *loglevel == "Info" {
		logrus.SetLevel(logrus.InfoLevel)
	}

	if *loglevel == "debug" || *loglevel == "Debug" {
		logrus.SetLevel(logrus.DebugLevel)
	}
	logrus.Info("日志等级为:", logrus.GetLevel())
}
func main() {
	// 解析命令行参数
	listenAddr := flag.String("listen", ":8080", "监听地址，格式为[host]:port")
	proxyAddr = flag.String("proxy", "127.0.0.1:8079", "监听地址，格式为[host]:port")
	loglevel := flag.String("log", "Info", "日志等级 Info Debug")
	enable_pprof := flag.Bool("enable_pprof", false, "是否启用pprof")
	version := flag.Bool("version", false, "是否显示版本")
	listenAddr_prometheus := flag.String("listen_prometheus", ":9988", "prometheus 指标 监听地址，格式为:port")

	flag.Parse()
	if *version {
		fmt.Println("version:", "v1.0.3")
		os.Exit(0)
	}
	if *enable_pprof {
		init_pprof()
	}
	loglevel_set(loglevel)

	// 设置输出到标准输出
	logrus.SetOutput(os.Stdout)

	go func() {
		// 初始化prometheus
		err := prometheus_init(*listenAddr_prometheus)
		if err != nil {
			logrus.Errorln("Error starting server:", err)
			os.Exit(1)
		}
	}()

	domainForwardMap = LoadConfig()

	// 启动代理服务，监听指定地址
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		logrus.Errorln("Error starting server:", err)
		fmt.Println("Error starting server:", err)
		return
	}

	defer listener.Close()

	hello := fmt.Sprintf("Proxy server is running on %s", *listenAddr)
	logrus.Info(hello)

	// 接受连接
	for {
		conn, err := listener.Accept()
		if err != nil {
			logrus.Errorln("Error accepting connection:", err)
			continue
		}

		// 处理 请求
		go handleConnectRequest(conn)
	}

}
