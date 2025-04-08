package main

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
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

func forward_io_copy(conn, targetConn net.Conn, forward_method string) {
	wg := &sync.WaitGroup{}
	wg.Add(2)

	// 使用 sync.Once 确保连接只被关闭一次
	var once sync.Once
	closeConnections := func() {
		slog.Info("Closing connections...")
		err := targetConn.Close()
		// 忽略 "use of closed network connection" 错误，因为它表示连接已经被另一方关闭或自己关闭了
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			slog.Error(fmt.Sprintf("targetConn.Close error: %s", err.Error()))
		}
		err = conn.Close()
		if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			slog.Error(fmt.Sprintf("conn.Close error: %s", err.Error()))
		}
	}

	// 转发 conn -> targetConn
	go func() {
		defer wg.Done()
		defer once.Do(closeConnections) // 当这个 goroutine 结束时，尝试关闭连接

		var uploadCounter prometheus.Counter
		if forward_method == "proxy" {
			uploadCounter = ProxyUploadBytes
		} else {
			uploadCounter = directUploadBytes
		}
		teeReader := io.TeeReader(conn, &countingWriter{counter: uploadCounter})
		logrus.Debugf("before io.Copy (targetConn, teeReader)")
		client_return_n, err := io.Copy(targetConn, teeReader)
		logrus.Debugf("after io.Copy (targetConn, teeReader) err: %v", err) // 使用 %v 打印错误

		// 如果是 TCP 连接，尝试关闭写方向，通知对端我们不会再发送数据
		if tcpConn, ok := targetConn.(interface{ CloseWrite() error }); ok {
			tcpConn.CloseWrite()
		}

		if err != nil && err != io.EOF { // io.EOF 是正常关闭的信号，不应视为错误
			// 忽略 "use of closed network connection" 因为可能是对方或自己关闭了
			if !strings.Contains(err.Error(), "use of closed network connection") {
				logrus.Errorf("error copying data to upstream: %v", err)
			}
		}
		logrus.Debugf("Total bytes uploaded: %d", client_return_n)
	}()

	// 转发 targetConn -> conn
	go func() {
		defer wg.Done()
		defer once.Do(closeConnections) // 当这个 goroutine 结束时，尝试关闭连接

		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyDownloadBytes
		} else {
			downloadCounter = DirectDownloadBytes
		}
		teeReader := io.TeeReader(targetConn, &countingWriter{counter: downloadCounter})
		logrus.Debugf("before io.Copy (conn, teeReader)")
		server_return_n, err := io.Copy(conn, teeReader)
		logrus.Debugf("after io.Copy (conn, teeReader) err: %v", err) // 使用 %v 打印错误

		// 如果是 TCP 连接，尝试关闭写方向
		if tcpConn, ok := conn.(interface{ CloseWrite() error }); ok {
			tcpConn.CloseWrite()
		}

		if err != nil && err != io.EOF {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				logrus.Errorf("error copying data to client: %v", err)
			}
		}
		logrus.Debugf("Total bytes downloaded: %d", server_return_n)
	}()

	// 等待两个 goroutine 都完成（即使它们可能因为错误或关闭而提前退出）
	wg.Wait()
	slog.Info("Both io.Copy goroutines finished.")
	// 确保最终关闭（虽然 once.Do 应该已经处理了，但作为保险）
	// once.Do(closeConnections) // 这一行可以移除，因为 defer 中已经有了
}
