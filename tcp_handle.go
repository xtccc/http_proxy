package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
)

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
	forward(upstream, ForwardMethod, reqLine, conn, host)

}

func forward(upstreamHost, forward_method, reqLine string, conn net.Conn, host string) {

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
		forward_io_copy(conn, upstreamConn)
	} else if forward_method == "direct" {

		targetConn, err := net.Dial("tcp", upstreamHost)
		if err != nil {
			logrus.Errorln("Error connecting to target:", err)
			return
		}
		_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if err != nil {
			logrus.Errorln("Error writing to client:", err)
			return
		}

		//targetConn.SetWriteDeadline(time.Time{}) // 清除写入超时

		forward_io_copy(conn, targetConn)

	} else if forward_method == "block" {
		//让客户端连接直接关闭
		conn.Close()
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
