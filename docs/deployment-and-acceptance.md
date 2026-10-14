# AI 审查部署、运行与验收

> 维护日期：2026-10-13。依据当前工作区；本文提供步骤与标准，不表示已经执行真实平台发布或生产部署。本轮仅执行本地测试和文档检查。

阅读入口：[项目说明](../GO_AI_PR_Review.md)、[技术框架](../技术框架层级说明.md)、[关键设计](review-design.md)。本机凭据加载见 env.md，该文件仅本机使用。

## 1. 当前验收状态

执行工具链：仓库本地 Go 1.23.12，命令 `.gotmp/go1.23.12/go/bin/go.exe test ./...`。本轮整体退出码 1；`internal/understanding` 的 `TestPrepareRejectsDifferentRepositoryKnowledge` 失败：带其他仓库标记、相同 SHA 的知识文档被复用。其他有测试的包返回 ok，部分来自缓存。

**来源隔离验收未通过**。本轮不修复代码；修复后必须重新运行该测试及全量测试。本文中的部署步骤是待执行操作，真实模型、Codeup/GitHub 写入、Docker 启动、业务 GPU 及跨主机场景均未在本轮验收。

## 2. 配置与运行前提

| 检查项 | 要求/原因 |
|---|---|
| 工具链 | 审查服务 Go 1.23；GVS 业务模块 Go 1.25.0 另行管理 |
| 平台读取 | 目标仓库、组织、endpoint 与账号权限准确；dry-run 不提供访问权限 |
| 模型 | key、兼容 endpoint、三档模型名和超时有效；模型真实兼容性需试点 |
| 发布开关 | 核对 dry_run、notification.pr_comment、codeup.commit_comment/mr_comment |
| 状态 | 试点与正式使用不同 state_path，基线初始策略明确 |
| 知识 | 每仓库独立 document_path，人工核对仓库标记及 SHA，明确 strict |
| 首次初始化 | snapshot_repository 中存在完整来源 commit，Git 可执行；对象缺失需要预先同步 |
| 预算 | 设置调用、预留量、轮次时长；费用预算开启时填写核实过的模型价格与币种 |
| 管理服务 | 本机默认回环；手动触发需要管理令牌；容器绑定地址另行覆盖 |
| 时间窗口 | timezone 和 start/end 配对，-once 也受窗口限制；-diff 不受窗口限制 |

Load 拒绝未知 YAML 字段和多个 YAML 文档；ValidateRuntime 在本地 diff 模式也执行。它不能证明 API key、模型名称或仓库路径在远端可用。程序不自动读取 .env。

示例配置中 GVS/infer 默认 dry_run=true，config-codeup.yaml 默认 false。三档业务模型均配置 kimi-for-coding，provider 不实现多协议切换。现有配置没有 snapshot_repository；已有知识可复用，不代表丢失知识后能自动重建。

## 3. 离线 Mock 验证

在仓库根目录两个终端运行；若 Go 未加入 PATH，替换为本机 Go 可执行文件完整路径。

```powershell
# 终端一
& '.\.gotmp\go1.23.12\go\bin\go.exe' run ./tools/mockllm
```

```powershell
# 终端二
& '.\.gotmp\go1.23.12\go\bin\go.exe' run ./cmd/agent -config configs/config.mock.yaml -diff testdata/sample.patch -dry-run
```

该配置模型 key 为 mock-key，endpoint 为本机 19090，理解未开启；本地 patch 流程不访问 GitHub。预期打印报告，检查路径、行号、角色、过滤和覆盖说明。Mock 不证明理解/确认能力或真实缺陷检出率。本轮没有重新执行这组演示命令，不能据此写为已验收。

## 4. 真实平台试点顺序

