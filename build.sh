#!/bin/bash
# build.sh — Linux 服务器构建脚本（与 GVS build.sh 风格一致）
set -e
cd "$(dirname "$0")"

echo "==> go mod tidy"
go mod tidy

echo "==> 构建 Linux x86_64 (bin/gvs-pr-agent)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/gvs-pr-agent ./cmd/agent

echo "==> 完成"
ls -lh bin/
