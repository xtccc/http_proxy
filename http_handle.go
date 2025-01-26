package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
)

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

func handleConnectRequest_http(conn net.Conn, req *http.Request) {
	proxy_upstream := *proxyAddr
	var host string
	if strings.Contains(req.Host, ":") {
		host = strings.Split(req.Host, ":")[0]
	} else {
		host = req.Host
	}

	upstream, ForwardMethod := getForwardMethodForHost(proxy_upstream, host, req.URL.Port(), "http")

	if ForwardMethod == "proxy" {
		handleConnection_http_proxy(conn, req, upstream)
	} else if ForwardMethod == "direct" {
		handleConnection_http(conn, req)

	} else if ForwardMethod == "block" {
		conn.Close()
		//让客户端连接直接关闭
		return
	}
}

// 修改 handleConnection_http 函数
func handleConnection_http(clientConn net.Conn, req *http.Request) {
	defer clientConn.Close()
	var addr string
	if !strings.Contains(req.URL.Host, ":") {
		addr = req.URL.Host + ":" + "80"
	} else {
		addr = req.URL.Host
	}
	if req.URL.Host != "" {
		if strings.Contains(req.RequestURI, req.URL.Host) {
			newRUI := strings.Split(req.RequestURI, req.URL.Host)[1]
			req.RequestURI = newRUI
		}
	}
	targetConn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Printf("Failed to connect to target: %v", err)
		return
	}
	defer targetConn.Close()

	// 将客户端的请求转发到目标服务器
	reqBytes, err := httputil.DumpRequest(req, true)
	if err != nil {
		log.Printf("Failed to dump request: %v", err)
		return
	}
	_, err = targetConn.Write(reqBytes)
	if err != nil {
		log.Printf("Failed to forward request: %v", err)
		return
	}

	// 读取目标服务器的响应
	resp, err := http.ReadResponse(bufio.NewReader(targetConn), req)
	if err != nil {
		log.Printf("Failed to read response: %v", err)
		return
	}
	defer resp.Body.Close()

	// 将响应写入到缓冲区以计算大小
	respBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		log.Printf("Failed to dump response: %v", err)
		return
	}
	_, err = clientConn.Write(respBytes)
	if err != nil {
		log.Printf("Failed to send response to client: %v", err)
		return
	}

}

// 修改 handleConnection_http_proxy 函数
func handleConnection_http_proxy(clientConn net.Conn, req *http.Request, upstream string) {
	defer clientConn.Close()

	upstreamConn, err := net.Dial("tcp", upstream)
	if err != nil {
		log.Printf("Failed to connect to upstream proxy: %v", err)
		return
	}
	defer upstreamConn.Close()

	req.URL.Scheme = "http"
	req.URL.Host = req.Host

	// 将请求转发到上游代理
	reqBytes, err := httputil.DumpRequestOut(req, true)
	if err != nil {
		log.Printf("Failed to dump request: %v", err)
		return
	}
	_, err = upstreamConn.Write(reqBytes)
	if err != nil {
		log.Printf("Failed to forward request to upstream: %v", err)
		return
	}

	// 读取上游代理的响应
	resp, err := http.ReadResponse(bufio.NewReader(upstreamConn), req)
	if err != nil {
		log.Printf("Failed to read response from upstream: %v", err)
		return
	}
	defer resp.Body.Close()

	// 将响应写入到缓冲区以计算大小
	respBytes, err := httputil.DumpResponse(resp, true)
	if err != nil {
		log.Printf("Failed to dump response: %v", err)
		return
	}
	_, err = clientConn.Write(respBytes)
	if err != nil {
		log.Printf("Failed to send response to client: %v", err)
		return
	}

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
