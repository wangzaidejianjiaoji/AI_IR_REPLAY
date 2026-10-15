# !556 处理复盘与审查流程优化

## 已核验的结果

2026-10-15，GVS !556「fix focus」的报告已发布并回读核验。

- MR：https://codeup.aliyun.com/lizheng/lzvsg/gvs/change/556
- 评论 ID：`30a825363a974b479077ea301c0ef87b`，关联源版本 17。
- 源 SHA：`1c511a210316bebdd8ccb56417b9bad3ba37b146`。
- 目标 SHA：`5cc010e68729a4a9d81759f85100923be076188a`。
- 覆盖 89 个变更文件：88 个文本完整 diff；1 个二进制只核验变更元数据，未标记通过。
- 88 个文本源码的 Git blob 哈希均与 Codeup 返回的 `newId` 匹配。
- 本对话模型的三个模块审查代理和统筹复核保留 9 项问题：1 项 P1、8 项 P2。
- 87 个 Go 文件语法解析通过；原始背景 mask 代码和测试的隔离执行为 6 项通过、3 项失败。
- 没有执行完整 Go 1.25/Linux/CGO、race 或真实设备/推理服务验证，也没有自动修改 GVS 业务代码。

本机证据位于 `data/mr556/publication-receipt.json`、`report-readback.md`、`coverage.json`、`source-evidence.json` 和 `mask-repro-result.txt`。这些是本次执行产物，不是公共测试夹具。

## 为什么没有及时产出评论

| 阶段 | 实测情况 | 处理与教训 |
|---|---|---|
| 历史运行 | 75 个筛选后文件被 20 文件上限拦截 | 不能把增加轮次超时当作覆盖率修复；超限必须明确失败 |
| 调度 | 每轮先审查分支推送，再进入 MR；!556 曾等待无关分支 | 精准任务应直接选择仓库和 MR ID，不依赖标题猜分支 |
| 凭据 | 旧 `runtime-env.ps1` 令牌返回 401；用户指定 `env.md` 的凭据可访问 MR/compare | 凭据来源以用户指定为准，先预检实际权限，只输出是否成功 |
| 模型 | 历史为 5 小时额度；恢复时 Kimi 明确返回 7 天额度耗尽 | 必须区分限额窗口；等待 5 小时不等于保证恢复 |
| 备用模型 | 45 秒预检超时 | 超时不能证明密钥或模型不可用，应与真实请求预算区分 |
| 续跑 | 原程序只保存成功基线，没有保存每次成功模型响应 | 失败后重跑会重复付出理解和专业审查成本 |
| 代码证据 | 文件 API 无权限；Git 抓取失败 | 用本机已有精确 blob、旧 blob 加 diff 或新增文件全文恢复，并校验哈希；没有拿可变工作树冒充固定版本 |
| 发布 | 接口规范化换行并去掉末尾换行 | 回读只允许 CRLF/LF 与末尾换行差异；必须核对完整正文和关联 SHA |

2026-10-15 的快照捕获时间为 09:59:56，最终回读验证为 10:25:27（Asia/Shanghai），约 25 分 31 秒。这包含诊断、源码恢复、并行审查、验证和发布，并非纯模型推理耗时。此前长时间等待主要受认证/额度阻塞，不能据此声称代码优化带来了某个真实提速百分比。

过程中出现过一次凭据行脱敏遗漏。以后不打印凭据文件原文，不使用宽松的整行脱敏代替按允许字段输出。`env.md` 当前被 Git 跟踪，文件内“已忽略”的说明不能作为事实依据；本次没有改写或提交其中的凭据。

## 已实现的代码改进

### 两种通用运行模式

!556 只是本次临时指定的 MR，不是长期目标。生产入口不保存该编号，也不通过 `fix focus` 标题匹配任务。手动参数仅对本次进程有效；后续每次可以选择配置内的仓库和任意开启状态 MR 编号。

| 模式 | 入口 | 范围与结束条件 |
|---|---|---|
| 手动指定 | `-repo <仓库全路径> -mr <正整数编号>` | 只审查指定 MR，单次完成或失败后退出 |
| 定时审查 | 不传 `-repo/-mr`、`-once`、`-diff` | 启动即扫描一轮，随后按 `scheduler.interval` 和 `jitter` 扫描配置范围 |

