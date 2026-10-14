#!/usr/bin/env bash
# Linux 使用方式：bash ./start-persistent.sh（默认启动 GVS 和 infer）。
# 首次需提供模型与 Codeup 凭据；Windows 的 runtime-env.ps1 不适用于 Bash。
# 可创建 ~/.config/AI_IR_REPLAY/runtime-env.sh，填写如下三项的真实值：
#   export KIMI_API_KEY='your-key'
#   export CODEUP_TOKEN='your-token'
#   export CODEUP_ORG_ID='your-org-id'
# 文件应使用 LF 换行；保存后 chmod 600 ~/.config/AI_IR_REPLAY/runtime-env.sh。
# 也可在当前终端 export 上述变量，脚本会将其传给后台守护进程。
# Linux one-command startup (does not require PowerShell):
#   bash ./start-persistent.sh              # GVS + infer
#   bash ./start-persistent.sh gvs          # GVS only
#   bash ./start-persistent.sh gvs-infer     # infer only
# Optional: chmod +x start-persistent.sh run-persistent.sh; ./start-persistent.sh
#
# Prerequisites: Bash, nohup, flock (util-linux). If bin/gvs-pr-agent is missing,
# Go 1.23+ builds it for this Linux host; Windows .exe files are not used.
# To rebuild after a code update, stop the instances and run:
#   CGO_ENABLED=0 GOOS=linux go build -tags timetzdata -trimpath -o bin/gvs-pr-agent ./cmd/agent
#
# Credentials: export KIMI_API_KEY, CODEUP_TOKEN, CODEUP_ORG_ID in your shell,
# or put their export statements in ~/.config/AI_IR_REPLAY/runtime-env.sh.
# XDG_CONFIG_HOME is respected; AI_IR_RUNTIME_ENV can specify another file.
# Keep credential files outside Git and chmod 600. Values are never printed.
#
# Runs detached from the terminal, prevents duplicate instances, and restarts
# an exited agent after 15 seconds. This is NOT a boot-time system service:
# reboot requires starting again (or separately configuring systemd).
# Logs: data/runtime/<instance>-<start-date>.log (UTF-8).
# Health: http://127.0.0.1:8081/health (GVS), :8082/health (infer).
# Stop an instance (supervisor also terminates its child):
#   kill "$(cat data/runtime/gvs.supervisor.pid)"
#   kill "$(cat data/runtime/gvs-infer.supervisor.pid)"
# The current configuration publishes MR comments; LLM calls can incur costs.
set -euo pipefail
if [[ $# -gt 1 ]]; then echo 'Usage: bash start-persistent.sh [all|gvs|gvs-infer]' >&2; exit 2; fi
instance=${1:-all}
case "$instance" in
    -h|--help) sed -n '2,/^set -/p' "${BASH_SOURCE[0]}" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
    all) instances=(gvs gvs-infer) ;;
    gvs|gvs-infer) instances=("$instance") ;;
    *) echo 'Usage: bash start-persistent.sh [all|gvs|gvs-infer]' >&2; exit 2 ;;
esac
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
for dependency in bash nohup flock; do
    if ! command -v "$dependency" >/dev/null 2>&1; then echo "Missing dependency: $dependency" >&2; exit 1; fi
done
runtime_env=${AI_IR_RUNTIME_ENV:-${XDG_CONFIG_HOME:-$HOME/.config}/AI_IR_REPLAY/runtime-env.sh}
if [[ -n ${AI_IR_RUNTIME_ENV:-} && ! -f $runtime_env ]]; then echo "Credential file missing: $runtime_env" >&2; exit 1; fi
if [[ -f $runtime_env ]]; then
    set -a
    source "$runtime_env" >/dev/null
    set +a
fi
for required in KIMI_API_KEY CODEUP_TOKEN CODEUP_ORG_ID; do
    if [[ -z ${!required:-} ]]; then echo "Missing environment variable: $required; configure $runtime_env or export it." >&2; exit 1; fi
done
binary="$root/bin/gvs-pr-agent"
if [[ ! -x $binary ]]; then
    if ! command -v go >/dev/null 2>&1; then echo 'Missing Linux binary and Go: install Go 1.23+ or build bin/gvs-pr-agent for this host.' >&2; exit 1; fi
    mkdir -p "$root/bin"
    (cd "$root" && CGO_ENABLED=0 GOOS=linux go build -tags timetzdata -trimpath -o "$binary" ./cmd/agent)
fi
umask 077
mkdir -p "$root/data/runtime"
for name in "${instances[@]}"; do
    nohup bash "$root/run-persistent.sh" "$name" >"$root/data/runtime/$name-supervisor.log" 2>&1 </dev/null &
    supervisor=$!
    sleep 0.2
    if kill -0 "$supervisor" 2>/dev/null; then
        echo "Start requested: $name (supervisor PID $supervisor)."
    elif wait "$supervisor"; then
        echo "Already running: $name."
    else
        echo "Startup failed: $name; see data/runtime/$name-supervisor.log." >&2
        exit 1
    fi
done