1. 从目标示例复制试点配置到 `configs/config-pilot.yaml`，明确目标仓库/分支，隔离 state_path 和 document_path，检查 seed_path 等引用。
2. 在仓库外注入凭据，只检查变量是否存在，不打印值。人工核对知识仓库标记；若首次初始化，提供匹配来源 HEAD 的本地 Git 对象并配置 snapshot_repository。
3. 将试点配置 dry_run=true，必要时关闭三类评论开关；设置轮次预算。备份试点基线与知识文档，再运行下列单轮命令。
4. 读取日志：是否在时间窗口、实际发现/跳过什么版本、是否降级/截断/失败、是否生成报告、是否成功保存基线。不要只看退出码。
5. 对固定样本逐项人工核验问题和代码位置，记录未确认候选、失败角色与核验耗时。
6. 正式发布前准备新的正式状态路径并确定首次基线策略，核实评论写权限及目标版本，再显式调整发布配置。不能直接沿用已推进的 dry-run 基线期待重新审查。

```powershell
# 仅在试点配置已准备完成后执行；仍可能收费和写本地状态/知识
& '.\.gotmp\go1.23.12\go\bin\go.exe' run ./cmd/agent -config configs/config-pilot.yaml -once -dry-run
```

dry-run 只阻止评论。Codeup 正常审查及无文件路径仍推进基线，GitHub 不生成评论去重标记可能反复收费。review_first_push=false 首次只记基线，不能把“没有报告”直接判为故障。MR 版本变化会拒绝发布，等待下轮即可，不能将旧报告标成新版本。

## 5. 容器部署说明

当前 Compose：configs 只读，data/docs 可写，传递 GitHub、Codeup、模型和管理环境变量；宿主端口绑定 127.0.0.1:8081；容器通过 AGENT_SERVER_ADDR=0.0.0.0:8081 监听。AGENT_CONFIG_PATH 默认 /app/configs/config.yaml，AGENT_REVIEW_DRY_RUN 默认 true，AGENT_ADMIN_TOKEN 为必填。

启动前检查环境、持久目录和配置。不要将 `docker compose config` 的完整输出粘贴到材料中，它可能展开凭据；只做语法检查可以用 `docker compose config --quiet`。准备完成后才运行：

```powershell
docker compose config --quiet
docker compose up -d --build
docker compose logs --tail 100 pr-agent
```

上面命令未在本轮执行。镜像含 git，但当前 Compose 没有业务仓库挂载；首次知识初始化需要为来源 Git 仓库增加只读挂载，在配置中填容器可见路径，例如 /source/gvs，而不是 D:/ 路径。这是待完成的部署配置，不是当前文件已经包含的内容。

使用其他配置时，state_path/document_path 必须落在实际持久卷内。docs 挂载会遮盖镜像自带文档，宿主目录要包含所需知识与引用文件；不要把空目录挂载误当成自动恢复。data 也承载实例锁，变更路径会改变互斥范围。容器重建后应核对基线与知识保持一致，不能只检查进程重启。

## 6. 管理接口与运行判定

| 请求 | 预期 | 限制 |
|---|---|---|
| GET /health | 200，status=ok | 仅 liveness，不测仓库、模型或轮次 |
| POST /trigger，无配置令牌 | 503 | 手动触发禁用 |
| POST /trigger，错误/缺 Bearer | 401 | 不应触发 |
| POST /trigger，正确 Bearer | 202 | 忙时可跳过，时间窗口可跳过，需日志确认 |

以下触发会启动真实轮次，只在已准备好的试点环境执行；令牌来自环境，不写字面值：

```powershell
Invoke-RestMethod -Uri 'http://127.0.0.1:8081/health'
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8081/trigger' -Headers @{ Authorization = 'Bearer ' + $env:AGENT_ADMIN_TOKEN }
```

本机 infer 示例为 8082，Mock 为 18081；Compose 对外统一 8081。健康检查无鉴权；非回环监听要求令牌，但服务没有 TLS。监控至少区分存活、轮次失败、模型失败、预算取消、知识降级、覆盖不完整与发布/落盘失败。目前没有完整 readiness 或轮次状态查询 API。

## 7. 异常恢复

