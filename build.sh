#!/usr/bin/bash
go build . 
rm http_proxy.log
./http_proxy
