package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus"
)

func logConnectionType(upstreamHost string, conn net.Conn) {
	if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		ip_type := "ipv4"
		if tcpAddr.IP.To4() == nil {
			ip_type = "ipv6"
		}
		logrus.Debugf("%s Connected to %s : localaddr:%s remoteaddr:%s", upstreamHost, ip_type, conn.LocalAddr().String(), conn.RemoteAddr().String())
	}
}

func readRequestHeaderAndBody(conn net.Conn) (string, []byte, error) {
	reader := bufio.NewReader(conn)
	var requestBuilder strings.Builder
	var contentLength int64 = 0

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", nil, fmt.Errorf("error reading client request: %v", err)
		}

		requestBuilder.WriteString(line)

		if strings.HasPrefix(line, "Content-Length:") {
			contentLength, _ = strconv.ParseInt(strings.TrimSpace(strings.Split(line, ":")[1]), 10, 64)
		}

		if err == io.EOF || line == "\r\n" {
			break
		}
	}

	var body []byte
	if contentLength > 0 {
		body = make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			return "", nil, fmt.Errorf("error reading request body: %v", err)
		}
	}

	return requestBuilder.String(), body, nil
}

// 处理CONNECT请求（HTTPS代理）
func handleConnectRequest_https(conn net.Conn, target, reqLine string) {
	hostPort := strings.Split(target, ":")
	if len(hostPort) != 2 {
		logrus.Errorf("Invalid target format ,target: %s", target)
		return
	}

	host := hostPort[0]
	port := hostPort[1]
	proxy_upstream := *proxyAddr
	upstream, ForwardMethod := getForwardMethodForHost(proxy_upstream, host, port, "https")

	// 调用 forward 函数进行请求转发
	forward(upstream, ForwardMethod, reqLine, conn)

}
func forward(upstreamHost, forward_method, reqLine string, conn net.Conn) {

	switch forward_method {
	case "proxy":
		upstreamConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			logrus.Errorln("Error connecting to target:", err)
			conn.Close() // 关闭客户端连接
			return
		}

		// 将客户端的 CONNECT 请求转发给上游代理
		_, err = upstreamConn.Write([]byte(reqLine))
		if err != nil {
			logrus.Errorln("Error forwarding CONNECT to upstream:", err)
			upstreamConn.Close() // 关闭上游连接
			conn.Close()         // 关闭客户端连接
			return
		}

		// 读取上游代理的响应
		upstream_resp, err := readRequestHeader(upstreamConn)
		if err != nil {
			logrus.Errorln("readRequestHeader(upstreamConn) error ", err)
			upstreamConn.Close() // 关闭上游连接
			conn.Close()         // 关闭客户端连接
			return
		}

		// 转发上游代理的响应给客户端
		_, err = conn.Write([]byte(upstream_resp))
		if err != nil {
			logrus.Errorln("Error forwarding response to client:", err)
			upstreamConn.Close() // 关闭上游连接
			conn.Close()         // 关闭客户端连接
			return
		}
		forward_io_copy(conn, upstreamConn, forward_method)
	case "direct":
		// 对于CONNECT隧道，每个请求都必须是一个新的TCP连接，
		// 因为隧道的生命周期与客户端的单个会话绑定。
		// 在这里使用连接池没有意义，因为连接在会话结束后无法被安全地复用。
		targetConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			logrus.Errorf("Error connecting to target %s: %v", upstreamHost, err)
			conn.Close() // 关闭客户端连接
			return
		}
		logConnectionType(upstreamHost, targetConn)

		// 告诉客户端隧道已建立
		_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if err != nil {
			logrus.Errorln("Error writing to client:", err)
			targetConn.Close() // 关闭目标连接
			conn.Close()       // 关闭客户端连接
			return
		}

		// 开始转发数据
		forward_io_copy(conn, targetConn, forward_method)
	case "block":
		//让客户端连接直接关闭
		conn.Close()
	}

}

func forward_io_copy(conn, targetConn net.Conn, forward_method string) {
	defer func() {
		// forward_io_copy 结束后，两个连接都将被关闭。
		logrus.Debug("函数forward_io_copy结束")
		err := conn.Close()
		if err != nil {
			logrus.Error(err.Error())
		}
		logrus.Debugf("关闭目标连接 %s -> %s", targetConn.LocalAddr(), targetConn.RemoteAddr())
		err = targetConn.Close()
		if err != nil {
			logrus.Error(err.Error())
		}
	}()
	logrus.Debug("函数forward_io_copy开始")

	//var wg sync.WaitGroup
	//wg.Add(2)
	ctx, cancel := context.WithCancel(context.Background())

	// 转发 conn -> targetConn
	go func() {

		defer func() {
			logrus.Debug("转发 conn -> targetConn 退出")
			cancel() // 结束后关闭另一边
		}()
		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyUploadBytes // 这个proxy 上传好像记录的不对，但是不知道如何修复 todo
		} else {
			downloadCounter = directUploadBytes
		}

		n, err := io.Copy(targetConn, conn)
		if err != nil {
			//	logrus.Errorf("conn -> targetConn 读取错误: %v", err)
			return
		}
		downloadCounter.Add(float64(n)) // 手动增加计数器
		cancel()                        // 结束后关闭另一边
	}()

	// 转发 targetConn -> conn
	go func() {
		defer func() {
			logrus.Debug("转发 targetConn -> conn 退出")
			cancel() // 结束后关闭另一边
		}()

		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyDownloadBytes
		} else {
			downloadCounter = DirectDownloadBytes
		}
		n, err := io.Copy(conn, targetConn)
		if err != nil {
			//logrus.Errorf("targetConn -> conn 读取错误: %v", err)
			return
		}
		downloadCounter.Add(float64(n)) // 手动增加计数器

	}()

	<-ctx.Done()
}
