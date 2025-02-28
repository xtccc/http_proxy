package main

import (
	"net/url"
	"strings"
)

// isHTTPS 判断请求是否为 HTTPS 协议
func isHTTPS(reqLine string) bool {
	reqline := strings.Split(reqLine, " ")[1]
	// 使用 url.Parse 来解析请求行中的目标地址
	parsedURL, err := url.Parse(reqline)
	if err != nil {
		// 如果解析失败，返回 false
		return false
	}

	// 判断是否是 https 协议
	return parsedURL.Scheme == "https"
}