`-once` 是全配置范围单轮扫描，不是指定 MR 模式。管理端点 `POST /trigger` 也触发全范围扫描，不接受指定 MR。GVS 配置已移除本次临时的 `ws100-1012` 源分支限制；定时模式将扫描配置仓库内符合条件的分支推送和开启状态 MR，范围扩大后可能增加模型调用。需要限制范围时使用 `branch_filter`；`commit_comment: false` 只关闭提交评论，不关闭分支审查。

### 手动指定 MR

两个启动脚本现在都可选择运行模式，默认保持后台定时启动两个实例。指定模式没有默认 MR，也不会启动另一个实例或进入失败自动重启循环：

```powershell
# Windows：前台指定一次；移除 -DryRun 才会按配置发布评论
.\start-persistent.ps1 -Mode manual -Instance gvs -MR 123 -DryRun
# 后台定时：仅 GVS；不传 -Instance 时同时启动 GVS 和 infer
.\start-persistent.ps1 -Mode scheduled -Instance gvs
```

```bash
# Linux：相同选择方式
bash ./start-persistent.sh gvs --mode manual --mr 123 --dry-run
bash ./start-persistent.sh gvs --mode scheduled
```

`123` 是示例编号，运行时替换为实际 MR。手动模式支持 PowerShell 的 `-Repo` / `-CheckpointDir` 和 Bash 的 `--repo` / `--checkpoint-dir`。默认仓库随实例选择，默认检查点为 `data/checkpoints/<实例>`。仓库是否已配置由审核程序校验；手动模式前台显示审查日志并返回程序退出码，定时模式继续使用现有后台日志和重启策略。

启动脚本使用 `bin/gvs-pr-agent.exe`（Windows）或 `bin/gvs-pr-agent`（Linux），必须是包含 `-repo/-mr/-checkpoint-dir` 的新版程序。`data/build` 下的验证构建不会自动替换部署程序。Windows 停止对应守护进程和审核进程后，可以构建到 `bin`：

```powershell
& .\.gotmp\go1.23.12\go\bin\go.exe build -o ./bin/gvs-pr-agent.exe ./cmd/agent
```

Linux 的构建命令见 `start-persistent.sh` 顶部说明。凭据加载仍遵循平台原来的外部凭据文件，启动脚本不读取项目内的 `env.md`。

新增 `Service.ReviewCodeupMR` 和 CLI `-repo`、`-mr`。只能选择配置内仓库，直接处理明确 MR，不进入分支推送或其他 MR 的审查。精准选择有意绕过轮询用的源分支过滤器；时间窗口、WIP/冲突、完整覆盖、dry-run、评论开关和发布前版本复查仍然生效。

参数不完整、MR 不存在、缺少版本、没有有效待审文件或审查失败时，精准 CLI 返回非零退出码。原 `-once` 的全仓轮询行为未改造，不应仅凭其退出码判断审查成功。

先注入配置引用的环境变量，并释放对应常驻实例持有的状态/理解文档锁，然后可运行：

```powershell
$selectedMR = [int](Read-Host '请输入要审查的 MR 正整数编号')
.\data\build\gvs-pr-agent.exe -config configs/config-gvs.yaml -repo lizheng/lzvsg/gvs -mr $selectedMR -checkpoint-dir ./data/checkpoints/gvs -dry-run
```

正式发布时移除 `-dry-run`；必须确认所用 YAML 的 MR 评论开关和状态路径。程序不自动把 Markdown 凭据文件作为机器配置执行。本次 !556 已发布，命令仅示范后续工作流，不应重新发布同一报告。

### 定时审查与模式切换

```powershell
.\data\build\gvs-pr-agent.exe -config configs/config-gvs.yaml -checkpoint-dir ./data/checkpoints/gvs
```

两个入口共用审查管线、完整覆盖检查、版本校验和基线；手动启动脚本默认开启成功响应检查点，定时模式按 YAML 的 `review.checkpoint_dir` 配置启用。同一状态文件或理解文档只能由一个进程持锁：常驻实例运行期间，另起手动 CLI 会失败退出。启动脚本的手动模式还会持有对应的守护锁，防止定时守护进程在重启间隔中拉起审核进程。切换时停止对应常驻实例，执行手动任务，再重新启动定时实例；不要通过删除锁或改用另一份基线绕过保护，否则可能重复评论。当前没有实现常驻实例内接收指定 MR 的管理接口。

