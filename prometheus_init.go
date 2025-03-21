package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// 代理下载流量 (字节)
	ProxyDownloadBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "http_proxy_download_bytes_total",
		Help: "Total bytes downloaded via proxy.",
	})

	// 代理上传流量 (字节)
	ProxyUploadBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "http_proxy_upload_bytes_total",
		Help: "Total bytes uploaded via proxy.",
	})

	// 直连下载流量 (字节)
	DirectDownloadBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "http_direct_download_bytes_total",
		Help: "Total bytes downloaded directly.",
	})

	// 直连上传流量 (字节)
	directUploadBytes = promauto.NewCounter(prometheus.CounterOpts{
		Name: "myapp_direct_upload_bytes_total",
		Help: "Total bytes uploaded directly.",
	})
)

// main 	http.Handle("/metrics", promhttp.Handler())
func prometheus_init(listenAddr_prometheus string) error {
	// 启动prometheus服务
	http.Handle("/metrics", promhttp.Handler())
	err := http.ListenAndServe(listenAddr_prometheus, nil)
	if err != nil {
		return err
	}
	return nil
}
