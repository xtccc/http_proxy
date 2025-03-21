package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
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
		logrus.Errorln("Invalid target format")
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
			return
		}
		defer upstreamConn.Close()

		// 将客户端的 CONNECT 请求转发给上游代理
		_, err = upstreamConn.Write([]byte(reqLine))
		if err != nil {
			logrus.Errorln("Error forwarding CONNECT to upstream:", err)
			return
		}

		// 读取上游代理的响应
		upstream_resp, err := readRequestHeader(upstreamConn)
		if err != nil {
			logrus.Errorln("readRequestHeader(upstreamConn) error ", err)
			return
		}

		// 转发上游代理的响应给客户端
		_, err = conn.Write([]byte(upstream_resp))
		if err != nil {
			logrus.Errorln("Error forwarding response to client:", err)
			return
		}
		forward_io_copy(conn, upstreamConn, forward_method)
	} else if forward_method == "direct" {

		targetConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			logrus.Errorln("Error connecting to target:", err)
			return
		}
		logConnectionType(upstreamHost, targetConn)
		_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if err != nil {
			logrus.Errorln("Error writing to client:", err)
			return
		}

		//targetConn.SetWriteDeadline(time.Time{}) // 清除写入超时

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

func forward_io_copy(conn, targetConn net.Conn, forward_method string) {
	// 使用 channel 和 WaitGroup 来管理双向转发
	errCh := make(chan error, 2)
	wg := &sync.WaitGroup{}
	wg.Add(2)

	// 转发 conn -> targetConn
	go func() {

		defer wg.Done()
		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyUploadBytes
		} else {
			downloadCounter = directUploadBytes
		}
		teeReader := io.TeeReader(conn, &countingWriter{counter: downloadCounter})
		client_return_n, err := io.Copy(targetConn, teeReader)
		if err != nil {
			errCh <- fmt.Errorf("error copying data to upstream: %w", err)
		}

		logrus.Debugf("Total bytes uploaded: %d", client_return_n)
	}()

	// 转发 targetConn -> conn
	go func() {
		defer wg.Done()
		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyDownloadBytes
		} else {
			downloadCounter = DirectDownloadBytes
		}
		// 读取targetConn 的同时将数据写入countingWriter ，返回的reader 用于读取
		teeReader := io.TeeReader(targetConn, &countingWriter{counter: downloadCounter})

		server_return_n, err := io.Copy(conn, teeReader)
		if err != nil {
			errCh <- fmt.Errorf("error copying data to client: %w", err)
		}
		logrus.Debugf("Total bytes downloaded: %d", server_return_n)

	}()

	// 等待转发完成
	wg.Wait()
	targetConn.Close()
	conn.Close()
	close(errCh)

}
