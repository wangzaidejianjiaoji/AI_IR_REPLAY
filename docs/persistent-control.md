# 常驻审核的一键启停

保留原名称 `start-persistent.sh`、`start-persistent.ps1` 和原有参数。两个脚本增加相同的动作：`start`、`stop`、`restart`、`status`；默认动作仍是 start，默认实例仍为 all（GVS 与 infer）。进程查询、身份核对、停止顺序和重启流程共用 `internal/lifecycle` 中的 Go 代码；Bash/PowerShell 保留各自的启动及凭据加载方式，无需为 Linux 安装 PowerShell。

## 首次构建管理工具

从项目根目录执行，要求 Go 1.23+。使用独立管理二进制，可以管理旧实例而不覆盖运行中的审核二进制：

```bash
# Linux
go build -o bin/start-persistent ./cmd/agent
```

```powershell
# Windows；Go 不在 PATH 时替换为本机 go.exe 完整路径
go build -o bin/start-persistent.exe ./cmd/agent
```

脚本优先使用 `bin/start-persistent[.exe]`，不存在时回退到 `bin/gvs-pr-agent[.exe]`。回退的审核二进制也必须由支持 `-action` 的新版源码构建，旧版会提示未知参数。管理工具只是同一程序的另一份构建，不会因运行 status/stop 而加载审查配置、读取模型凭据或调用模型。

## Linux

```bash
bash ./start-persistent.sh                               # 启动全部，与以前相同
bash ./start-persistent.sh --action status                 # 检查全部
bash ./start-persistent.sh gvs --action stop               # 停止 GVS
bash ./start-persistent.sh gvs-infer --action restart        # 重启 infer
bash ./start-persistent.sh --action stop                   # 停止全部

# 原有指定 MR 参数完全保留；编号替换为实际 MR
bash ./start-persistent.sh gvs --mode manual --mr 123 --dry-run
bash ./start-persistent.sh gvs --mode manual --mr 123 --repo lizheng/lzvsg/gvs --checkpoint-dir data/checkpoints/gvs --dry-run
```

## Windows PowerShell

```powershell
.\start-persistent.ps1                                    # 启动全部，与以前相同
.\start-persistent.ps1 -Action status                     # 检查全部
.\start-persistent.ps1 -Action stop -Instance gvs          # 停止 GVS
.\start-persistent.ps1 -Action restart -Instance gvs-infer # 重启 infer
.\start-persistent.ps1 -Action stop                       # 停止全部

# 原有指定 MR 参数完全保留；编号替换为实际 MR
.\start-persistent.ps1 -Mode manual -Instance gvs -MR 123 -DryRun
.\start-persistent.ps1 -Mode manual -Instance gvs -MR 123 -Repo lizheng/lzvsg/gvs -CheckpointDir data/checkpoints/gvs -DryRun
```

`Action start` 可以显式填写。stop/status/restart 只接受实例选择，不能与 Mode、MR、Repo、DryRun 或 CheckpointDir 混用。此处 MR 指 Codeup 合并请求，未增加 GitHub 单 PR 指定入口。手动 MR 审核仍需先停止对应定时实例；dry-run 仍可能收费、写知识和推进成功基线。

## 状态与停止行为

- status 输出 `supervisor=<PID>` 和 `agent=<PID>`，或 `stopped`。发现审核进程时，还读取该实例的 `data/runtime/<实例>.status.json`，展示当前/最近一次审核的任务标识、仓库、标题、阶段、运行中的角色与审核包、开始时间、耗时、输入/输出/合计 token、请求数和检查点命中数。只有 supervisor 表示正在重启等待或审核程序未能启动；只有 agent 可能是手动审核或遗留子进程。查询成功即返回 0，包括 stopped 状态；该命令不验证平台或模型可用性。
- stop 先结束守护程序，再扫描并结束对应审核进程（含该实例的手动 MR 审核），防止被守护程序拉起。已停止时重复执行成功。
- Linux 发送 SIGTERM；Windows 使用进程终止，正在审核的工作可能中断。未完成的审查可能需要重新调用模型；已有发布或知识写入不会回滚。
- 以当前管理工具所在项目的脚本路径、审核可执行文件路径、配置实例和进程创建标识核对目标，不直接信任 PID 文件，不删除锁文件或基线。管理工具应放在当前项目的 bin 目录，并由启动实例的同一账号运行。
- 停止后等待最多 15 秒确认退出；超时返回非零，restart 不继续启动。restart 的启动阶段仍需要原有凭据、二进制和配置；管理命令应串行执行。
- 状态、基线、检查点和日志保留。日志仍在 `data/runtime`。Windows 的 GVS 管理端口沿用 8081，Linux 沿用 8083，infer 均沿用 8082。

### Linux 查看审核进度

```bash
bash ./start-persistent.sh gvs-infer --action status
watch -n 2 'bash ./start-persistent.sh gvs-infer --action status'
tail -f "data/runtime/gvs-infer-$(date +%F).log"
```

审核进程在启动、任务开始/结束及每 15 秒输出一条“运行状态”日志，同时原子更新状态文件。阶段包含 `fetching`、`preparing`、`understanding n/m`、`analysis`、`reviewing/confirming`、`confirming`；`jobs` 列出正在运行的角色和包名。`idle` 表示尚未执行任务；`reviewing` 表示正在执行；`completed` / `interrupted` 表示最近一次尝试的结果，轮询等待期间保留该结果。耗时包括获取变更、知识更新、分析、审查和发布，不是单个模型推理时间。状态文件只存标题和标识，不保存提示词或源码。

