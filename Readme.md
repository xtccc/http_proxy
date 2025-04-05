# HTTP Proxy Server

[中文说明](./readme_zh.md)

A lightweight HTTP proxy server written in Go, supporting domain-based forwarding rules.

## Features

- 🚀 High performance proxy server
- 🔍 Domain pattern matching (exact and wildcard)
- ⚙️ YAML configuration file
- 🔒 HTTPS tunneling support
- 📝 Logging with logrus

## Configuration

Edit `config.yaml` to define forwarding rules:

```yaml
rules:
  - domainPattern: "*.cn"  # Wildcard match
    forwardMethod: "direct" # direct or proxy
  - domainPattern: "google.com" # Exact match 
    forwardMethod: "proxy"
  - domainPattern: "*.baidu.com"
    forwardMethod: "block" # block site
```

## Installation

1. Install Go (1.20+)
2. Clone this repository
3. Build the project:

```bash
go build -o proxy
```


```bash
go install github.com/xtccc/http_proxy@latest
```

## Usage

Start the proxy server:

```bash
./proxy -listen :8080 -proxy 127.0.0.1:8079
```

Default listen address is `:8080`

## Forwarding Rules

The proxy supports two forwarding methods:

- `direct`: Connect directly to target server
- `proxy`: Forward through upstream proxy (default: 127.0.0.1:8079) (http protocol)

## Logging

Logs are written to `http_proxy.log` in the current directory.

## Example Configuration

```yaml
rules:
  - domainPattern: "*.cn"
    forwardMethod: "direct"
  - domainPattern: "google.com"
    forwardMethod: "proxy"
  - domainPattern: "*.bilibili.com"
    forwardMethod: "direct"
```

## Global Direct Connection Configuration

To enable global direct connection, add the following rule to your `config.yaml`:

```yaml
rules:
  - domainPattern: "*"
    forwardMethod: "direct"
```

This configuration will forward all HTTP/HTTPS traffic directly without using the proxy server. Use with caution.

## License

GPLv3
