#!/usr/bin/env bash
# Linux smoke test: isolated fake agent; never contacts Codeup or an LLM.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
bash -n "$root/start-persistent.sh" "$root/run-persistent.sh"
bash "$root/start-persistent.sh" --help >/dev/null
if bash "$root/start-persistent.sh" invalid >/dev/null 2>&1; then
    echo 'FAIL: invalid instance accepted' >&2; exit 1
fi
if ! command -v flock >/dev/null 2>&1; then
    echo 'CLI checks passed; Linux lifecycle test requires util-linux flock.' >&2
    exit 77
fi
temp=$(mktemp -d)
fixture="$temp/project with spaces"
supervisor=''
cleanup() {
    if [[ -n $supervisor ]]; then kill "$supervisor" 2>/dev/null || true; fi
    # Restrict deletion to the directory created by this test.
    rm -rf -- "$temp"
}
trap cleanup EXIT
mkdir -p "$fixture/bin" "$fixture/configs"
touch "$fixture/configs/config-gvs.yaml"
cp "$root/start-persistent.sh" "$root/run-persistent.sh" "$fixture/"
cat > "$temp/runtime-env.sh" <<'ENV'
export KIMI_API_KEY=mock CODEUP_TOKEN=mock CODEUP_ORG_ID=mock
ENV
export AI_IR_RUNTIME_ENV="$temp/runtime-env.sh"
cat > "$fixture/bin/gvs-pr-agent" <<'AGENT'
#!/usr/bin/env bash
printf 'START %s %s\n' "$$" "$*" >> data/runtime/events.log
trap 'printf "STOP %s\n" "$$" >> data/runtime/events.log; exit 0' TERM INT
if [[ ! -f data/runtime/first-exit ]]; then
    touch data/runtime/first-exit
    exit 42
fi
while :; do sleep 1; done
AGENT
chmod +x "$fixture/bin/gvs-pr-agent"
bash "$fixture/start-persistent.sh" gvs
supervisor=$(cat "$fixture/data/runtime/gvs.supervisor.pid")
bash "$fixture/start-persistent.sh" gvs
[[ $(cat "$fixture/data/runtime/gvs.supervisor.pid") == "$supervisor" ]]
for ((i=0; i<30; i++)); do
    if [[ $(grep -c '^START ' "$fixture/data/runtime/events.log") -ge 2 ]]; then break; fi
    sleep 1
done
[[ $(grep -c '^START ' "$fixture/data/runtime/events.log") == 2 ]]
child=$(awk '/^START / {pid=$2} END {print pid}' "$fixture/data/runtime/events.log")
kill "$supervisor"
for ((i=0; i<10; i++)); do
    [[ ! -f "$fixture/data/runtime/gvs.supervisor.pid" ]] && break
    sleep 1
done
[[ ! -f "$fixture/data/runtime/gvs.supervisor.pid" ]]
if kill -0 "$child" 2>/dev/null; then echo 'FAIL: orphan agent remains' >&2; exit 1; fi
supervisor=''
echo 'PASS: paths with spaces, detached launch, duplicate prevention, restart and stop cleanup.'