| 症状 | 当前含义 | 处理建议 |
|---|---|---|
| 实例锁失败 | 相同资源路径被占用或锁操作失败 | 查运行实例、卷和权限；停止旧实例后重试，不直接删锁文件 |
| 来源文档不匹配 | 当前代码可能不拒绝错误仓库文档 | 停止混用，人工恢复正确仓库的版本文档，优先修复来源隔离 |
| 首次初始化失败 | Git 对象/路径/快照预算或模型预算不足 | 核对完整 SHA、文件范围及字节上限，不用 seed 冒充完整证据 |
| 知识更新失败 | strict 阻断或 non-strict 旧知识降级 | 对照报告 SHA 与目标 HEAD；按失败阶段恢复，不擅自更新 SHA |
| 预算耗尽 | 共享轮次上下文取消 | 检查包×角色与重试量；调整试点规模/预算，勿直接无限放宽 |
| 评论成功后状态失败 | 远端与本地没有原子事务 | 先核对平台评论和 SHA，再决定重跑；预期可能重复评论 |
| MR 审查期间有新推送 | 快照复查失败 | 等待下一轮审查当前版本，不推进旧基线 |
| -once 退出 0 无报告 | 可能窗口跳过、首次基线、无文件或仓库错误 | 读取日志而不是把退出 0 判为成功 |

回滚部署时先停止旧进程，再恢复对应配置、基线和知识副本。恢复较旧基线会重审并可能重复评论；恢复较旧知识会触发增量更新或降级。不要只恢复二进制而忽略本地状态版本。

## 8. 验收矩阵

“有本地测试”仅指本轮测试输出及源码断言，不代替完整场景验收。

| 项目 | 当前证据 | 验收条件 |
|---|---|---|
| 知识仓库隔离 | 已有失败测试 | 不同仓库文档必须拒绝，不作为复用或兜底 |
| 状态保存失败 | state 测试返回 ok | 内存与磁盘都保持旧基线，后续能重试 |
| 知识兜底/初始化 | 理解包除隔离项未见其他失败 | strict 阻断、non-strict 标注旧 SHA；无证据不能生成新知识 |
| MR 快照 | review/snapshot_test.go，review 包 ok | compare 使用捕获 SHA；版本变化不评论、不推进 |
| 确认协议 | coordinator 包 ok | 无改写、非法 ID 阻断、未处理候选披露 |
| 覆盖和作业统计 | coordinator/notifier 包 ok | 零问题且有缺口不得误解为全文通过 |
| 预算/超时 | llm/review 包 ok | 重试纳入预算、超限取消、不发布当前结果 |
| 配置/鉴权/单机锁 | config/cmd/instance 包 ok | 非法配置拒绝、错误令牌不触发、同路径实例互斥 |
| dry-run 隔离 | 当前行为已核对，环境操作待验收 | 不评论，状态副作用明确，正式路径不受试点影响 |
| 容器持久化/初始化 | Compose/Dockerfile 静态核对 | 重建保留基线/知识，业务 Git 对象路径可用 |
| 真实平台契约 | 本轮未验证 | 读取、compare、版本与评论权限分别验收 |
| 模型质量与成本 | 本轮未验证 | 固定 diff 和专家标注，报告误报/漏报边界及真实用量 |

全量验证命令：

```powershell
& '.\.gotmp\go1.23.12\go\bin\go.exe' test ./...
```

修复当前来源隔离问题后，先运行其用例，再全量回归；不要将局部通过表述为全量通过。build.ps1/build.sh 会执行 go mod tidy，不用于只读检查。需要单独编译时可输出到临时目录，不改模块文件。

## 9. 验收记录格式

每次记录日期/操作者、审查服务提交与未提交改动状态、配置标识（不含凭据）、仓库与源/目标/知识 SHA、模式和发布开关、命令退出码、失败阶段、覆盖与未确认数、实际 Usage/估算预算/账单口径、人工核验结果及报告存放位置。

服务工作区尚未提交时，提交 SHA 无法单独复现这套实现，应同时保存对应差异及新增文件清单。业务两份理解文档的 SHA 是独立来源版本，不随审查服务文档维护更改。
