<!-- SOURCE-REPOSITORY: lizheng/lzvsg/gvs -->
<!-- SOURCE-SHA: 5cc010e68729a4a9d81759f85100923be076188a -->

<!-- SOURCE-REPOSITORY: lizheng/lzvsg/gvs -->
<!-- SOURCE-SHA: c948e000fc0865c25f408cd1d5cb67802fea39e0 + 未回填的后续增量 -->
<!-- INCREMENT: appimage 配方拆分（删除 appimage_gvs.yml，新增 appimage_gvs_x86_64.yml / appimage_gvs_aarch64.yml，build_x86.sh / build_arm.sh 引用切换）+ gvs/cam/hope/hope.go 登录超时参数 3 → 3000 并补充错误日志 -->
<!-- INCREMENT-SHA: 待回填 —— 输入未提供本次增量的提交标识，引用本节结论时必须先核对目标仓库实际提交 -->

# GVS 代码理解：AI 审查使用的版本化上下文

> 文档维护：首轮基线 2026-10-13（SHA c948e000），本轮为增量更新（日期与提交 SHA 以回填为准）。基线轮依据顶部 SHA 的本地 Git 对象；本轮在基线之上叠加增量 diff，未重新全仓核验。本文不得称为当前线上全量理解。

本轮变更范围：构建打包层（AppImage 配方由单一文件拆分为按架构拆分，构建脚本引用切换）与 hope 相机登录链路（HVS_Connect 超时参数 3 改为 3000，失败路径新增错误日志）。业务编排、推理通信、跨仓库 proto 契约未在增量中出现变化，基线理解继续有效。

## 0. 维护约定

以 L0 系统关系、L1 模块职责、L2 调用链、L3 契约组织。SOURCE-SHA 是知识版本，不是文档维护日期。源码版本未变化时不得仅因改文档更新 SHA。增量更新必须附变更 SHA 与实际源码依据；本轮增量 SHA 未随输入提供，引用本文涉及时标注为“待回填 SHA 的增量”。

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
| 打包分发 | AppImage 按架构拆分配方：appimage_gvs_x86_64.yml 与 appimage_gvs_aarch64.yml（本轮增量），分别由 build_x86.sh 与 build_arm.sh 调用；旧单一配方 appimage_gvs.yml 已删除 |

旧文档关于“GVS 经 9979 net/rpc 桥调用 infer”的主链路描述不适用于此 SHA。**qatool 的 -bench_port 在此快照仍默认 9979，不能写为 50051。** QA 与主服务链路需要分别核对。

## 2. L1 架构分层与模块目录

所有下列路径相对目标仓库根目录，业务包以 gvs/ 开头。

| 层 | 关键路径/符号 | 职责 |
|---|---|---|
| 进程壳 | gvs/main.go、gvs/app.go | main、initApp、LoadModel、closeAll；启动、信号与资源收尾 |
| 相机接入 | gvs/cam、gvs/wide | Cam 接口、厂商 SDK、WideCamClient、多宽相机与窄相机；hope 厂商实现位于 gvs/cam/hope/hope.go |
| 业务编排 | gvs/statemachine、gvs/gvflow、gvs/gvscheduler | 状态转换、数据流图和任务调度 |
| 推理控制 | gvs/infercontrol、gvs/communication | InferClient、相机同步、OSD 与 WorkMode 双向流 |
| 算法封装 | gvs/dnn、gvs/track、gvs/grdetect | 模型客户端、检测/跟踪、GRDetectorMgr/ComboTrackCamera |
| 外部接口 | gvs/gvhttp、gvs/msg、MQTT 相关入口 | HTTP handler、业务消息与模式控制 |
| 数据及基础设施 | gvs/datastorage、gvs/datacollector、gvs/gvutils | 落盘、收集、配置、日志和退出辅助 |
| 配套进程 | qatool、pidservice、starter、hikproxy | QA、PID、启动器、厂商代理，各自按构建单元验证 |
| 构建打包 | build_x86.sh、build_arm.sh、appimage_gvs_x86_64.yml、appimage_gvs_aarch64.yml | 架构专用 AppImage 配方与构建入口（本轮增量后） |

