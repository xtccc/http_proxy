package main

import (
	"bufio"
	"crypto/tls"
	"net"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

func setup_proxy_bak() {
	// 写一个定时器
	// 每分钟检测一次上游是否正常，如果不正常，使用 proxyAddrbak 替换  proxyAddr
	// 替换之前先使用 check_upstream_hc 函数检测一下proxyAddrbak是否正常，不正常还是不替换了
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// 检测上游是否正常
		if !check_upstream_hc(*proxyAddr) {
			logrus.Warn("上游 ", *proxyAddr, " 异常，尝试切换到备份上游 ", *proxyAddrbak)
			// 检测 proxyAddrbak 是否正常
			if check_upstream_hc(*proxyAddrbak) {
				logrus.Info("备份上游 ", *proxyAddrbak, " 正常，切换")
				// 替换 proxyAddr
				temp := *proxyAddr
				*proxyAddr = *proxyAddrbak
				*proxyAddrbak = temp
				logrus.Info("已将 proxyAddr 切换为 ", *proxyAddr, "，proxyAddrbak 切换为 ", *proxyAddrbak)
			} else {
				logrus.Error("备份上游 ", *proxyAddrbak, " 也不正常，不进行切换")
			}
		} else {
			logrus.Debug("上游 ", *proxyAddr, " 正常")
		}
	}
}
func check_upstream_hc(http_upstream_addr string) bool {
	// 1. 连接到代理服务器
	upstreamConn, err := net.DialTimeout("tcp", http_upstream_addr, 10*time.Second) // 增加超时
	if err != nil {
		logrus.Errorln("Error connecting to upstream proxy:", err)
		return false
	}
	defer upstreamConn.Close()

	// 2. 发送 CONNECT 请求到代理
	var sb strings.Builder
	sb.WriteString("CONNECT www.google.com:443 HTTP/1.1\r\n")
	sb.WriteString("Host: www.google.com:443\r\n")
	sb.WriteString("User-Agent: curl/7.88.1\r\n") // 与您的curl一致
	sb.WriteString("Proxy-Connection: Keep-Alive\r\n")
	sb.WriteString("\r\n")
	connectReq := sb.String()

	logrus.Debugln("Sending CONNECT request:\n", connectReq)
	_, err = upstreamConn.Write([]byte(connectReq))
	if err != nil {
		logrus.Errorln("Error sending CONNECT to upstream proxy:", err)
		return false
	}

	// 3. 读取代理对 CONNECT 请求的响应
	// 简单的响应读取，仅用于示例。生产代码应使用更健壮的HTTP响应解析。
	proxyReader := bufio.NewReader(upstreamConn)
	statusLine, err := proxyReader.ReadString('\n')
	if err != nil {
		logrus.Errorln("Error reading status line from proxy:", err)
		return false
	}
	logrus.Debugln("Proxy CONNECT response status line:", strings.TrimSpace(statusLine))

	if !strings.Contains(statusLine, " 200 ") { // 检查是否包含 " 200 " 而不仅仅是 "200 OK" 因为有些代理可能返回 "200 Connection established"
		logrus.Errorln("Proxy CONNECT request failed:", strings.TrimSpace(statusLine))
		// 读取并打印更多代理的响应（如果有）
		for {
			headerLine, err := proxyReader.ReadString('\n')
			if err != nil || headerLine == "\r\n" {
				break
			}
			logrus.Debugln("Proxy response header:", strings.TrimSpace(headerLine))
		}
		return false
	}

	// 读取并丢弃 CONNECT 响应的剩余头部，直到空行
	for {
		line, err := proxyReader.ReadString('\n')
		if err != nil {
			logrus.Errorln("Error reading proxy CONNECT response headers:", err)
			return false
		}
		if line == "\r\n" { // HTTP头部结束
			break
		}
	}
	logrus.Debug("Proxy tunnel established.")

	// 4. 在已建立的隧道上进行TLS握手
	tlsConfig := &tls.Config{
		ServerName:         "www.google.com", // SNI (Server Name Indication)
		InsecureSkipVerify: false,            // 在生产环境中不要设置为true，除非您知道自己在做什么
		MinVersion:         tls.VersionTLS12, // 建议设置最低TLS版本
	}
	// upstreamConn 现在是通往 www.google.com:443 的TCP连接
	// 我们需要在此连接上启动TLS客户端握手
	tlsConn := tls.Client(upstreamConn, tlsConfig)

	// 设置握手超时
	// Go的net.Conn没有直接的SetHandshakeDeadline，但tls.Conn的Handshake会使用底层的超时
	// 或者，可以在一个goroutine中执行Handshake并通过channel和timer来控制超时
	err = upstreamConn.SetDeadline(time.Now().Add(10 * time.Second)) // 为握手设置超时
	if err != nil {
		logrus.Errorln("Error setting deadline for TLS handshake:", err)
		return false
	}

	err = tlsConn.Handshake()
	if err != nil {
		logrus.Errorln("TLS handshake with www.google.com failed:", err)
		return false
	}
	defer tlsConn.Close() // 确保TLS连接也被关闭

	// 清除之前的超时设置，或为后续读写设置新的超时
	err = upstreamConn.SetDeadline(time.Time{}) // 清除超时
	if err != nil {
		logrus.Errorln("Error clearing deadline:", err)
		return false
	}

	logrus.Debug("TLS handshake successful with www.google.com. Cipher Suite:", tls.CipherSuiteName(tlsConn.ConnectionState().CipherSuite))

	// 5. 通过TLS连接发送 HEAD 请求
	var sb2 strings.Builder
	sb2.WriteString("HEAD / HTTP/1.1\r\n")      // 注意是 HEAD
	sb2.WriteString("Host: www.google.com\r\n") // 注意是 Host:
	sb2.WriteString("User-Agent: curl/7.88.1\r\n")
	sb2.WriteString("Accept: */*\r\n")
	sb2.WriteString("Connection: close\r\n") // 请求后关闭连接
	sb2.WriteString("\r\n")
	headReq := sb2.String()

	logrus.Debugln("Sending HEAD request over TLS:\n", headReq)
	_, err = tlsConn.Write([]byte(headReq))
	if err != nil {
		logrus.Errorln("Error sending HEAD request to www.google.com:", err)
		return false
	}

	// 6. 读取来自 www.google.com 的响应 (通过TLS连接)
	logrus.Debug("HEAD request sent. Waiting for response from www.google.com...")
	// 使用 bufio.Reader 读取响应
	serverResponseReader := bufio.NewReader(tlsConn)

	// 读取状态行
	respStatusLine, err := serverResponseReader.ReadString('\n')
	if err != nil {
		logrus.Errorln("Error reading status line from www.google.com:", err)
		return false
	}
	logrus.Debug("www.google.com Status:", strings.TrimSpace(respStatusLine))

	// 读取并打印响应头
	logrus.Debug("www.google.com Headers:")
	for {
		headerLine, err := serverResponseReader.ReadString('\n')
		if err != nil {
			logrus.Errorln("Error reading headers from www.google.com:", err)
			// 对于HEAD请求，在读取完所有头部后，服务器可能会关闭连接，
			// 这可能导致ReadString返回EOF，这不一定是错误，除非没有读取到任何头部。
			// 如果respStatusLine成功读取，那么至少连接是成功的。
			if strings.TrimSpace(respStatusLine) != "" {
				break // 假定头部读取完毕，或者连接已按预期关闭
			}
			return false
		}
		logrus.Debug(headerLine)  // 打印原始头部行
		if headerLine == "\r\n" { // HTTP头部结束
			break
		}
	}

	return true
}
