FROM golang:latest AS builder
ARG TAG=dev  # 设置默认值
ENV CGO_ENABLED=0
COPY . /app
RUN cd /app && go build -ldflags "-X main.version=$TAG" -tags=release -o http_proxy .


FROM scratch
COPY --from=builder /app/http_proxy /usr/bin/http_proxy
COPY --from=builder /app/config.yaml /app/config.yaml
WORKDIR /app
ENTRYPOINT [ "/usr/bin/http_proxy" ]