此 SHA 使用 gWideCamClis 与 wideCams()、wideCamAt() 管理宽相机，不能沿用旧版 gWcams 数组的名字推断当前调用方。跟踪状态细节以本版本 gvs/track、相关 PID 实现为准，不套用其他版本的文档。

## 3. L2 启动与推理链路

### 3.1 启动与退出

main 在 gvs/main.go；initApp 在 gvs/app.go。main 建立单实例锁和 panic 退出兜底，后续配置、业务初始化与服务循环按源码分支运行。

initApp 的已核对顺序：读取配置与初始化检查；设置扫描回调；启动 filterregions；TestMode 分支只连接 infer、LoadModel、LoadTrackingModels 后返回。正常分支启动 MQTT 处理，清理旧 mmap，初始化相机环境、构建并登录相机，再设置 gRPC 状态回调并 InitInferClient，初始化 OSDManager 与 WorkModeManager。

initApp 的 mark() 会检查 gCtx 取消，以 ErrInitCanceled 区分取消与错误。相机错误累计后可触发 CheckRebootCameras 并 panic。ensureFfmpegCopied 在此段已注释，不能称为正常启动必经步骤；但增量后的 AppImage 配方会把预置的 ffmpeg 可执行文件直接打包进镜像（AppDir/usr/local/bin/ffmpeg，且 chmod -x 去除执行位，运行期按需取用），打包产物与运行期复制行为需要区分描述，不能因 ensureFfmpegCopied 被注释就断言镜像内没有 ffmpeg。

closeAll 的 TestMode 分支关闭测试与模型后返回，正常分支处理相机登出等资源。审查应沿各模式分别核对生命周期，不假定所有退出都会执行相同清理。

### 3.2 推理与流控制

调用链结构（无 mermaid 渲染时用下表核对）：

| 步骤 | 节点 |
|---|---|
| 1 | GVS 业务入口（gvhttp / MQTT / statemachine 驱动） |
| 2 | dnn / track / infercontrol |
| 3 | communication.InferClient（gRPC，127.0.0.1:50051，insecure） |
| 4 | gvs_infer 模型与流管道 |
| 5 | 并行数据面：dnn 建立的共享内存图像 mmap 被 infer 侧消费 |
| 6 | OSDManager / WorkModeManager 双向流复用同一 gRPC 连接 |

gvs/communication/grpc.go 设置 50 MiB 消息限制、keepalive 和连接退避；连接状态监视使用后台 context。在 TransientFailure 或 Shutdown 时代码会 panic，不能描述为透明恢复。Ready 回调会触发相机重同步，但必须结合进程退出行为评估可用性。

### 3.3 QA 与主服务区别

qatool/qatool.go 定义 -bench、-bench_port、-preflight、-scan、RTK 等参数；此 SHA 的 bench_port 默认 9979。不能把 QA 性能数字与主服务/infer 延迟直接对比，需先明确协议、输入、warmup、循环数和同步边界。

此 SHA 中没有旧文档引用的 qatool/ARCHITECTURE.md、Windows stub 文件或 v4.1.0 证据，它们不能作为本快照已经具备的能力。关于 release.json 的表述需要按增量修正：基线轮曾记为“没有 release.json 证据”，但增量中的两个 AppImage 配方均包含复制步骤（gvs/release.json → AppDir/release.json），说明打包流程把 gvs/release.json 视为存在且必需的输入文件；该文件是否存在、内容由目标仓库当前状态决定，配方本身不生成它。若该文件缺失，appimage-builder 的 script 阶段会因 cp -f 失败而中断，属于打包期显式失败而非运行期缺失。

## 3.4 打包与分发链路（本轮增量新增）

构建入口与配方对应关系：

| 构建脚本 | 目标架构 | 配方文件 | 关键差异 |
|---|---|---|---|
| build_x86.sh 的 build_gvs_x86_64 | x86_64 | appimage_gvs_x86_64.yml | OpenCV 4.7.0（OPENCV_PATH 指向 opencv-4.7.0-x86_64）、附带 OPENBLAS 静态库路径、libtbb、libopenblas.so.0；不包含 hikproxy |
| build_arm.sh 的 build_gvs_aarch64 | aarch64 | appimage_gvs_aarch64.yml | OpenCV 4.13.0 CUDA 12.6（OPENCV_PATH 指向 opencv-4.13.0-cuda12.6-aarch64）、复制更完整的 OpenCV 共享库集合（含 imgproc/core/highgui/videoio/video/objdetect/calib3d/imgcodecs/features2d/dnn/photo/flann/cudaimgproc/cudaarithm/cudafilters）、包含 hikProxy 二进制 |

