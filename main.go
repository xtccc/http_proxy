package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

// 添加 Prometheus 指标
var (
	forwardedRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_forwarded_requests_total",
			Help: "转发请求的总数，按协议、主机和转发方式统计",
		},
		[]string{"protocol", "host", "method"},
	)

	forwardedBytes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_forwarded_bytes_total",
			Help: "转发流量的总字节数，按协议、主机和转发方式统计",
		},
		[]string{"protocol", "host", "method"},
	)
)

func init() {
	// 注册 Prometheus 指标
	prometheus.MustRegister(forwardedRequests)
	prometheus.MustRegister(forwardedBytes)
}

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

		// 如果是通配符匹配（例如 *.douyu.cn）
		if strings.HasPrefix(rule.DomainPattern, "*.") {
			domainSuffix := rule.DomainPattern[2:]
			// 检查域名后缀是否匹配,和*.domain相同的也能匹配上
			if strings.HasSuffix(host, domainSuffix) {
				if rule.ForwardMethod == "direct" {
					upstreamHost = direct_upstream
				} else {
					upstreamHost = proxy_upstream
				}
				logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)
				// 记录请求计数
				forwardedRequests.WithLabelValues(protocol, host, rule.ForwardMethod).Inc()
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
			// 记录请求计数
			forwardedRequests.WithLabelValues(protocol, host, rule.ForwardMethod).Inc()
			return upstreamHost, rule.ForwardMethod
		}
	}

	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") || (strings.HasPrefix(host, "172.") && isInRange(host, "172.16.0.0", "172.31.255.255")) {
		// 172.16.0.0 - 172.31.255.255 直连
		// 如果 host 是以 192.168. 或 10. 开头的内网 IP，使用直连规则
		upstreamHost = direct_upstream
		logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, "direct", upstreamHost)
		// 记录请求计数
		forwardedRequests.WithLabelValues(protocol, host, "direct").Inc()
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
			// 记录请求计数
			forwardedRequests.WithLabelValues(protocol, host, method).Inc()
			return
		} else {
			upstreamHost = direct_upstream
			method = "direct"
			logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, method, upstreamHost)
			// 记录请求计数
			forwardedRequests.WithLabelValues(protocol, host, method).Inc()
			return
		}
	}

	// 默认使用代理
	logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, "proxy", proxy_upstream)
	// 记录请求计数
	forwardedRequests.WithLabelValues(protocol, host, "proxy").Inc()
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
		logrus.Errorln("Invalid CONNECT request format")
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

func main() {
	// 解析命令行参数
	listenAddr := flag.String("listen", ":8080", "监听地址，格式为[host]:port")
	proxyAddr = flag.String("proxy", "127.0.0.1:8079", "监听地址，格式为[host]:port")
	// 添加 Prometheus 指标采集的 HTTP 端口
	metricsAddr := flag.String("metrics", ":9090", "Prometheus metrics 监听地址")
	flag.Parse()

	// 创建一个日志文件
	file, err := os.OpenFile("http_proxy.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		logrus.Fatal(err)
	}
	// 设置输出到文件
	logrus.SetOutput(file)

	domainForwardMap = LoadConfig()

	// 启动代理服务，监听指定地址
	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		logrus.Errorln("Error starting server:", err)
		fmt.Println("Error starting server:", err)
		return
	}

	defer listener.Close()

	hello := fmt.Sprintf("Proxy server is running on %s\n", *listenAddr)
	logrus.Errorln(hello)
	fmt.Println(hello)

	// 启动 Prometheus 指标服务
	go func() {
		http.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(*metricsAddr, nil); err != nil {
			logrus.Errorln("Error starting metrics server:", err)
			fmt.Println("Error starting metrics server:", err)
		}
	}()

	// 接受连接
	for {
		conn, err := listener.Accept()
		if err != nil {
			logrus.Errorln("Error accepting connection:", err)
			fmt.Println("Error accepting connection:", err)
			continue
		}

		// 处理 请求
		go handleConnectRequest(conn)
	}
}
