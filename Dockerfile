FROM golang:latest
ARG TAG=dev  # 设置默认值
COPY . /app
RUN cd /app && go build -ldflags "-X main.version=$TAG" -tags=release -o http_proxy .
RUN cd /app && mv http_proxy /usr/bin/http_proxy
WORKDIR /app
ENTRYPOINT [ "/usr/bin/http_proxy" ]
