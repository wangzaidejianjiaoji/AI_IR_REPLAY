#!/usr/bin/env bash
# Isolated mode/argument tests; never contacts Codeup or a model.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
temp=$(mktemp -d)
fixture="$temp/project with spaces"
trap 'rm -rf -- "$temp"' EXIT
mkdir -p "$fixture/bin" "$fixture/configs" "$temp/tools"
cp "$root/start-persistent.sh" "$fixture/"
touch "$fixture/configs/config-gvs.yaml" "$fixture/configs/config-gvs-infer.yaml"
cat > "$fixture/bin/gvs-pr-agent" <<'AGENT'
#!/usr/bin/env bash
printf '%s\n' "$@" > "$TEST_ARGS_FILE"
exit "${TEST_EXIT_CODE:-0}"
AGENT
cat > "$fixture/run-persistent.sh" <<'SUPERVISOR'
#!/usr/bin/env bash
printf '%s\n' "$1" >> "$(dirname -- "${BASH_SOURCE[0]}")/scheduled.txt"
SUPERVISOR
chmod +x "$fixture/bin/gvs-pr-agent"
# Git Bash lacks flock: exercise routing with a fake; real lifecycle/locking
# remains covered by tools/test-persistent-linux.sh on Linux.
if ! command -v flock >/dev/null 2>&1; then
    printf '#!/usr/bin/env bash\nexit 0\n' > "$temp/tools/flock"
    chmod +x "$temp/tools/flock"
    export PATH="$temp/tools:$PATH"
    echo 'NOTE: flock unavailable; this test covers routing, not OS locking.'
fi
export KIMI_API_KEY=mock CODEUP_TOKEN=mock CODEUP_ORG_ID=mock
export AI_IR_RUNTIME_ENV="$temp/credentials.sh"
touch "$AI_IR_RUNTIME_ENV"
export TEST_ARGS_FILE="$temp/args.txt" TEST_EXIT_CODE=42
mv "$AI_IR_RUNTIME_ENV" "$temp/credentials.saved"
for action in stop status restart; do
    status=0
    bash "$fixture/start-persistent.sh" gvs --action "$action" || status=$?
    [[ $status == 42 ]] || { echo "FAIL: action routing $action: $status" >&2; exit 1; }
    printf '%s\n' -action "$action" -instance gvs > "$temp/want.txt"
    diff -u "$temp/want.txt" "$TEST_ARGS_FILE"
done
mv "$temp/credentials.saved" "$AI_IR_RUNTIME_ENV"
status=0
bash "$fixture/start-persistent.sh" gvs --mode manual --mr 27 --dry-run --checkpoint-dir 'data/checkpoints with spaces' || status=$?
[[ $status == 42 ]] || { echo "FAIL: manual exit code $status != 42" >&2; exit 1; }
printf '%s\n' -config configs/config-gvs.yaml -repo lizheng/lzvsg/gvs -mr 27 -checkpoint-dir 'data/checkpoints with spaces' -dry-run > "$temp/want.txt"
diff -u "$temp/want.txt" "$TEST_ARGS_FILE"
[[ ! -f "$fixture/scheduled.txt" ]]
export TEST_EXIT_CODE=0
bash "$fixture/start-persistent.sh" --mode manual gvs-infer --mr 81 --repo other/configured-repository
printf '%s\n' -config configs/config-gvs-infer.yaml -repo other/configured-repository -mr 81 -checkpoint-dir data/checkpoints/gvs-infer > "$temp/want.txt"
diff -u "$temp/want.txt" "$TEST_ARGS_FILE"
for invalid in missing-instance missing-mr negative-mr scheduled-mr scheduled-dry-run scheduled-checkpoint unknown invalid-action stop-mr status-mode; do
    case "$invalid" in
        missing-instance) args=(--mode manual --mr 27) ;;
        missing-mr) args=(gvs --mode manual) ;;
        negative-mr) args=(gvs --mode manual --mr -1) ;;
        scheduled-mr) args=(gvs --mr 27) ;;
        scheduled-dry-run) args=(gvs --dry-run) ;;
        scheduled-checkpoint) args=(gvs --checkpoint-dir data/cache) ;;
        unknown) args=(gvs --invalid) ;;
        invalid-action) args=(gvs --action delete) ;;
        stop-mr) args=(gvs --action stop --mr 27) ;;
        status-mode) args=(gvs --action status --mode scheduled) ;;
    esac
    rm -f "$TEST_ARGS_FILE"
    if bash "$fixture/start-persistent.sh" "${args[@]}" >/dev/null 2>&1; then echo "FAIL: accepted $invalid" >&2; exit 1; fi
    [[ ! -f "$TEST_ARGS_FILE" ]]
done
export TEST_EXIT_CODE=0
bash "$fixture/start-persistent.sh"
sort "$fixture/scheduled.txt" > "$temp/actual.txt"
printf '%s\n' gvs gvs-infer > "$temp/want.txt"
diff -u "$temp/want.txt" "$temp/actual.txt"
echo 'PASS: lifecycle routing without credentials, manual arguments, exit codes, invalid input, default scheduled launch.'
