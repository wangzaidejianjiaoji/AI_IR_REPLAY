# 单 PR 续审自审记录

日期：2026-10-15。实现基线为 HEAD `4c9123e3` 加本次未提交工作区改动；不能仅凭提交号复现新增实现。需求与边界见 [prd.md](../prd.md)，运行步骤见 [部署与验收](deployment-and-acceptance.md)。

## 发现、回归与修复

| 发现 | 对应回归 | 修复/边界 |
|---|---|---|
| 旧尝试结束可能覆盖队列新版本，旧扫描闭包可能执行新清单 | `TestFinishingOldAttemptCannotCompleteNewVersion`、`TestQueuedSnapshotChangePreventsStaleClosure` | 完成时校验预期版本，领取后比对闭包版本，不用旧审核标新版本完成 |
| 到期失败任务可能反复排在从未尝试任务前 | `TestDueFailureCannotStarveUnattemptedJob`、`TestDurableQueueFairnessAndRecovery` | 优先最早 LastAttempt，结合 RetryAt、存活所有者与确定性比较 |
| 同一 Service 指定 MR 新版本被进程内去重挡住 | `TestFocusedMRNewVersionInSameService`、`TestMRTargetOnlyChangeIsReviewed` | 固定源/目标及配置身份参与去重；未知版本先重新发现 |
| 元数据变动未形成新身份，GitHub 发布快照不够固定 | `TestMRMetadataOnlyChangeIsReviewed`、`TestGitHubChangedVersionCannotPublish` | 标题/说明等依赖进入固定身份；按 SHA compare，发布前复查状态、SHA 与元数据 |
| 相同 SHA 的知识可能缺少仓库来源或含冲突来源 SHA | `TestPrepareRejectsDifferentRepositoryKnowledge`、`TestPrepareRejectsUnidentifiedVersionedDocumentEvenWhenSHAEqualsHead`、`TestPrepareRejectsConflictingSourceSHAMarkers` | 复用前校验来源；无版本知识只能经固定源码初始化，不作为可信旧快照 |
| 供应商错误可能带回 key；示例不应含明文凭据 | `TestProviderErrorsNeverExposeConfiguredAPIKey`；示例配置静态检查 | 错误脱敏，示例用环境占位符；实际凭据仅在本机迁移到既有仓库外运行配置，未打印 |
| 金额预算可能漏校验备用模型价格 | `TestCostBudgetRequiresFallbackModelPrices` | 开启金额预算时主备模型都须有价格；估算不等于真实账单 |
| 响应/冷却检查点保存失败仍可能宣告成功 | `TestCheckpointWriteFailureFailsRequest`、`TestQuotaCheckpointWriteFailureStopsFallback` | 持久化失败阻断；固定知识保存错误也向上传播，不冒充可恢复成功 |
| 切换模型破坏先前成功响应复用，响应目录隔离导致冷却不共享 | `TestResponseCheckpointSurvivesDowngradeWithActualProviderMetadata`、`TestSharedQuotaDirectoryWorksAcrossResponseCheckpoints` | 响应保留实际供应商信息；独立共享冷却目录，关闭响应缓存仍有效 |
| 大文件整体拒绝、候选重复证据挤占预算、并发顺序影响结果 | `TestPackBoundedSplitsOnlyCompleteHunks`、`TestConfirmationSharedEvidenceProtocolAndStablePrompt`、`TestAggregateUsesCompleteIssueTieBreakBeforeReflection` | 完整 hunk 分片、共享证据 ID、确定性汇总；单 hunk/候选完整证据超限仍失败 |
| 评论成功后基线失败，重启可能重复发布 | `TestPublicationReceiptSurvivesStateFailureAndRestart` | 本地发布回执抑制常见重试重复；远端成功到回执保存不原子，仍需人工核对故障窗口 |

## 验证记录与未验收项

- packager/coordinator 局部测试及 vet 已通过；实现过程中针对完整 hunk、共享证据、排序和编码预算观察到预期失败后修复。
- 最终工作区执行 `go test -count=1 ./...` 和 `go vet ./...`，均退出 0；PowerShell 与 Git Bash 脚本兼容测试通过。Windows 与 Linux 候选二进制构建成功，未覆盖运行版本。
- Windows 原生测试、Linux 交叉编译及 Git Bash 脚本路线分别记录；没有原生 Linux 运行结论。race 所需 cgo 工具链不可用，不宣称 race 通过。
- 未调用真实模型或发布真实评论，未停止用户真实审核进程、未覆盖其运行二进制。切换新实现须先停全部旧实例、构建新二进制再启动。
- 共享运行锁只在同机同目录成立；版本复查与评论、本地回执与远端成功均非事务。真实平台契约、配额恢复时间、费用和缺陷检出率仍待独立验收。
