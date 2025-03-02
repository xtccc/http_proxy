package main

import (
	"net/http"
	_ "net/http/pprof"

	"github.com/sirupsen/logrus"
)

func init_pprof() {
	logrus.Info("pprof开启")
	go func() {
		http.ListenAndServe(":6060", nil)
	}()

	// go tool pprof http://192.168.31.2:6060/debug/pprof/heap
}
