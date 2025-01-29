package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

// 检查域名是否符合后缀匹配规则
func getForwardMethodForHost(proxy_upstream, host, port string) (upstreamHost, method string) {
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
				logrus.Infof("host: %s method: %s upstream: %s", host, rule.ForwardMethod, upstreamHost)
				return upstreamHost, rule.ForwardMethod
			}
		} else if host == rule.DomainPattern {
			if rule.ForwardMethod == "direct" {
				upstreamHost = host + ":" + port
			} else {
				upstreamHost = proxy_upstream
			}
			// 精确匹配域名
			logrus.Infof("host: %s method: %s upstream: %s", host, rule.ForwardMethod, upstreamHost)
			return upstreamHost, rule.ForwardMethod
		}
	}

	if strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "10.") {
		// 如果 host 是以 192.168. 或 10. 开头的内网 IP，使用直连规则
		upstreamHost = host + ":" + port
		logrus.Infof("host: %s method: %s upstream: %s", host, "direct", upstreamHost)
		return upstreamHost, "direct"
	}

	// 默认使用代理
	logrus.Infof("host: %s method: %s upstream: %s", host, "proxy", proxy_upstream)
	return proxy_upstream, "proxy"
}

// 读取 HTTP 请求头直到遇到空行
func readRequestHeader(conn net.Conn) (string, error) {
	// 创建一个新的缓冲读取器
	reader := bufio.NewReader(conn)

	// 用于构建请求数据
	var requestBuilder strings.Builder

	for {
		// 逐行读取请求
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("error reading client request: %v", err)
		}

		// 写入到请求构建器中
		requestBuilder.WriteString(line)

		if err == io.EOF {
			break
		}

		// 检测是否到达空行（请求头结束）
		if line == "\r\n" {
			break
		}

	}

	// 返回读取到的完整请求头
	return requestBuilder.String(), nil
}

func createHTTPRequest(reqline string) (*http.Request, error) {
	// 使用 strings.Reader 将 reqline 包装成 io.Reader
	reader := strings.NewReader(reqline)

	// 使用 http.ReadRequest 解析请求
	req, err := http.ReadRequest(bufio.NewReader(reader))
	if err != nil {
		return nil, fmt.Errorf("error parsing HTTP request: %v", err)
	}

	return req, nil
}

func handleConnectRequest(conn net.Conn) {
	reqLine, err := readRequestHeader(conn)
	if err != nil {
		fmt.Println("readRequestHeader error", err)
		return
	}

	// 输出请求行
	//	fmt.Println("Received request:", reqLine)

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
		req, err := createHTTPRequest(reqLine)
		if err != nil {
			fmt.Println("createHTTPRequest error", err)
			return
		}
		handleConnectRequest_http(conn, req)
	}
	/* default:
		fmt.Println("Unsupported method:", method)
	} */

}

// 处理CONNECT请求（HTTPS代理）
func handleConnectRequest_https(conn net.Conn, target, reqLine string) {
	hostPort := strings.Split(target, ":")
	if len(hostPort) != 2 {
		fmt.Println("Invalid target format")
		return
	}

	host := hostPort[0]
	port := hostPort[1]
	proxy_upstream := *proxyAddr
	upstream, ForwardMethod := getForwardMethodForHost(proxy_upstream, host, port)

	// 调用 forward 函数进行请求转发
	forward(upstream, ForwardMethod, reqLine, conn)

}
func handleConnectRequest_http(conn net.Conn, req *http.Request) {
	proxy_upstream := *proxyAddr
	upstream, ForwardMethod := getForwardMethodForHost(proxy_upstream, req.Host, req.URL.Port())

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

func forward(upstreamHost, forward_method, reqLine string, conn net.Conn) {

	if forward_method == "proxy" {
		// 尝试连接到目标服务器
		upstreamConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			fmt.Println("Error connecting to target:", err)
			return
		}
		defer upstreamConn.Close()

		// 将客户端的 CONNECT 请求转发给上游代理
		_, err = upstreamConn.Write([]byte(reqLine))
		if err != nil {
			fmt.Println("Error forwarding CONNECT to upstream:", err)
			return
		}

		// 读取上游代理的响应
		upstream_resp, err := readRequestHeader(upstreamConn)
		if err != nil {
			fmt.Println("readRequestHeader(upstreamConn) error ", err)
			return
		}

		// 转发上游代理的响应给客户端
		_, err = conn.Write([]byte(upstream_resp))
		if err != nil {
			fmt.Println("Error forwarding response to client:", err)
			return
		}
		forward_io_copy(conn, upstreamConn)
	} else if forward_method == "direct" {

		targetConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			fmt.Println("Error connecting to target:", err)
			return
		}
		_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if err != nil {
			fmt.Println("Error writing to client:", err)
			return
		}

		//targetConn.SetWriteDeadline(time.Time{}) // 清除写入超时

		forward_io_copy(conn, targetConn)

	}

}

func forward_io_copy(conn, targetConn net.Conn) {
	// 使用 channel 和 WaitGroup 来管理双向转发
	errCh := make(chan error, 2)
	wg := &sync.WaitGroup{}
	wg.Add(2)

	// 转发 conn -> targetConn
	go func() {
		defer wg.Done()
		_, err := io.Copy(targetConn, conn)
		if err != nil {
			errCh <- fmt.Errorf("error copying data to upstream: %w", err)
		}
	}()

	// 转发 targetConn -> conn
	go func() {
		defer wg.Done()
		_, err := io.Copy(conn, targetConn)
		if err != nil {
			errCh <- fmt.Errorf("error copying data to client: %w", err)
		}
	}()

	// 等待转发完成
	wg.Wait()
	targetConn.Close()
	conn.Close()
	close(errCh)

}
