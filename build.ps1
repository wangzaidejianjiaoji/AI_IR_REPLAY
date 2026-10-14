# build.ps1 — Windows 本机构建 + 交叉编译 Linux 产物
# 用法: .\build.ps1          （构建 Windows 调试版 + Linux 部署版）

$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

Write-Host "==> go mod tidy"
go mod tidy

Write-Host "==> 构建 Windows 调试版 (bin/gvs-pr-agent.exe)"
$env:CGO_ENABLED = "0"
go build -trimpath -o bin/gvs-pr-agent.exe ./cmd/agent

Write-Host "==> 交叉编译 Linux x86_64 部署版 (bin/gvs-pr-agent-linux-amd64)"
$env:GOOS = "linux"; $env:GOARCH = "amd64"
go build -trimpath -ldflags="-s -w" -o bin/gvs-pr-agent-linux-amd64 ./cmd/agent

# 恢复本机环境
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue

Write-Host "==> 完成，产物在 bin/ 目录"
Get-ChildItem bin | Format-Table Name, Length