两个配方的共同结构：准备 AppDir 目录树（icons、bin、lib、weights、etc/gvs）、复制 gvs 二进制与 starter（打包为 gvs.app 入口）、复制 weights/*.json、复制 cam/hik 与 cam/hope 对应架构的 SDK 库、复制 release.json、复制对应架构 ffmpeg 二进制及库并 chmod -x、复制 trackerengine 配置 yml 到 usr/local/etc/gvs。

关键不变量与易错点：

- **runtime.env 不做构建期变量替换**。配方注释明确说明 runtime.env 段只原样写入 AppRun.env，${APPDIR} 等在运行期由 AppRun 展开，而 ${TARGET_ARCH} 之类构建期变量在 env 段中不能依赖替换。增量后两个配方的 env 段全部写死架构（x86_64-linux-gnu / aarch64-linux-gnu），这是对旧单一配方在 env 段使用 ${TARGET_ARCH} 的缺陷修正。新增第三个架构时必须新建配方，不能在 env 段复用旧写法。
- **CUDA 库路径按架构不同**：x86_64 配方写死 /usr/local/cuda/lib64，aarch64 配方写死 /usr/local/cuda-12.6/lib64。与 OPENCV_PATH 指向的 CUDA 版本（aarch64 侧 4.13.0-cuda12.6）应对应；x86_64 侧 cuda 路径未带版本号，依赖部署环境的符号链接约定。
- **AppImage 入口是 starter**：app_info.exec 为 usr/local/bin/gvs.app（即 starter.amd64 / starter.arm64），gvs 本体由 starter 拉起；starter 与 gvs 的退出信号传递、崩溃重启策略仍是进程壳审查点。
- **ffmpeg 以非执行位打包**：chmod -x 表示运行期按库/资源方式加载或由代码显式调用解释器执行，与 ensureFfmpegCopied 被注释相互印证：镜像自带 ffmpeg，运行期不再复制。
- **旧配方删除后引用必须同步**：build_x86.sh 与 build_arm.sh 已分别切换到新配方；仓库内任何残留对 appimage_gvs.yml 的引用（CI、文档、其他脚本）都会直接失败，审查变更时应全局搜索旧文件名。
- aarch64 配方比 x86_64 多打包 hikProxy，说明 hikproxy 组件目前仅随 ARM 产物分发；x86_64 部署若依赖 hikproxy 属于能力缺口而非疏漏证据，需按部署目标核对。

## 3.5 hope 相机登录链路（本轮增量）

gvs/cam/hope/hope.go 的 Login() 使用 driver.call(ctx, ...) 包一层带超时/取消的 ctx。增量把 HVS_Connect 的最后一个参数由 C.int(3) 改为 C.int(3000)，并新增行内注释“3000ms”；失败分支新增一行 h.logf，输出 ret 与 h.HopeGetlastErr()，随后照旧返回 fmt.Errorf。

需要按增量修正或强化的理解：

- 旧代码 C.int(3) 与“3000ms”语义不匹配的风险已消除：当前参数显式为 3000，按毫秒解释，与注释一致。若 SDK 该参数实际单位不是毫秒，则 3 → 3000 的放大是行为变更而非纯修复，审查时应以 hope SDK 头文件/文档核实单位，不能仅凭注释断言。
- 登录超时从（疑似）3 个时间单位放大到 3000ms，意味着弱网或高负载相机场景下 Login 等待窗口显著变长；调用方 ctx 的超时设置若短于 3000ms，实际生效的是外层 ctx 而非 SDK 参数，两层超时的 min 关系需要核对 driver.call 的实现。
- 失败路径新增错误日志不改变控制流，仍返回 error；但注意失败日志与返回错误都包含 ret 与 err_code，信息冗余一致，不存在日志与错误不一致的新风险。
- Login 成功后原有 HVS_SetAutoReconnect(userid, 1) 行为未变，自动重连开启仍是 hope 相机断线恢复的依赖点。

## 4. L3 与 infer 的跨仓库契约

GVS 协议源位于 **gvs/communication/proto/infer.proto**，infer 协议源位于 **proto/infer.proto**。两仓库分别提交生成代码，任何变更都应同时比较两端源定义和生成代码，不能说已存在单一共享事实源。

接口包含 11 个 RPC：Connect、New、Detect、Close、GetMeta、InitTrack、SyncCameraList、TrackStream、StopTrack、OSDStream、WorkModeStream。协议数量与名字应由源 proto 清点，避免漏掉 GetMeta。

对照 GVS c948e000 与 infer 920c7a3f，已验证漂移：

- infer DetectRequest 有 detect_with_track field 11，GVS 侧无此字段。
- infer DetectResponse 有 error_message field 2，GVS 侧只有 results field 1。
- GVS TrackStreamResult 为 oneof：detect_result=1、file_saved=2；infer 侧只声明 detect_result=1。

这些是版本对照事实。新增字段未必导致连接失败，客户端忽略未识别字段仍可能丢失业务错误或事件；需验证实际调用方和滚动升级顺序，不能仅凭字段差异断言线上事故。

本轮增量不涉及 proto 或通信代码，上述漂移结论维持不变。

其他审查约束：model_name/sub_name 必须两端一致，权重路径和 TensorRT engine 应匹配部署版本；模型不存在、业务检测为空、transport 错误要区分。infer Close 在 920c7a3f 为记录日志并返回 OK 的 no-op，客户端不能据此认定服务端模型/显存已经释放。

## 5. AI 审查应关注的风险与覆盖边界

- 相机登录/登出、mmap、CGO/SDK 句柄必须按成功、失败、取消和重载路径核对；hope Login 在增量后超时窗口变大，需核对 driver.call 外层超时与 SDK 3000ms 参数的生效关系。
- gCtx 取消应传递到后台任务；gRPC 连接监视中的 panic 与长流退出是重点。
- 共享帧、相机列表、状态机和回调的锁范围与生命周期影响并发安全。
- proto、OSD/WorkMode 消息与模型名称变化需要跨仓库评审。
- 主模块 Go 1.25.0 与 QA/配套模块的工具链应分别检查。
- 打包变更：全局搜索 appimage_gvs.yml 残留引用；核对两个新配方的 env 写死路径与目标部署环境一致（cuda vs cuda-12.6）；核对 gvs/release.json 在打包输入中真实存在；x86_64 产物不含 hikproxy 是否与部署预期一致。
- 本项目路径分配能匹配 gvs/dnn 等 GVS 路径；但纳入文件不等于未截断，也不代表所有专项角色参与。

本文提供有来源的业务背景，不给业务缺陷数、覆盖率或性能改善承诺。未核对的历史表述已移除，后续增量更新必须附变更 SHA 和实际源码依据。

## 6. 知识集成与版本维护

基线轮仅同步 AI_IR_REPLAY 工作区的知识管理行为；本轮增量叠加打包与 hope 登录两处代码变更。专业角色读取变更分析摘要，不能假定每个角色都读取了本文全文。

首次知识初始化需要 snapshot_repository 中与来源 HEAD 完全匹配的 Git commit，对选中的源码/协议/配置/构建文本附内容哈希；这不是业务全仓运行验收。更新失败时 strict=false 可使用旧版本快照，但来源仓库隔离测试目前失败。GVS 与 infer 必须使用不同 document_path，并检查 SOURCE-REPOSITORY，strict=true 本身不能消除该缺陷。

设计边界见 [关键设计](review-design.md)，操作与当前测试结果见 [部署与验收](deployment-and-acceptance.md)。跨仓库结论继续同时标注两端 SHA；文档维护日期不代表源码或部署升级。

本轮增量的待办：回填增量提交 SHA；核验 HVS_Connect 第 6 参数单位（毫秒假设的佐证）；全局确认旧配方文件名无残留引用；确认 gvs/release.json 在打包流水线中的生成责任方。