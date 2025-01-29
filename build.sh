#!/usr/bin/bash
go build . 
rm http_proxy.log
./http_proxy


 GOOS=windows GOARCH=amd64 go build