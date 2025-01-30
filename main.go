package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

// 检查域名是否符合后缀匹配规则
func getForwardMethodForHost(proxy_upstream, host, port, protocol string) (upstreamHost, method string) {
	// 遍历映射规则
	for _, rule := range domainForwardMap.Rules {

		// 如果是通配符匹配（例如 *.douyu.cn）
		if strings.HasPrefix(rule.DomainPattern, "*.") {
			// 检查域名后缀是否匹配
			if strings.HasSuffix(host, rule.DomainPattern[1:]) {
				if rule.ForwardMethod == "direct" {
					upstreamHost = host + ":" + port
				} else {
					upstreamHost = proxy_upstream
				}
				logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)
				return upstreamHost, rule.ForwardMethod
			}
		} else if host == rule.DomainPattern {
			if rule.ForwardMethod == "direct" {
				upstreamHost = host + ":" + port
			} else {
				upstreamHost = proxy_upstream
			}
			// 精确匹配域名
			logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, rule.ForwardMethod, upstreamHost)
			return upstreamHost, rule.ForwardMethod
		}
	}

	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") {
		// 如果 host 是以 192.168. 或 10. 开头的内网 IP，使用直连规则
		upstreamHost = host + ":" + port
		logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, "direct", upstreamHost)
		return upstreamHost, "direct"
	}

	// 默认使用代理
	logrus.Infof("protocol: %s host: %s method: %s upstream: %s", protocol, host, "proxy", proxy_upstream)
	return proxy_upstream, "proxy"
}

func createHTTPRequest(reqline string, body []byte) (*http.Request, error) {
	reader := strings.NewReader(reqline)
	req, err := http.ReadRequest(bufio.NewReader(reader))
	if err != nil {
		return nil, fmt.Errorf("error parsing HTTP request: %v", err)
	}

	if len(body) > 0 {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}

	return req, nil
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
		fmt.Println("Invalid CONNECT request format")
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

func handleConnectRequest_http(conn net.Conn, req *http.Request) {
	proxy_upstream := *proxyAddr
	upstream, ForwardMethod := getForwardMethodForHost(proxy_upstream, req.Host, req.URL.Port(), "http")

	if ForwardMethod == "proxy" {
		handleConnection_http_proxy(conn, req, upstream)
	} else if ForwardMethod == "direct" {
		handleConnection_http(conn, req)

	} else {
		logrus.Error("ForwardMethod is wrong", ForwardMethod)
		return
	}

}

var domainForwardMap Config
var proxyAddr *string

func main() {
	// 解析命令行参数
	listenAddr := flag.String("listen", ":8080", "监听地址，格式为[host]:port")
	proxyAddr = flag.String("proxy", "127.0.0.1:8079", "监听地址，格式为[host]:port")
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
		fmt.Println("Error starting server:", err)
		return
	}

	defer listener.Close()

	fmt.Printf("Proxy server is running on %s\n", *listenAddr)

	// 接受连接
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Println("Error accepting connection:", err)
			continue
		}

		// 处理 CONNECT 请求
		go handleConnectRequest(conn)
	}
}
