#!/usr/bin/env bash
# 作用：后台定时审核，或前台执行一次指定 MR 审核。在项目目录运行：
#   bash ./start-persistent.sh                                         # 定时：GVS + infer
#   bash ./start-persistent.sh gvs --mode scheduled                     # 定时：仅 GVS
#   bash ./start-persistent.sh gvs-infer                                # 定时：仅 infer
#   bash ./start-persistent.sh gvs --mode manual --mr 123 --dry-run      # 手动：替换编号；移除 --dry-run 发布评论
#   bash ./start-persistent.sh --action status                         # 检查全部实例：进程 + 审核表格（审核对象/阶段/角色包/开始时间/耗时/Token；当前无审核只列历史）
#   bash ./start-persistent.sh gvs --action stop                        # 停止 GVS 守护与审核进程
#   bash ./start-persistent.sh gvs-infer --action restart                # 重启 infer
# 手动须选单个实例，并先停止对应守护进程和审核进程；只执行一次，返回审核退出码。
# 可选 --repo <配置内仓库全路径>、--checkpoint-dir <目录>；默认检查点 data/checkpoints/<实例>。
# 定时模式后台运行，审核进程退出后 15 秒重启；日志在 data/runtime。
# 依赖 Bash、flock（util-linux），后台模式另需 nohup；缺少 bin/gvs-pr-agent 时用 Go 1.23+ 构建。
# 凭据：export KIMI_API_KEY、CODEUP_TOKEN、CODEUP_ORG_ID，或放入外部 runtime-env.sh（chmod 600）。
# 默认 ~/.config/AI_IR_REPLAY/runtime-env.sh；支持 XDG_CONFIG_HOME、AI_IR_RUNTIME_ENV。
# 更新程序前先停止实例，再构建：
#   CGO_ENABLED=0 GOOS=linux go build -tags timetzdata -trimpath -o bin/gvs-pr-agent ./cmd/agent
# 停止定时实例（同时停止审核子进程；infer 将 gvs 替换为 gvs-infer）：
#   kill "$(cat data/runtime/gvs.supervisor.pid)"
set -euo pipefail
usage() { echo 'Usage: bash start-persistent.sh [all|gvs|gvs-infer] [--action start|stop|restart|status] [--mode scheduled|manual] [--mr ID] [--repo PATH] [--dry-run] [--checkpoint-dir DIR]' >&2; }
instance=all
instance_set=false
mode=scheduled
action=start
mode_set=false
mr=''
repo=''
checkpoint_dir=''
dry_run=false
manual_options=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) sed -n '2,/^set -/p' "${BASH_SOURCE[0]}" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
        all|gvs|gvs-infer)
            if [[ $instance_set == true ]]; then usage; exit 2; fi
            instance=$1; instance_set=true; shift ;;
        --action|--mode|--mr|--repo|--checkpoint-dir)
            if [[ $# -lt 2 || -z $2 || $2 == --* ]]; then usage; exit 2; fi
            case "$1" in
                --action) action=$2 ;;
                --mode) mode=$2; mode_set=true ;;
                --mr) mr=$2; manual_options=true ;;
                --repo) repo=$2; manual_options=true ;;
                --checkpoint-dir) checkpoint_dir=$2; manual_options=true ;;
            esac
            shift 2 ;;
        --dry-run) dry_run=true; manual_options=true; shift ;;
        *) usage; exit 2 ;;
    esac
done
case "$action" in start|stop|restart|status) ;; *) usage; exit 2 ;; esac
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
if [[ $action != start ]]; then
    if [[ $manual_options == true || $mode_set == true ]]; then
        echo '--mode/--mr/--repo/--dry-run/--checkpoint-dir require --action start.' >&2; exit 2
    fi
    controller="$root/bin/start-persistent"
    if [[ ! -x $controller ]]; then controller="$root/bin/gvs-pr-agent"; fi
    if [[ ! -x $controller ]]; then
        echo 'Build bin/start-persistent from ./cmd/agent before managing instances.' >&2; exit 1
    fi
    exec "$controller" -action "$action" -instance "$instance"
fi
case "$mode" in
    manual)
        if [[ $instance == all || ! $mr =~ ^[1-9][0-9]*$ ]]; then
            echo 'Manual mode requires gvs|gvs-infer and --mr <positive integer>.' >&2; exit 2
        fi ;;
    scheduled)
        if [[ $manual_options == true ]]; then echo 'MR/repository/dry-run/checkpoint options require --mode manual.' >&2; exit 2; fi ;;
    *) usage; exit 2 ;;
esac
case "$instance" in
    all) instances=(gvs gvs-infer) ;;
    gvs|gvs-infer) instances=("$instance") ;;
esac
dependencies=(bash flock)
if [[ $mode == scheduled ]]; then dependencies+=(nohup); fi
for dependency in "${dependencies[@]}"; do
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
if [[ $mode == manual ]]; then
    cd "$root"
    [[ -f configs/config-$instance.yaml ]] || { echo "Missing config: configs/config-$instance.yaml" >&2; exit 1; }
    exec 9>"data/runtime/$instance.supervisor.lock"
    if ! flock -n 9; then
        echo "Instance $instance is busy. Stop its supervisor and agent before manual review; do not delete lock files." >&2
        exit 1
    fi
    if [[ -z $repo ]]; then
        if [[ $instance == gvs ]]; then repo=lizheng/lzvsg/gvs; else repo=lizheng/lzvsg/gvs_infer; fi
    fi
    checkpoint_dir=${checkpoint_dir:-data/checkpoints/$instance}
    unset AGENT_REVIEW_DRY_RUN
    agent_args=(-config "configs/config-$instance.yaml" -repo "$repo" -mr "$mr" -checkpoint-dir "$checkpoint_dir")
    if [[ $dry_run == true ]]; then agent_args+=(-dry-run); fi
    # One foreground invocation; a failure must not enter the restart loop.
    if "$binary" "${agent_args[@]}" 9>&-; then exit 0; else exit "$?"; fi
fi
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
