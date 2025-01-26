#!/usr/bin/bash
GOOS=windows GOARCH=amd64 go build
sudo cp config.yaml http_proxy.exe  /mnt/hy2/
 


sudo cp config.yaml /etc/http_proxy
sudo systemctl restart http_proxy
