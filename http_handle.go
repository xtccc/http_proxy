package main

import (
	"bufio"
	"log"
	"net"
	"net/http"
	"strings"
)

func handleConnection_http(clientConn net.Conn, req *http.Request) {
	defer clientConn.Close()
	var addr string
	if !strings.Contains(req.URL.Host, ":") {
		addr = req.URL.Host + ":" + "80"
	} else {
		addr = req.URL.Host
	}
	targetConn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Printf("Failed to connect to target: %v", err)
		return
	}
	defer targetConn.Close()

	// 将客户端的请求转发到目标服务器
	if err := req.Write(targetConn); err != nil {
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

	// 将目标服务器的响应返回给客户端
	if err := resp.Write(clientConn); err != nil {
		log.Printf("Failed to send response to client: %v", err)
		return
	}
}

func handleConnection_http_proxy(clientConn net.Conn, req *http.Request, upstream string) {
	defer clientConn.Close()

	// Connect to the upstream HTTP proxy
	upstreamConn, err := net.Dial("tcp", upstream)
	if err != nil {
		log.Printf("Failed to connect to upstream proxy: %v", err)
		return
	}
	defer upstreamConn.Close()

	// Modify the request to be suitable for the upstream proxy
	// Include the full URL (scheme + host + path)
	req.URL.Scheme = "http"
	req.URL.Host = req.Host
	/* req.Header.Set("Host", req.Host)
	req.RequestURI = ""
	req.Header.Del("Proxy-Connection") */

	// Forward the client's request to the upstream proxy
	if err := req.WriteProxy(upstreamConn); err != nil {
		log.Printf("Failed to forward request to upstream: %v", err)
		return
	}

	// Read the response from the upstream proxy
	resp, err := http.ReadResponse(bufio.NewReader(upstreamConn), req)
	if err != nil {
		log.Printf("Failed to read response from upstream: %v", err)
		return
	}
	defer resp.Body.Close()

	// Forward the response back to the client
	if err := resp.Write(clientConn); err != nil {
		log.Printf("Failed to send response to client: %v", err)
		return
	}
}
