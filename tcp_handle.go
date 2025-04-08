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
		logrus.Debugf("%s Connected to %s : %s", upstreamHost, ip_type, conn.RemoteAddr().String())
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

	if forward_method == "proxy" {
		// 尝试连接到目标服务器
		upstreamConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			logrus.Errorln("Error connecting to target:", err)
			conn.Close() // 关闭客户端连接
			return
		}
		// defer upstreamConn.Close() // Defer removed, will be closed in forward_io_copy

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
	} else if forward_method == "direct" {

		targetConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			logrus.Errorln("Error connecting to target:", err)
			conn.Close() // 关闭客户端连接
			return
		}
		// defer targetConn.Close() // Defer removed, will be closed in forward_io_copy
		logConnectionType(upstreamHost, targetConn)
		_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if err != nil {
			logrus.Errorln("Error writing to client:", err)
			targetConn.Close() // 关闭目标连接
			conn.Close()       // 关闭客户端连接
			return
		}

		forward_io_copy(conn, targetConn, forward_method)
	} else if forward_method == "block" {
		//让客户端连接直接关闭
		conn.Close()
	}

}

type countingWriter struct {
	counter prometheus.Counter
}

func (cw *countingWriter) Write(p []byte) (int, error) {
	n := len(p)
	cw.counter.Add(float64(n))
	return n, nil
}

func copyWithCancel(ctx context.Context, targetConn io.Writer, teeReader io.Reader) (int64, error) {
	var totalCopied int64
	buf := make([]byte, 1024) // 使用缓冲区来逐步读取数据
	cr := New(ctx, teeReader)

	for {
		n, err := cr.Read(buf)
		if err == io.EOF {
			// 如果读取完毕，退出
			logrus.Debug("copyWithCancel 读取完毕，退出")
			return totalCopied, nil
		}
		if err != nil {
			// 如果读取错误，返回错误
			logrus.Debug("copyWithCancel 读取错误，返回错误")
			return totalCopied, err
		}

		// 将读取的数据写入目标连接
		nn, err := targetConn.Write(buf[:n])
		totalCopied += int64(nn)
		if err != nil {
			// 如果写入错误，返回错误
			return totalCopied, err
		}
	}
}

func forward_io_copy(conn, targetConn net.Conn, forward_method string) {
	defer func() {
		logrus.Debug("函数forward_io_copy结束")
	}()
	logrus.Debug("函数forward_io_copy开始")
	// 创建一个控制信号的通道
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	// 转发 conn -> targetConn
	go func() {

		defer func() {
			logrus.Debug("转发 conn -> targetConn 推出")
		}()
		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyUploadBytes // 这个proxy 上传好像记录的不对，但是不知道如何修复 todo
		} else {
			downloadCounter = directUploadBytes
		}
		teeReader := io.TeeReader(conn, &countingWriter{counter: downloadCounter})
		// 	获取返回的通道
		copied, err := copyWithCancel(ctx, targetConn, teeReader)
		if err != nil {
			logrus.Debugf("Error during copy: %v", err)
		} else {
			logrus.Debugf("Total bytes copied: %d", copied)
		}
		done <- struct{}{}

	}()

	// 转发 targetConn -> conn
	go func() {
		defer func() {
			logrus.Debug("转发 targetConn -> conn 退出")
		}()

		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyDownloadBytes
		} else {
			downloadCounter = DirectDownloadBytes
		}

		// 读取targetConn 的同时将数据写入countingWriter ，返回的reader 用于读取
		teeReader := io.TeeReader(targetConn, &countingWriter{counter: downloadCounter})
		// 	获取返回的通道
		copied, err := copyWithCancel(ctx, conn, teeReader)
		if err != nil {
			logrus.Debugf("Error during copy: %v", err)
		} else {
			logrus.Debugf("Total bytes copied: %d", copied)
		}
		done <- struct{}{}
	}()

	// 等待转发完成
	<-done
	logrus.Debug("drone 受到了")
	targetConn.Close()
	conn.Close()
	logrus.Debug("开始取消")
	cancel()
	logrus.Debug("取消发送")
}
