package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

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

func forward_io_copy(conn, targetConn net.Conn, forward_method string) {
	defer func() {
		logrus.Debug("函数forward_io_copy结束")
		err := conn.Close()
		if err != nil {
			logrus.Error(err.Error())
		}
		err = targetConn.Close()
		if err != nil {
			logrus.Error(err.Error())
		}
	}()
	BufferSize := 1024 * 128
	logrus.Debug("函数forward_io_copy开始")
	// 设置超时时间
	timeout := 30 * time.Second // 30秒超时
	var wg sync.WaitGroup
	wg.Add(2)
	// 转发 conn -> targetConn
	go func() {

		defer func() {
			logrus.Debug("转发 conn -> targetConn 退出")
			wg.Done()
		}()
		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyUploadBytes // 这个proxy 上传好像记录的不对，但是不知道如何修复 todo
		} else {
			downloadCounter = directUploadBytes
		}

		// 	获取返回的通道
		buffer := make([]byte, BufferSize) // 示例缓冲区大小

		for {
			// 每次 Read() 操作之前都设置 ReadDeadline (从 conn 读取)
			conn.SetReadDeadline(time.Now().Add(timeout)) // 不要对 conn 设置 read deadline, 否则 client 慢了会超时

			n, err := conn.Read(buffer)
			if err != nil {
				if err == io.EOF {
					logrus.Debug("conn -> targetConn 连接关闭 (EOF)")
					return // 连接关闭，退出循环
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					logrus.Debug("conn -> targetConn 读取超时:", err) //Client 发送超时
					return                                        // 超时，退出循环
				}
				logrus.Errorf("conn -> targetConn 读取错误: %v", err)
				return // 其他读取错误，退出循环
			}

			// 将读取到的数据写入 targetConn
			_, err = targetConn.Write(buffer[:n])
			if err != nil {
				logrus.Errorf("conn -> targetConn 写入错误: %v", err)
				return // 写入错误，退出循环
			}
			downloadCounter.Add(float64(n)) // 手动增加计数器
		}
	}()

	// 转发 targetConn -> conn
	go func() {
		defer func() {
			logrus.Debug("转发 targetConn -> conn 退出")
			wg.Done()
		}()

		var downloadCounter prometheus.Counter
		if forward_method == "proxy" {
			downloadCounter = ProxyDownloadBytes
		} else {
			downloadCounter = DirectDownloadBytes
		}
		buffer := make([]byte, BufferSize) // 示例缓冲区大小

		for {
			// 每次 Read() 操作之前都设置 ReadDeadline (从 targetConn 读取)
			targetConn.SetReadDeadline(time.Now().Add(timeout))

			n, err := targetConn.Read(buffer)
			if err != nil {
				if err == io.EOF {
					logrus.Debug("targetConn -> conn 连接关闭 (EOF)")
					return // 连接关闭，退出循环
				}
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					logrus.Debug("targetConn -> conn 读取超时:", err) // 上游发送超时
					return                                        // 超时，退出循环
				}
				logrus.Errorf("targetConn -> conn 读取错误: %v", err)
				return // 其他读取错误，退出循环
			}

			// 将读取到的数据写入 conn
			_, err = conn.Write(buffer[:n])
			if err != nil {
				logrus.Errorf("targetConn -> conn 写入错误: %v", err)
				return // 写入错误，退出循环
			}
			downloadCounter.Add(float64(n)) // 手动增加计数器
		}
	}()

	wg.Wait()
}

func proxy_health_check(upstreamConn net.Conn) bool {
	defer upstreamConn.Close()
	proxy_website := "www.google.com:443"
	reqLine := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: curl/7.88.1\r\nProxy-Connection: Keep-Alive\r\n\r\n", proxy_website, proxy_website)

	// 将客户端的 CONNECT 请求转发给上游代理
	_, err := upstreamConn.Write([]byte(reqLine))
	if err != nil {
		upstreamConn.Close() // 关闭上游连接
		return false
	}

	// 读取上游代理的响应
	upstream_resp, err := readRequestHeader(upstreamConn)
	if err != nil {
		upstreamConn.Close() // 关闭上游连接
		return false
	}
	resps := strings.Split(upstream_resp, " ")
	if len(resps) > 1 && resps[1] == "200" {
		return true

	}
	return false
}