token 是**本次尝试中新请求返回的供应商 usage 累计值**，包含知识更新、意图分析、角色审核、统筹确认及备用模型调用；检查点复用计入 `cache_hits`，不会重复计费。正在等待响应的请求尚无 token 数。`usage_missing` 表示未收到可统计 usage 的请求（包括网络/响应失败），此时显示值可能低于实际账单；它不是轮次预算的字节估算/输出预留值。恢复审核后，计数从新尝试开始，历史值可查日志。

status 只展示与发现的 agent PID 一致、最近一分钟内更新过的状态；旧文件显示 `stale`，旧版程序未产生文件时显示 unavailable。升级后需要重新构建审核二进制及独立管理工具 `bin/start-persistent`（如果使用），再重启实例，才能获得新状态字段。当前管理命令仍只支持 GVS / infer 两个约定配置名；其他配置的状态文件按配置文件名存储。

### Codeup MR 版本增量审核

GVS / infer 的 MR 以本地已成功审核的源 SHA、目标 SHA 为基线。首次审核仍读取目标到源的完整合并差异；目标 SHA 未变时，例如上次成功审核版本 5、当前为版本 6，只把版本 5 → 版本 6 的新增代码差异送入模型。版本间使用 `straight=true` 直接比较两个提交的文件树，避免 rebase / 强推后 merge-base 比较重带旧内容或漏掉撤销；接口依据 [Codeup GetCompare](https://www.alibabacloud.com/help/zh/yunxiao/developer-reference/getcompare)。报告“验证总览”会写明增量起止 SHA，旧版本的问题不会作为本次完整重审结果重新汇报。

- 源 / 目标 SHA 都未变：修改标题、描述或版本显示标识不入队重审，不调用模型，不重复发评论。标题描述仍可作为真正代码审核的背景材料。
- 提交 SHA 变化，但直接比较为空：不调用模型、不发新报告；确认远端版本仍一致后记录新的代码基线。
- 版本 6 审核失败：基线停留在版本 5，随后版本 7 必须审核 5 → 7，包含版本 6 中尚未成功审核的变化。
- 目标 SHA 变化或旧记录缺少目标 SHA：重新检查完整 MR 差异，以覆盖合并环境变化。仅修改审核配置/规则也不会让相同代码快照重复审核。
- 审核过程中仅修改标题描述不废弃本次代码审核；源/目标代码、分支、WIP、冲突、关闭状态变化仍会阻止错误发布。代码相同但版本 ID 更新时，评论关联最新的同代码版本。

继续使用原有 `codeup.state_path` 中的 `mr:<仓库>:<MR ID>` 成功记录，不需要清空基线。旧 `input_hash` 包含标题描述也不会强迫同一代码快照重审。检查点和发布回执额外包含增量起点，避免将完整审核和不同起点的增量审核混用。如果上次源提交已不可读取，比较失败会保留原基线并等待处理，不会偷偷改用另一个比较范围。此次调整针对 Codeup MR；GitHub PR 的流程保持原有规则。

### 理解文档的增量更新

已有 `SOURCE-REPOSITORY` 和 `SOURCE-SHA` 的文档，只比较知识版本到源分支新 HEAD 的 diff。证据按 UTF-8 安全边界分批，保留全部 diff；每批最多 12 KB 原始证据，并检查 JSON 转义后的大小。每次只发送受限的既有知识片段及本批证据，让模型返回精确文本替换和必要的新增小节；没有变化的内容在本地保留，不再整体生成文档。旧文档长度超过 60 KB 或累计 diff 超过 90 KB 不再直接拒绝更新。

补丁必须命中已提供的旧文本且在全文中唯一；无效 JSON、歧义替换、来源标记修改均拒绝。所有批次成功后才一次写入文档并推进 SHA；中途失败保留原文档和旧版本，已返回的用量仍计入本次消耗。启用检查点时，重试会复用相同请求的成功批次。首次初始化仍需版本化 Git 源码快照，保留原有快照预算检查。

增量模式降低单次输入/输出大小，但大 diff 会增加调用次数，仍受 `review.max_llm_calls`、`max_round_tokens` 和轮次超时约束。相关旧知识按代码符号与路径词匹配选择，是启发式检索，不能保证所有跨模块关系都会被召回；未选中的旧知识不会被程序删除。此次升级更新审核协议及检查点作用域，未完成任务可能需要额外模型调用；已成功审核且代码快照相同的 Codeup MR 仍按基线跳过。

## 本地验证

```bash
go test -count=1 ./internal/lifecycle ./cmd/agent
bash tools/test-start-modes.sh
# Linux 环境额外执行真实 flock/守护重启测试
bash tools/test-persistent-linux.sh
```

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File tools/test-start-modes.ps1
```

测试使用临时目录、模拟凭据和假审核进程，不访问模型或代码托管平台。`internal/lifecycle` 包包含真实平台脚本的启动、防重复、停止和重启测试；Git Bash 的路由测试不替代 Linux 原生生命周期测试。
