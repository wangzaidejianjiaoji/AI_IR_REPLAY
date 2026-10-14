# 多阶段构建：编译产物为静态二进制（CGO_ENABLED=0），运行镜像极小
FROM golang:1.23-alpine AS builder
# GOPROXY 支持国内代理，避免直连 proxy.golang.org 超时（可用 --build-arg 覆盖）
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o gvs-pr-agent ./cmd/agent

FROM alpine:3.20
# 切换 apk 源到国内镜像，避免直连 dl-cdn.alpinelinux.org 超时
RUN sed -i 's#https\?://dl-cdn.alpinelinux.org#https://mirrors.aliyun.com#g' /etc/apk/repositories \
    && apk add --no-cache ca-certificates tzdata git
WORKDIR /app
COPY --from=builder /build/gvs-pr-agent .
COPY configs ./configs
COPY docs ./docs
COPY GO_AI_PR_Review.md ./GO_AI_PR_Review.md
ENV TZ=Asia/Shanghai
EXPOSE 8081
ENTRYPOINT ["./gvs-pr-agent"]
CMD []
