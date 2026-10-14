<!-- SOURCE-REPOSITORY: lizheng/lzvsg/gvs -->
<!-- SOURCE-SHA: c948e000fc0865c25f408cd1d5cb67802fea39e0 -->

# GVS 代码理解：AI 审查使用的版本化上下文

> 文档维护：2026-10-13。源码依据为顶部 SHA 的本地 Git 对象。没有访问远端核验最新 main，本文不得称为当前线上全量理解。

本轮纠正旧文档中与标注 SHA 不符的模块路径、Go 版本、推理通信链路和 QA 工具描述。业务源码未修改。详细设计必须继续回指本版本文件/符号，不能用文档替代源码。

## 0. 维护约定

以 L0 系统关系、L1 模块职责、L2 调用链、L3 契约组织。SOURCE-SHA 是知识版本，不是文档维护日期。源码版本未变化时不得仅因改文档更新 SHA。

[AI 审查项目说明](../GO_AI_PR_Review.md) 和 [技术框架](../技术框架层级说明.md) 解释审查系统；本文件只提供业务上下文。审查程序在理解模块开启时使用本文参与前置分析，专业角色接收变更摘要，不会自动读完整目标仓库。

## 1. L0 系统上下文

GVS 是视觉业务主服务，负责相机接入、扫描/检测/跟踪编排、云台控制、HTTP/MQTT、存储与告警。业务主 Go module 位于 **gvs/go.mod**，声明 Go **1.25.0**。这与 AI_IR_REPLAY 的 Go 1.23 不同。

本仓库是多构建单元布局：gvs、qatool、pidservice、starter、hikproxy 各有入口或 go.mod，不能把仓库根目录等同于业务 module 根目录。

| 关系 | 版本化事实与来源 |
|---|---|
| GVS 与 infer | gvs/communication/grpc.go 的 InitInferClient 直接连接 127.0.0.1:50051，使用 insecure gRPC |
| 帧数据 | gvs/dnn/yolov5/yolov5.go 创建 24 MiB mmap，路径为 /tmp/dnn<model_name>_index_<i>_shared_image.dat，索引 0..3 |
| 进程互斥 | gvs/main.go 使用 single.New("gvs_gvs")、CheckLock 和 TryUnlock |
| 业务进程资源 | 全局 context、相机、模型封装、状态机、数据收集器等，需要显式退出与释放 |
| qatool | 独立目录/进程，检查设备、preflight、网络扫描与 bench；不是主服务内部调用层 |

旧文档关于“GVS 经 9979 net/rpc 桥调用 infer”的主链路描述不适用于此 SHA。**qatool 的 -bench_port 在此快照仍默认 9979，不能写为 50051。** QA 与主服务链路需要分别核对。

## 2. L1 架构分层与模块目录

所有下列路径相对目标仓库根目录，业务包以 gvs/ 开头。

| 层 | 关键路径/符号 | 职责 |
|---|---|---|
| 进程壳 | gvs/main.go、gvs/app.go | main、initApp、LoadModel、closeAll；启动、信号与资源收尾 |
| 相机接入 | gvs/cam、gvs/wide | Cam 接口、厂商 SDK、WideCamClient、多宽相机与窄相机 |
| 业务编排 | gvs/statemachine、gvs/gvflow、gvs/gvscheduler | 状态转换、数据流图和任务调度 |
| 推理控制 | gvs/infercontrol、gvs/communication | InferClient、相机同步、OSD 与 WorkMode 双向流 |
| 算法封装 | gvs/dnn、gvs/track、gvs/grdetect | 模型客户端、检测/跟踪、GRDetectorMgr/ComboTrackCamera |
| 外部接口 | gvs/gvhttp、gvs/msg、MQTT 相关入口 | HTTP handler、业务消息与模式控制 |
| 数据及基础设施 | gvs/datastorage、gvs/datacollector、gvs/gvutils | 落盘、收集、配置、日志和退出辅助 |
| 配套进程 | qatool、pidservice、starter、hikproxy | QA、PID、启动器、厂商代理，各自按构建单元验证 |

此 SHA 使用 gWideCamClis 与 wideCams()、wideCamAt() 管理宽相机，不能沿用旧版 gWcams 数组的名字推断当前调用方。跟踪状态细节以本版本 gvs/track、相关 PID 实现为准，不套用其他版本的文档。

## 3. L2 启动与推理链路

### 3.1 启动与退出

main 在 gvs/main.go；initApp 在 gvs/app.go。main 建立单实例锁和 panic 退出兜底，后续配置、业务初始化与服务循环按源码分支运行。

initApp 的已核对顺序：读取配置与初始化检查；设置扫描回调；启动 filterregions；TestMode 分支只连接 infer、LoadModel、LoadTrackingModels 后返回。正常分支启动 MQTT 处理，清理旧 mmap，初始化相机环境、构建并登录相机，再设置 gRPC 状态回调并 InitInferClient，初始化 OSDManager 与 WorkModeManager。

