#!/usr/bin/env bash
# 作用：单实例定时审核守护循环；通常由 start-persistent.sh 后台启动。
#   bash ./run-persistent.sh gvs          # 前台守护 GVS
#   bash ./run-persistent.sh gvs-infer    # 前台守护 infer
# 加载 configs/config-<实例>.yaml；凭据来自环境变量或外部 runtime-env.sh。
# 凭据默认 ~/.config/AI_IR_REPLAY/runtime-env.sh；支持 XDG_CONFIG_HOME、AI_IR_RUNTIME_ENV。
# flock 防重复启动；审核进程退出后 15 秒重启，日志在 data/runtime。
# 停止（同时停止审核子进程；infer 将 gvs 替换为 gvs-infer）：
#   kill "$(cat data/runtime/gvs.supervisor.pid)"
# 手动指定 MR 请用 start-persistent.sh；不要删除锁文件绕过运行中的实例。
set -euo pipefail
if [[ $# -ne 1 ]]; then echo 'Usage: bash run-persistent.sh [gvs|gvs-infer]' >&2; exit 2; fi
instance=$1
case "$instance" in
    gvs) port=8083 ;;
    gvs-infer) port=8082 ;;
    *) echo 'Usage: bash run-persistent.sh [gvs|gvs-infer]' >&2; exit 2 ;;
esac
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
cd "$root"
umask 077
mkdir -p data/runtime
lock="data/runtime/$instance.supervisor.lock"
pidfile="data/runtime/$instance.supervisor.pid"
exec 9>"$lock"
if flock -n -E 75 9; then :; else
    status=$?
    [[ $status == 75 ]] && exit 0
    echo "Cannot acquire supervisor lock: $lock" >&2
    exit "$status"
fi
child=''
cleanup() {
    trap '' INT TERM
    if [[ -n $child ]]; then
        kill "$child" 2>/dev/null || true
        wait "$child" 2>/dev/null || true
    fi
    if [[ -f $pidfile && $(cat "$pidfile") == "$$" ]]; then rm -f -- "$pidfile"; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
runtime_env=${AI_IR_RUNTIME_ENV:-${XDG_CONFIG_HOME:-$HOME/.config}/AI_IR_REPLAY/runtime-env.sh}
if [[ -n ${AI_IR_RUNTIME_ENV:-} && ! -f $runtime_env ]]; then echo "Credential file missing: $runtime_env" >&2; exit 1; fi
if [[ -f $runtime_env ]]; then
    set -a
    source "$runtime_env" >/dev/null
    set +a
fi
for required in KIMI_API_KEY CODEUP_TOKEN CODEUP_ORG_ID; do
    if [[ -z ${!required:-} ]]; then echo "Missing environment variable: $required" >&2; exit 1; fi
done
[[ -x bin/gvs-pr-agent ]] || { echo 'Missing executable: bin/gvs-pr-agent' >&2; exit 1; }
[[ -f configs/config-$instance.yaml ]] || { echo "Missing config: configs/config-$instance.yaml" >&2; exit 1; }
unset AGENT_REVIEW_DRY_RUN
export AGENT_SERVER_ADDR="127.0.0.1:$port"
printf '%s\n' "$$" > "$pidfile"
while :; do
    log="data/runtime/$instance-$(date +%F).log"
    printf '[%s] Starting %s\n' "$(date -Is)" "$instance" >> "$log"
    # The child must not inherit the supervisor's flock descriptor.
    ./bin/gvs-pr-agent -config "configs/config-$instance.yaml" >> "$log" 2>&1 9>&- &
    child=$!
    if wait "$child"; then status=0; else status=$?; fi
    child=''
    printf '[%s] Exited: %s; restarting in 15 seconds\n' "$(date -Is)" "$status" >> "$log"
    sleep 15 9>&- &
    child=$!
    wait "$child" || true
    child=''
done
