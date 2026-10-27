# AI\_IR\_REPLAY 本地运行环境说明

> 维护日期：2026-10-14。本文件受 .gitignore 排除，仅限本机使用。本文登记真实 Kimi API key，禁止提交到 Git 仓库或复制到团队材料；不登记 PAT。

## 1. 凭据保存与加载

### Kimi 本地凭据登记

| 项              | 值                                                                                    |
| -------------- | ------------------------------------------------------------------------------------ |
| KIMI\_API\_KEY | `${KIMI_API_KEY}`                                                                    |
| base\_url      | `https://api.kimi.com/coding/v1`                                                     |
| 模型             | **[kimi-k2.7-code](https://platform.kimi.com/docs/guide/kimi-k2-7-code-quickstart)** |

### minimax 本地凭据登记

| 项                 | 值                             |
| ----------------- | ----------------------------- |
| MiniMax\_API\_KEY | `${MiniMax_API_KEY}`          |
| base\_url         | `https://api.minimaxi.com/v1` |
| 模型                | `MiniMax-M3`                  |

### Codeup 本地凭据登记（2026-10-14 用户临时登记，本文件已在 Git 跟踪，禁止 `git commit` 推送本版到远程）

| 项               | 值                                                                  |
| --------------- | ------------------------------------------------------------------ |
| CODEUP\_TOKEN   | `pt-HE9urvi1slnXHz5jeshN8M4a_83b99f21-5ad6-4cb7-998d-1c18c123a8b4` |
| CODEUP\_ORG\_ID | `6515185d921086200571953d`                                         |

密钥取自本项目的 runtime-env.ps1，于 2026-10-14 重新登记；本次未调用线上 API 验证有效性。

运行时使用的 KIMI\_API\_KEY、CODEUP\_TOKEN 和 CODEUP\_ORG\_ID 保存在本机用户目录下的运行脚本：

```text
%LOCALAPPDATA%/AI_IR_REPLAY/runtime-env.ps1
```

脚本只对当前用户授予读写权限，位于仓库外。它仍是本地明文凭据，不是秘密管理服务。不要展示、提交或复制到团队材料；已有暴露风险的凭据建议由负责人轮换，本轮没有吊销、轮换或测试线上凭据。

当前 PowerShell 会话加载：

```powershell
. (Join-Path $env:LOCALAPPDATA 'AI_IR_REPLAY/runtime-env.ps1')
# 仅检查是否存在，不显示值
'KIMI_API_KEY','CODEUP_TOKEN','CODEUP_ORG_ID' | ForEach-Object {
  [pscustomobject]@{ Name = $_; Present = [bool][Environment]::GetEnvironmentVariable($_) }
}
```

生产环境建议用部署系统或凭据管理服务注入。程序不自动加载 .env 或 env.md。

## 2. 环境变量及本地配置事实

| 变量/项目                   | 用途             | 说明                                           |
| ----------------------- | -------------- | -------------------------------------------- |
| KIMI\_API\_KEY          | 模型 API 认证      | 现有 YAML 使用此变量；换兼容供应商需一并核对 endpoint/key/model |
| CODEUP\_TOKEN           | Codeup PAT     | 请求头 x-yunxiao-token；只读演示也需要读取目标仓库权限          |
| CODEUP\_ORG\_ID         | Codeup 组织 ID   | 组织信息留在运行环境，不用于公开展示                           |
| GITHUB\_TOKEN           | GitHub API 认证  | 非公开仓库读取也可能需要，dry-run 不提供访问权限                 |
| AGENT\_CONFIG\_PATH     | 默认配置路径         | CLI -config 优先                               |
| AGENT\_REVIEW\_DRY\_RUN | 强制禁止发布         | true 或 1 生效，不能阻止模型调用/状态更新                    |
| AGENT\_LOG\_LEVEL       | 日志级别覆盖         | 优先于 YAML                                     |
| AGENT\_ADMIN\_TOKEN     | 管理接口 Bearer 令牌 | 覆盖 server.admin\_token；Compose 必填，不写入文档      |
| AGENT\_SERVER\_ADDR     | 管理监听地址覆盖       | 容器设 0.0.0.0:8081；本机默认回环地址                    |

现有 configs/config-gvs.yaml 和 config-gvs-infer.yaml：模型 endpoint 为 <https://api.kimi.com/coding/v1，small/medium/large> 均配置 kimi-for-coding。这是本地配置事实，不代表已核实供应商当前模型版本、256K 上限、价格或访问资格。

Codeup endpoint：<https://openapi-rdc.aliyuncs.com。本地配置使用组织内全路径> lizheng/lzvsg/gvs 与 lizheng/lzvsg/gvs\_infer；访问权限和 API 可用性需在授权环境中验证。

## 3. 运行模式及副作用

离线 Mock：

```powershell
# 终端一
 go run ./tools/mockllm
# 终端二
 go run ./cmd/agent -config configs/config.mock.yaml -diff testdata/sample.patch -dry-run
```

真实仓库试点：先检查 YAML 的目标仓库、时间窗口、规则、独立 state\_path 和知识路径，再运行 -once -dry-run。dry-run 仍可能收费和改写本地基线/知识，不能直接复用生产基线演示。configs/config-codeup.yaml 的 dry\_run=false，不能把它当安全演示默认值。

理解模块使用本地 docs 中 SOURCE-SHA 识别快照。GVS 文档 SHA 为 c948e000，infer 为 920c7a3f；它们不表示 2026-10-13 的远端最新 HEAD。

## 4. 新增运行配置

review\.max\_llm\_calls、max\_round\_tokens、round\_timeout 默认分别为 100、1000000、20m。max\_round\_cost 默认 0；开启时须提供 llm.prices 的模型单价，统一币种。预算按字节估算预留，供应商 Usage 与实际收费另行核对。

understanding.snapshot\_repository 指向持有完整来源 commit 的本地 Git 仓库；首次无知识 SHA 时必须提供。max\_snapshot\_bytes 默认 1000000，超限失败而非静默截断。现有示例 YAML 没有配置 snapshot\_repository，已有知识可以复用，但删除或丢失知识后不可假定能自动重建。容器内须配置已挂载的 Linux 路径，宿主目录不自动可见。

每个业务仓库、试点与正式环境分别使用 document\_path/state\_path；检查 SOURCE-REPOSITORY 与 SOURCE-SHA。当前错误仓库知识复用测试失败，strict=true 不能替代来源检查。CLI 会创建对应 .lock 文件并持有操作系统锁，不要以删除锁文件方式绕过实例互斥。

团队运行说明见 [部署与验收](docs/deployment-and-acceptance.md)，本文件继续只维护本机环境信息。

## 5. 历史验证与重新核验

2026-09-22 的原记录声称：PAT 可访问 branches、changeRequests、compares、comments，仓库枚举返回 403，短路径访问失败。此信息仅作为历史排障线索，本轮未调用远端重新确认。

重新核验应分开检查：网络/endpoint、认证、组织与仓库路径、读取权限、评论写权限、模型格式。不要把某接口的 403 推导为 PAT 对所有接口有效，也不要把只读成功推导为评论写入成功。

2026-10-13 本地 go test ./... 失败：TestPrepareRejectsDifferentRepositoryKnowledge；其他有测试的包返回 ok。当前知识来源隔离不能标为已验收，真实平台与容器也未在本轮验证。

本地工具链：本审查项目要求 Go 1.23；工作区 .gotmp/go1.23.12/go/bin/go.exe 可用于当前验证。业务 GVS 标注快照中的主模块为 Go 1.25.0，应使用对应工具链验证业务代码。