initApp 的 mark() 会检查 gCtx 取消，以 ErrInitCanceled 区分取消与错误。相机错误累计后可触发 CheckRebootCameras 并 panic。ensureFfmpegCopied 在此段已注释，不能称为正常启动必经步骤。

closeAll 的 TestMode 分支关闭测试与模型后返回，正常分支处理相机登出等资源。审查应沿各模式分别核对生命周期，不假定所有退出都会执行相同清理。

### 3.2 推理与流控制

~~~mermaid
flowchart LR
  A[GVS 业务入口] --> B[dnn / track / infercontrol]
  B --> C[communication.InferClient]
  C --> D[gRPC 127.0.0.1:50051]
  D --> E[gvs_infer 模型与流管道]
  B --> F[共享内存图像]
  F --> E
  G[OSDManager / WorkModeManager] --> C
~~~

gvs/communication/grpc.go 设置 50 MiB 消息限制、keepalive 和连接退避；连接状态监视使用后台 context。在 TransientFailure 或 Shutdown 时代码会 panic，不能描述为透明恢复。Ready 回调会触发相机重同步，但必须结合进程退出行为评估可用性。

### 3.3 QA 与主服务区别

qatool/qatool.go 定义 -bench、-bench_port、-preflight、-scan、RTK 等参数；此 SHA 的 bench_port 默认 9979。不能把 QA 性能数字与主服务/infer 延迟直接对比，需先明确协议、输入、warmup、循环数和同步边界。

此 SHA 中没有旧文档引用的 qatool/ARCHITECTURE.md、release.json、Windows stub 文件或 v4.1.0 证据。它们不能作为本快照已经具备的能力。

## 4. L3 与 infer 的跨仓库契约

GVS 协议源位于 **gvs/communication/proto/infer.proto**，infer 协议源位于 **proto/infer.proto**。两仓库分别提交生成代码，任何变更都应同时比较两端源定义和生成代码，不能说已存在单一共享事实源。

接口包含 11 个 RPC：Connect、New、Detect、Close、GetMeta、InitTrack、SyncCameraList、TrackStream、StopTrack、OSDStream、WorkModeStream。协议数量与名字应由源 proto 清点，避免漏掉 GetMeta。

对照 GVS c948e000 与 infer 920c7a3f，已验证漂移：

- infer DetectRequest 有 detect_with_track field 11，GVS 侧无此字段。
- infer DetectResponse 有 error_message field 2，GVS 侧只有 results field 1。
- GVS TrackStreamResult 为 oneof：detect_result=1、file_saved=2；infer 侧只声明 detect_result=1。

这些是版本对照事实。新增字段未必导致连接失败，客户端忽略未识别字段仍可能丢失业务错误或事件；需验证实际调用方和滚动升级顺序，不能仅凭字段差异断言线上事故。

其他审查约束：model_name/sub_name 必须两端一致，权重路径和 TensorRT engine 应匹配部署版本；模型不存在、业务检测为空、transport 错误要区分。infer Close 在 920c7a3f 为记录日志并返回 OK 的 no-op，客户端不能据此认定服务端模型/显存已经释放。

## 5. AI 审查应关注的风险与覆盖边界

- 相机登录/登出、mmap、CGO/SDK 句柄必须按成功、失败、取消和重载路径核对。
- gCtx 取消应传递到后台任务；gRPC 连接监视中的 panic 与长流退出是重点。
- 共享帧、相机列表、状态机和回调的锁范围与生命周期影响并发安全。
- proto、OSD/WorkMode 消息与模型名称变化需要跨仓库评审。
- 主模块 Go 1.25.0 与 QA/配套模块的工具链应分别检查。
- 本项目路径分配能匹配 gvs/dnn 等 GVS 路径；但纳入文件不等于未截断，也不代表所有专项角色参与。

本文提供有来源的业务背景，不给业务缺陷数、覆盖率或性能改善承诺。未核对的历史表述已移除，后续增量更新必须附变更 SHA 和实际源码依据。


## 6. 知识集成与版本维护

本次维护仅同步 AI_IR_REPLAY 工作区的知识管理行为；未重新核验 GVS 业务源码，不改变顶部 SOURCE-SHA。专业角色读取变更分析摘要，不能假定每个角色都读取了本文全文。

首次知识初始化需要 snapshot_repository 中与来源 HEAD 完全匹配的 Git commit，对选中的源码/协议/配置/构建文本附内容哈希；这不是业务全仓运行验收。更新失败时 strict=false 可使用旧版本快照，但来源仓库隔离测试目前失败。GVS 与 infer 必须使用不同 document_path，并检查 SOURCE-REPOSITORY，strict=true 本身不能消除该缺陷。

设计边界见 [关键设计](review-design.md)，操作与当前测试结果见 [部署与验收](deployment-and-acceptance.md)。跨仓库结论继续同时标注两端 SHA；文档维护日期不代表源码或部署升级。