### 固定版本的模型请求检查点

可选 `review.checkpoint_dir`，也可用 `-checkpoint-dir` 覆盖。默认关闭，不改变既有运行配置。

检查点作用域包含仓库、MR ID、完整源 SHA 和目标 SHA；请求键进一步包含完整提示词（包括规则、知识文档和分包 diff）、模型、生成参数、主备供应商和账号指纹。源/目标提交、提示词或模型配置改变，相关请求不会命中旧缓存。

只保存 HTTP 成功的非空响应，使用临时文件后原子替换。请求正文、认证头及明文 API key 不写入检查点；响应内容仍可能包含代码派生信息，目录应留在本机忽略路径中。解析或协议检查拒绝的响应会从缓存删除，覆盖专业 Agent、变更分析、统筹确认和知识文档维护。

恢复时复用成功响应，重新执行解析、定位、反思、完整覆盖、统筹与版本校验。缓存命中不增加本轮 HTTP/Token 预算，返回的 usage 为零；原 usage 作为检查点元数据保留。因此恢复轮次的报告 Token 只表示本轮新调用，不是该 MR 历次累计消费。

这属于模型请求级复用，不是任意代码位置恢复。失败/未完成的请求仍需重做，确认候选组合改变时也可能重新请求确认。重跑仍会重新取得远端 diff。文件写入失败会记录警告，不保证该次响应可恢复。

### 限额分类与供应商冷却

依据 HTTP 状态和限额证据返回 `QuotaError`，不再用错误文本中任意出现的“429”判断限额。识别 403 的五小时/周额度，以及 429 的速率限制；普通 401/403 不自动归为额度问题。

- 优先使用有效 `Retry-After`。
- 没有服务器时间时，速率限制默认一分钟后重检；五小时/周额度默认五小时后重检。
- `RetryAt` 是下次检查时间，不声称是供应商实际额度重置时间。
- 同一客户端在冷却内跳过该供应商的新 HTTP 请求，有备用供应商时直接走备用。
- 启用检查点时保存冷却信息，后续同账号客户端可以读取；凭据指纹改变不继承旧账号冷却。
- 对已在途的并行请求，后返回的短期限额不能覆盖已知更长的冷却；状态合并和本客户端的持久化写入使用同一锁。

不在请求函数中睡五小时，也不占用审查线程等待。精准 CLI 失败退出；常驻调度或外部续办任务再次运行时，根据冷却时间决定是否重新请求。缓存命中可以在额度未恢复时复用；未完成的模型工作仍需可用模型。API 程序不能把“本对话模型”当作自动可调用的 API 密钥，本轮对话模型审查是用户另行授权的工作流。

### 知识来源校验

显式 `SOURCE-REPOSITORY` 与配置不一致时拒绝复用，也不作为 permissive 降级快照返回。修复了原有 `TestPrepareRejectsDifferentRepositoryKnowledge` 失败。没有仓库标记的历史文档仍保留兼容，应逐步补齐来源标记；仅 SHA 一致并不足以证明来源正确。

## 验证要求

回归测试覆盖精准 CLI 错误退出、只选择一个 MR、WIP/冲突和版本变化拒绝发布、失败不推进基线、重启复用成功结果、完整源/目标 SHA 变化失效、只补跑失败专业请求、坏响应/损坏文件淘汰、缓存命中不消耗新预算、主备冷却与凭据切换，以及长冷却不被短冷却覆盖。

测试和构建只使用本地模拟服务，不重新向 !556 发评论或消费真实模型额度：

```powershell
$reviewGo = '.\.gotmp\go1.23.12\go\bin\go.exe'
& $reviewGo test ./...
& $reviewGo vet ./...
& $reviewGo build -o ./data/build/gvs-pr-agent.exe ./cmd/agent
```

本轮未自动重启常驻实例，运行中的旧二进制不会因源码改变而自行更新。替换二进制前先停止对应实例，并保留其状态、理解文档和检查点目录。

启动入口可用 `tools/test-start-modes.ps1` 和 `bash tools/test-start-modes.sh` 运行隔离测试，验证参数选择、退出码、路径空格和默认定时行为；PowerShell 另外验证真实文件锁互斥。Linux 守护进程生命周期和真实 flock 验证使用 `bash tools/test-persistent-linux.sh`，需要安装 util-linux。
