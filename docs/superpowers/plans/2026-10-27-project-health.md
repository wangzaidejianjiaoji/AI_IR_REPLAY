# 项目健康度实施计划

**目标：** 随常驻审核自动采集实际 usage，生成本地 HTML 和每日优化审查，不额外调用模型或自动改动审核策略。

**设计：** 复用 progress.Tracker 的启动、15 秒心跳和结束发布；每个实例维护独立健康账本，以 PID、开始时间及任务版本识别一次尝试，用累计计数差值避免心跳重复计量。北京时间跨日按观察到新增用量的日期入账。健康输出目录为 status 所在 runtime 目录的同级 health 目录。报告写入使用临时文件替换，多实例使用报告锁避免覆盖时序倒退。账本损坏时告警并拒绝覆盖。

**技术：** Go 标准库 html/template、encoding/json；沿用 internal/instance 的平台锁，无新增依赖、HTTP 接口或后台调度服务。

## 验收与执行顺序

- [x] 先添加行为测试并确认失败：自动生成报告；心跳/结束/重启去重；跨日累计差值；双实例汇总；异常建议；HTML 转义；账本损坏保护。
- [x] 添加健康账本及统计：日输入/输出/总 token、请求、usage 缺失、缓存命中、尝试、结束、中断、累计耗时、同版本重复尝试及高消耗对象。每日数据独立于原 100 条历史保留。
- [x] 添加 HTML 总览及按日归档：明确数据口径、最近心跳、启发式建议、前日对比和建议消退记录。页面本地自动刷新并按当前时间判断过期心跳。
- [x] 接入 Tracker.Publish；失败只告警；退出刷新最终状态。启动脚本显示输出位置并说明升级需重新构建现有二进制。
- [x] 执行针对性测试和全量 Go 测试、Linux 交叉构建；检查启动模式及生成 HTML。更新运行文档，记录实际验证范围。

## 验证结果

- `go test -count=1 ./...`：通过（Windows）；随后新增的故障隔离测试以 `go test -count=1 ./internal/progress` 验证通过。
- `go vet ./internal/progress`、`bash -n start-persistent.sh run-persistent.sh`、`git diff --check`：通过。
- `bash tools/test-start-modes.sh`：通过，Git Bash 缺少 flock 时使用测试内的替身，仅证明路由和参数行为。
- `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags timetzdata -trimpath -o bin/gvs-pr-agent ./cmd/agent`：通过；产物确认为 Linux ELF x86-64 静态链接程序。
- Linux 原生 `tools/test-persistent-linux.sh` 未运行，当前环境为 Windows。未启动真实审核或访问模型、代码平台。

## 限制

无供应商 usage 的请求可能低估账单；不估算金额；强制结束最多丢失最后一个心跳间隔的增量；跨午夜未收到的响应计入下一日，耗时按采样区间归属而非精确午夜拆分。首次安装不回灌已有截断历史。未运行期间不产生报告。每日“优化”指自动审查和建议追踪，代码和模型参数调整由人工决定。
