package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
)

/* // 域名和转发方式的映射
var domainForwardMap = []struct {
	DomainPattern string
	ForwardMethod string
}{
	{"*.cn", "direct"},
	{"douyu.com", "direct"},
	{"*.douyucdn.cn", "direct"},
	{"*.bilibili.com", "direct"},
	{"*.miui.com", "direct"},
	{"*.zhihu.com", "direct"},
	{"*.zhimg.com", "direct"},
	{"*.hdslb.com", "direct"},
	{"*.biliapi.net", "direct"},
	{"*.alicdn.com", "direct"},
	{"*.alipay.com", "direct"},
	{"*.jd.com", "direct"},
	{"*.360buyimg.com", "direct"},
	{"*.feishu.cn", "direct"},
	{"*.feishucdn.com", "direct"},
	{"*.mi.com", "direct"},
	{"111.13.24.98", "direct"},
	{"*.qq.com", "direct"},
	{"google.com", "proxy"},
}
*/
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
		if err != nil {
			return "", fmt.Errorf("error reading client request: %v", err)
		}

		// 写入到请求构建器中
		requestBuilder.WriteString(line)

		// 检测是否到达空行（请求头结束）
		if line == "\r\n" {
			break
		}
	}

	// 返回读取到的完整请求头
	return requestBuilder.String(), nil
}

func handleConnectRequest(conn net.Conn) {
	defer conn.Close()

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
	target := parts[1] // 目标域名和端口
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

		// 双向转发数据（开始隧道）
		go func() {
			_, err := io.Copy(upstreamConn, conn)
			if err != nil {
				fmt.Println("Error copying data to upstream:", err)
			}
		}()
		_, err = io.Copy(conn, upstreamConn)
		if err != nil {
			fmt.Println("Error copying data to client:", err)
		}
	} else if forward_method == "direct" {
		targetConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			fmt.Println("Error connecting to target:", err)
			return
		}
		defer targetConn.Close()

		// 发送 200 OK 响应到客户端，告知隧道建立成功
		conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

		// 双向转发数据
		go io.Copy(targetConn, conn)
		io.Copy(conn, targetConn)

	}

}
