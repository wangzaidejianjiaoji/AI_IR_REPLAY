<!-- SOURCE-REPOSITORY: lizheng/lzvsg/gvs -->
<!-- SOURCE-SHA: b5930f579c8147f651cf62f5f98a655e66c26bdd -->

<!-- SOURCE-SHA: c948e000fc0865c25f408cd1d5cb67802fea39e0 + 未回填的后续增量 -->
<!-- INCREMENT: appimage 配方拆分（删除 appimage_gvs.yml，新增 appimage_gvs_x86_64.yml / appimage_gvs_aarch64.yml，build_x86.sh / build_arm.sh 引用切换）+ gvs/cam/hope/hope.go 登录超时参数 3 → 3000 并补充错误日志 + 海康相机控制协议可配置化（ConfigParams.ControlProtocol，camfactory 按 sdk/isapi/auto 分流，新增 gvs/cam/hik/isapi HTTP digest 客户端包）与 Cam 接口新增 SetWiper/SetFan（BaseCam 空实现） -->
<!-- INCREMENT-SHA: 待回填 —— 输入未提供本次增量的提交标识，引用本节结论时必须先核对目标仓库实际提交 -->

# GVS 代码理解：AI 审查使用的版本化上下文

> 文档维护：首轮基线 2026-10-13（SHA c948e000），本轮为增量更新（日期与提交 SHA 以回填为准）。基线轮依据顶部 SHA 的本地 Git 对象；本轮在基线之上叠加增量 diff，未重新全仓核验。本文不得称为当前线上全量理解。

本轮变更范围：构建打包层（AppImage 配方由单一文件拆分为按架构拆分，构建脚本引用切换）、hope 相机登录链路（HVS_Connect 超时参数 3 改为 3000，失败路径新增错误日志）、海康相机控制面（ControlProtocol 配置化、新增 gvs/cam/hik/isapi 包、Cam 接口扩展 SetWiper/SetFan）；后续批次叠加 huanyu 侧 SetZoomSpeed/SetWiper/SetFan 生效实现、gvs/cam/viewzoom.go 视场插值工具、omni OSD 速度倍率标注，以及 proto 生成代码再生成（新增 TrackBgClassifierResult，DetectResult 增加 field 11）。

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
| 相机接入 | gvs/cam、gvs/wide | Cam 接口、厂商 SDK、WideCamClient、多宽相机与窄相机；hope 厂商实现位于 gvs/cam/hope/hope.go；hik 支持按 ControlProtocol 在 SDK 与 ISAPI（gvs/cam/hik/isapi）两条控制面间分流 |
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

- infer DetectRequest 有 detect_with_track field 11；本批 GVS DetectRequest 新增 time_stamp field 11（非 detect_with_track），同号不同名/语义，跨仓库需重点核对。
- infer DetectResponse 有 error_message field 2，GVS 侧只有 results field 1。
- GVS TrackStreamResult 为 oneof：detect_result=1、file_saved=2；infer 侧只声明 detect_result=1。

这些是版本对照事实。新增字段未必导致连接失败，客户端忽略未识别字段仍可能丢失业务错误或事件；需验证实际调用方和滚动升级顺序，不能仅凭字段差异断言线上事故。

上述漂移对照基于 GVS c948e000 快照；本批增量中 gvs/communication/proto/infer.pb.go 已再生成（新增 TrackBgClassifierResult 消息，DetectResult 新增 field 11 track_bg_classifier），GVS 侧 DetectResult 的字段构成已变化，与 infer 侧的字段级对照需按新快照重新核验；gvs/communication/osdstream.go 的 omni OSD 文本格式同步变更（见 3.6 续二）。

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

## 3.6 海康相机控制协议与 ISAPI 客户端（本轮增量）

- cam.ConfigParams 新增 ControlProtocol 字段；cam 包定义 ControlProtocol 字符串类型，常量 Auto（空串）/ SDK（"sdk"）/ ISAPI（"isapi"），并提供 UsesISAPI() 判定。
- gvs/app.go 的 buildCameras 为三处相机构造（窄相机、反向窄相机、宽相机）统一填充 ControlProtocol：由 cameraControlProtocol(info) 对配置字符串 TrimSpace + ToLower 后匹配 sdk/isapi，未识别值回退 Auto。即协议选择来自配置项，构造期一次性确定。
- camfactory.NewCam 的 hik 厂商分支内优先判断 config.ControlProtocol.UsesISAPI()，再按 config.Reverse 选择 hik.NewISAPICam / hik.NewReverseISAPICam；否则维持原 SDK 封装路径。RTSP 流地址构造在分流之前，ISAPI 只替换控制面、不换取流链路。Auto 及未识别值走原 SDK 行为，与增量前兼容。
- 新增 gvs/cam/hik/isapi 包：HTTP + digest 认证（依赖 github.com/icholy/digest，自定义 findDigestChallenge 要求 realm/nonce 非空，否则报 errInvalidDigestChallenge）。Client 默认端口 80，http.Client 总超时 5s；doRaw 用 context.WithTimeout(ctx, c.http.Timeout) 再包一层，认证恢复重试仍在该总窗口内。响应体读取限 1 MiB。
- 通道默认值：PTZ=1、Focus=2、Patrol=1、Shutter=2、FFC=1，为 0 时回填默认；最终任一通道路径 ≤0 或 Host 为空则构造失败。
- doRaw 对 GET、无 body、因 errInvalidDigestChallenge 失败且 ctx 未到期时，记录 Warn 并重试一次（依赖 digest 传输层丢弃失效缓存的副作用）；请求失败与非 2xx 统一记录 [HIK][ISAPI] 错误日志并返回 httpStatusError（含 method/path/status/body）。
- request 走 XML（Content-Type application/xml），解析 ResponseStatus，StatusCode 非 0/1 判失败；requestJSON 走表单编码 + JSON accept。
- device_control.go 已补全：PresetExists（404 或 body 含 presetNotExist/notExist 视为预设不存在）、AbsolutePosition（汇总 elevation/azimuth/zoom/focus/focalLen/速度字段，缺失字段按零值处理）、SetElevation（先 AbsolutePosition 读回再改 Elevation 写回，Elevation/Azimuth 格式 %.3f）、ElevationReached（|当前值−目标| ≤ ElevationTolerance=0.05 判定到位）、SetAbsolutePosition；preset 系列 SetPreset/DeletePreset/GotoPreset 均要求 presetID>0（客户端校验，不发起请求），GotoPresetPosition 在 GotoPreset 后等待 presetSettleTime=3s 再读回 AbsolutePosition（ctx 取消优先返回）；SetPatrol 要求 patrolID>0、points 非空且各点 PresetID/DelayLaps>0；ManualShutterCorrect 走 Image 通道；SetAutomaticFFC 要求补偿分钟>0。通道默认值经 patrolChannel/shutterChannel/ffcChannel 兜底，分别回退 1/2/1，与构造期回填一致。absolutePositionSpeed 对越界速度（<0.3 或 >160）回退 absoluteSpeed="100.00"。
- 新增 ptz.go：Stop 即 ContinuousMove(0,0)；ContinuousMove/ContinuousMoveWithZoom 的 pan/tilt/zoom 限 [-100,100]，越界在客户端直接报错；ContinuousZoom 复用 zoom 轴；ContinuousFocus 走 /ISAPI/System/Video/inputs/channels/{id}/focus，focus 通道缺省 2。
- 新增 sdk_compat.go：以无会话 digest 模型对齐旧 SDK 调用形态——Login 实为 GetDeviceInfo（凭据校验并取 model/SN，替代 NET_DVR_Login 的登录信息），Logout 为 no-op（ISAPI 无设备会话需释放）；Capture 替代 NET_DVR_CaptureJPEGPicture_NEW，默认主码流 trackStreamID=101，返回 image/jpeg 字节，显式传入流 ID 时须 >0 才生效；Reboot、Position3D（坐标 0..255，替代 NET_DVR_PTZSelZoomIn_EX）、FirmwareVersion（版本拼接 build 日期）。后续批次已核对到 GetFocusConfig 之后的完整段落：focus 配置族（GetFocusConfig/SetFocusConfig/FocusPos/SetFocusPos/SetFocusMode，Style 非空校验，mode 0/1/2 映射 AUTO/MANUAL/SEMIAUTOMATIC，Position 负值拒绝）；SetExposure（auto/manual 写 exposure，manual 时附带 AutoIrisLevel=aperture，shutter 单独 PUT shutterLevel，注释明确不做单位换算）；GetStream/SetStream（帧率按 ×100 存取，SetStream 用正则对原 XML 做 read-modify-write 以保留设备特有字段，空值/0 字段跳过替换，目标 tag 缺失即报错）；GetXML/PutXML/patchXMLTags 通用 XML 通道（路径必须 /ISAPI/ 前缀，body 非空校验）；ShowDateTimeOSD（dateTimeOverlay 的 enabled 补丁）；SwitchDayNight（ircutFilter 的 IrcutFilterType=day/night，注释明确不动激光照明）；GetPTZCapabilities、GetZoomLimit/SetZoomLimit（ZoomLimitRatio 正数校验）；GetMainStream/SetMainStream（101）与 GetThirdStream/SetThirdStream（103）包装。XML 结构体集中在新增 types.go（responseStatus、ptzData、ptzAbsoluteEx/AbsoluteMove、preset/patrol 等）。SetPTZOSD、SetImageFlip、SetZoomSpeed 目前为校验后即 return nil 的空实现（不发任何请求）；SetZoomFocusPos 同样直接 return nil。雨刷/风扇的 hope 真实实现与 ISAPIReverseCam 见 3.6 续；ISAPI 路径的 SetWiper/SetFan 由哪个方法承载仍无证据。
- 新增 gvs/cam/hik/isapi_camera.go：ISAPICam 内嵌 HikComm 并以 var _ cam.Cam = (*ISAPICam)(nil) 声明满足 Cam 契约，注释明确 RTSP 仍为视频传输、仅设备操作换为 HTTP ISAPI；NewISAPICam 用 login.IP/80 及 Channels{PTZ:1,Focus:1} 建 isapi.Client，config.Preset.Z 为 0 时回填 2，构造失败返回 nil。Login 走 api.Login(context.Background())，model 空时回退 DeviceType，设备 FirmwareVersion 非空则覆盖 config.FirmwareVersion；Logout 仅委托 api.Logout。本批次证据补全其云台/对焦/OSD/流控制面：Cruise 经 panTiltSpeed/zoomSpeed 把 PanTiltDir/ZoomDir + CamSpeed 换算为带符号速度（非法方向报错），转换结果打 Info 日志；ZoomDir 对 s==-1 直接 no-op，成功后记录 lastZoomDir。timedFocus 的停止请求在 ctx 已取消后仍用 context.Background() 强制发出（注释明确），随后 500ms settle，ctx 取消时返回 ctx.Err()。FocusMode 把 Style 的 AUTO/MANUAL/SEMIAUTOMATIC 映射 0/1/2，失败返回 -1；Focus/FocusNear/FocusFar 为 near→far 定时序列并打阶段耗时日志。CruiseDirWithZoom 在到达变焦边界（isZoomAlreadyArrived：ZoomIn 且 ≥MaxZoom 或 ZoomOut 且 ≤MinZoom）时置 zoom=0；非 ZoomStop 或距上次对焦 >300ms 时按当前 Z 查 focus 表自动 SetFocusPos，未命中仅 Warn 不报错。setResolution 经 resolutionSize 白名单（仅码 19/20/27/64/67/70，其余报错）写主码流宽高。GetGisInfo 按 isGISSupportedModel 白名单（仅 iDS-2DF8C845I5XS-AFW/SP、DS-2DF8C845I5XS-DFW/VR/SP、DS-2DF8C845I5XS-DFW/VR/S 三个型号）启用，10 次×1s 重试解析 XML 中 azimuth/lat/lon（兼容 latitude/longitude 别名）。GetThirdStreamUrl 固定 rtsp://user:pass@ip:554/Streaming/Channels/103。
- InitAsNightMode 为 5 次、间隔 30s 的 RetryCtx 阶段化初始化（switch_day_night→show_osd→set_camera_param→set_resolution→set_focus_mode→approach_ptz→capture_resolution_check，timed focus 为 focus 阶段）；ctx 已取消或阶段错误伴随 ctx 取消时 ReturnNilIfCtxDone/直接 return nil，SetZoomSpeed 与 SetExposure(auto) 失败只 Warn 不阻断；focus 表按 SN/period/宽或窄/昼夜/温度查 custom fTime 覆盖，未命中走 timed focus；最后 Capture→RawToImage 校验实际分辨率等于硬件配置。ApproachPTZ/ApproachAbsolutePTZ 先写目标再轮询 GetPrivatePTZ，100 次×60ms 内 MaxDiff(targetPtz)<0.11 视为到达，无 ctx 参与；SetAbsolutePTZ 在边界用 NormalizePublicTilt 处理负俯仰、clampZoom 受 MinZoom/MaxZoom 约束，GetPTZ 做私有/公共 PTZ 换算，speedValue 将 CamSpeed 1..6 映射 15..90、≥7 映射 100。
- Cam 接口新增 SetWiper(bool) / SetFan(bool) 两个方法，BaseCam 提供空实现（return nil）；接口扩展是对全部厂商实现的编译期约束。hope 已给出真实实现（见 3.6 续），hik ISAPI 路径的实现仍未见证据，是否落回 BaseCam 空语义需核对。
- .gitignore 新增 hikproxy/hikProxy 忽略规则（注释说明：若文件已被 git 跟踪，本规则不会取消其跟踪状态）。

审查要点：ControlProtocol 配置值的大小写/空白容错只在 cameraControlProtocol 一处，配置拼写错误会静默回退 Auto 而非报错；ISAPI 路径的 5s 超时与外层调用 ctx 是嵌套取 min 关系；digest 重试复用同一 req 对象，需确认在 GetBody 缺失场景下重试是否真能发出（依赖 digest.Transport 内部行为）；sdk_compat.go 中 SetPTZOSD/SetImageFlip/SetZoomSpeed 为静默 no-op（仅参数校验、不发起请求），调用方若期待 OSD/翻转/变速真实生效会误以为成功；SetStream 与 patchXMLTags 的正则改写依赖设备返回 XML 中目标 tag 存在，格式异常时显式报错而非静默跳过。

### 3.6 续（后续批次补全）

- ISAPICam 尾部方法：主流/低分取流地址固定 101/102 通道 RTSP，GetTrackerStreamUrl 返回构造期 streamUrl；CaptureByPreview 恒返回错误（预览抓帧走 RTSP 帧源，不支持本地抓图）；SetCurrentFocus/SetCruiseDefaultFocus 经 focusValueByZoom 查表（Period 为空或 day/night 走默认表，否则附加 profile 维度）调 SetFocusPos 并维护 lastFocusValue，查表未命中仅 Warn 不报错；GetZoomPosByHFov 在 ptRanges[h.Model()] 缺失时返回 1，否则按与目标 hfov 宽度绝对差最近选取 zoom；ZoomFocusPos 返回私有 Z 与 FocusPos；Status 用 2s ping（cam.IsReachable）判离线。
- 新增 gvs/cam/hik/isapi_reverse.go：ISAPIReverseCam 内嵌 *ISAPICam，构造时 flipImg=true；approach 为 ApproachPTZ/ApproachAbsolutePTZ 共用的反向机路径——TrackWorkMode 下 Warn 并跳过，俯仰非法（15<T<270）钳到 15，PTZ 各轴取整到 0.1 后写目标，500 次×60ms 轮询 MaxDiff(targetPtz)<0.11，超时返回带当前 PTZ 的错误；GetPanTiltRange 固定 0..360/15..345；SetThirdStreamConfig 为空实现；GetThirdStreamUrl 固定 103 通道。注意反向轮询 500 次与非反向 ISAPICam 的 100 次不同。
- hope.SetWiper/SetFan：driver.call 外层 ctx 600ms；SetWiper(true) 先经 HVS_ControlAux AUX_WIPER=1，再 time.AfterFunc 1s 异步补发 0（雨刷自动关闭），SetFan 走 AUX_DEFROST、无自动关闭逻辑。
- gvs/cam/huanyu/camptrange.go 的 ptRanges.PtzTable 数值整体重标定：前批已见 UV-EBH46V，本批补证 UV-EBH86V 与 UV-EBH86VS 同步更新，且三机型的新表数值完全一致（zoom 1..92 各档宽/高/对角视场角变化、结构不变），GetZoomPosByHFov 等查表结果会随之变化，属标定数据更新。另 gvs/cam/huanyu/httpsdk/netdevice.go 新增 NetDevice.SetZoomSpeed：速度钳制到 1..8，经 ptz/setBasicConfig 的 zoomSpeed 参数真实下发；同文件新增 NetDevice.SetWiper/SetFan（ptz/setWiper、ptz/setFan，bool 映射 on/off 字符串真实下发，无 hope 侧 1s 异步自动关闭逻辑）；huanyu.go 为 Huanyu 补 SetWiper/SetFan/SetZoomSpeed 三个委托 httpDevice 的包装方法。这与 hik ISAPI 路径 SetZoomSpeed 仅校验后 return nil 的 no-op 不同，huanyu 侧是生效实现，审查 Cam 接口行为时需按厂商区分。
- 审查补充：SetWiper 的 AfterFunc 在函数返回后异步执行且引用 h.userid，登出/重载后句柄有效性需注意；ISAPIReverseCam.approach 的 500 次轮询无 ctx 参与，进程退出时最长阻塞约 30s。

### 3.6 续二（后续批次补全）

- 新增 gvs/cam/viewzoom.go（package cam）：ZoomIntegerFrac=0.1。ViewFromZoom(z, get) 按倍率取视场——z≤0、NaN/Inf 或 get==nil 返回零值；小数部分 <0.1 时按最近整数倍查表，否则在 floor(z)/ceil(z) 间线性插值；任一端 CamView 宽高非正视为无效，取有效端，两端均无效返回零值。ViewAtZoom(c, z, night...) 为基于 Cam.GetView 的包装，night 省略时与 GetView(z) 一致。
- gvs/communication/osdstream.go：omni OSD 文本由 "%s,%.2f,%.2f"（方向,SpeedP,SpeedT）改为追加 SpeedScale 的 "%s,%.2f,%.2f,x%.2f"，以 "x" 前缀标注速度缩放倍率、保留两位小数区分档位；非 omni 分支格式不变，OSD 消费方解析需同步适配。
- proto 再生成：gvs/communication/proto/infer.pb.go 新增 TrackBgClassifierResult 消息（is_complex bool field 1、score float32 field 2），DetectResult 新增 field 11 track_bg_classifier（TrackBgClassifierResult 类型）；InitTrackResponse 由空消息扩展为新增 results 字段（repeated DetectResult，field 1），InitTrack 调用方现在可能携带初始检测结果返回；DetectResponse 仍为 results field 1（本批 diff 未见 error_message 字段加入）。消息/枚举索引整体后移（DetectResult 起 msgTypes 5→6、6→7、7→8……15→16，并一直延续至 CameraListResponse 16→17、TrackCommand 17→18、FileSaveNotification 18→19、TrackStreamResult 19→20、StopTrackRequest/Response 20→21/21→22、NormalizedPoint 22→23、OSDItem 23→24、OSDCommand 24→25、OSDStreamResult 25→26、ModelInfo 26→27、ConnectRequest 27→28、ConnectResponse 28→29），与 TrackBgClassifierResult 实际插入 msgTypes[5] 一致（depIdxs 首项 DetectResult.track_bg_classifier 指向索引 5、TrackStreamResult 的 OneofWrappers 由 msgTypes[19] 改为 msgTypes[20]、NumMessages 35→36），即插入点之后全部消息索引 +1；本批证据进一步核到文件尾段：WorkModeRequest 29→30、WorkModeResponse 30→31、GetMetaResponse_ClassInfo 的 depIdxs 引用由 34 改为 35（嵌套消息描述符索引 {10,0}→{11,0}），msgTypes 数组由 35 扩为 36 条，goTypes 中三个 map entry（ClassesEntry/ModelsEntry/ResultsEntry）索引同步由 35/36/37 变为 36/37/38。DetectResponse 本批 diff 未见 error_message 字段加入，仍只有 results field 1。本批 diff 同时含 GVS 侧源文件 gvs/communication/proto/infer.proto 的同步修改：新增 TrackBgClassifierResult 消息（注释明确 nil 区分“未分类”与“分类为简单背景”，推理服务不得以零值代替未分类）、DetectResult 新增 field 11 track_bg_classifier、InitTrackResponse 新增 results field 1，并加入“任何 proto 变更必须重新生成 infer.pb.go 一并提交、禁止手工编辑生成文件”的维护注释；infer 仓库侧对应生成代码是否同步仍待跨仓库核对（对照 SHA 待回填）。

### 3.6 续三（后续批次补全）

- gvs/datastorage/datastorage.go 新增 SaveRawBluImgForDebug(cat, img) 与 doDebugSaveNoBox：仅在 gvutils.DebugOn() 开启时把无框线原始帧写入 debugBuf（getDebugBuffer），文件名经 gvutils.GenerateFileNameByBluImg 生成，写失败记录 [ppbuf] 日志；与既有的 doDebugSave（带框调试图）是两条独立调试落盘路径。注意 SaveRawBluImgForDebug 的 cat 参数在实现中未被使用，调用方若期望按类别分流存储不会生效。
- gvs/dnn/ostrack/ostrackstream.go：OSTrackStream 结构体新增 saveVideoPath / saveFFDVideoPath（saveFHDVideoPath）字段，供 Retarget 重发 INIT 时复用录制路径；Init 拆出 sendInitCmd，INIT 的 TrackCommand 现在携带 bbox 转换的 Rect，RawFileName/OverlayFileName 仍来自 saveFHDVideoPath/saveVideoPath，Init 成功后加 mu 记录两个路径再按需启动 receiveLoop。新增 Retarget(rect)：在 mu 下检查 stream nil/stopped，未就绪返回错误；就绪则复用 sendInitCmd 但传空 save paths，注释明确不动 receiveLoop 与视频保存路径。convertToTrackDetectResult 返回 (result, ok)：nil pb、Status 不在 OSTrackStatusTrack..OSTrackStatusCapture、非 Lost 状态缺 Rect/TrackRect、任一矩形 range 非法均判 false；PredictRect/SearchRect 可缺省但存在即校验。convertRect 将负坐标裁剪到 0，Min>Max 判非法。TrackBgClassifier 存在时校验 score 非 NaN/Inf 且落在 0..1，通过后写 result.BgClassifier 并置 BgClassifierValid=true；math import 的用途即该分数合法性校验。receiveLoop 对无效 DetectResult 打 Warn 后 sleep 10ms 再 break，避免异常帧导致忙循环。

### 3.6 续四（后续批次补全）

- gvs/fastcap/fastcap.go：RotateCamAndCapBycapReq 移除函数开头 TrackWorkMode 前置返回、内联 3 次 ApproachPTZ 重试与相机类型错误上报，改为 RotateCamByPtz(capReq.Ptz) 失败即 return nil，成功后记录日志并委托 CaptureAfterRotate(capReq)；TrackWorkMode 拒绝、nil cap/cam 防御、scanVideoType 与 NARROWCAM 下 RotatedChan 通知改由 CaptureAfterRotate 承担。CaptureByFastCap 当前 PTZ 由 GetAbsolutePTZ 改为 GetPTZ，失败时记录 error 并直接 return nil（不再 errCount++ 后继续），capReq.Ptz 仅在 GetPTZ 成功后写回；本批可见片段中 RotateCamAndCapBycapReq 的 frameCount 未再参与控制，不能据此外推其他路径不用。
- gvs/grdetect/grbasedetector.go：UpdateCurrentDroneInfo 的引导目标刷新阈值由“距离差 <5 不刷新”扩展为 shouldRefreshGuideTarget：位移 >=5m、高度差 >=2m，或相对 guideCameraPosition 的视线角变化 >=0.3° 即刷新。guideCameraPosition 优先取 detector.location.Position 的非零经纬，否则回退 gvutils.GetConfig() 的 DeviceLocationLat/Lng/Alt；guideLookDeltaDeg 用 gvutils.CalcDirAndDis 求目标相对相机的方位/距离，以 AngleDiffDeg 算方位差、atan2(alt-cam.alt, max(dist,1e-3)) 算俯仰差并取较大者，相机经纬无效时返回 0。RebuildCurPtzList 夜间分支的 maxZoom/minZoom 由 comboTrackCam.TrackCam.Resolution() 改为配置 DirectionInfo.NarrowMaxZoom/NarrowMinZoom，GenerateDirectionPtzList 入参由 SpeckleCam 改为 TrackCam；夜间方向 PTZ 列表为空时现在记录 “skip empty night direction ptz list” Info 日志并直接 return（不再继续后续 alt/elH 分支）。RebuildCurPtzList 的 altPtz/elHPtz 两处 camView 由 TrackCam.GetView(int(z)) 改为 cam.ViewAtZoom(TrackCam, float64(z))，即视场计算改走浮点倍率插值。
- UpdateSpecklePtzList 中 speckle 确认的 narrowZoom 内联判定（父帧 Z 距 WideMaxZoom ≤1.1 取 NarrowMaxZoom，否则 NarrowMinZoom）抽为 narrowConfirmZoom(parentZ, DirectionInfo)；其结果 ≤0 时回退 gvutils.TargetNarrowTrackZoom()。新增 fixNarrowConfirmZ：确认 PTZ 的 Z ≤0 或 ≤wideMax+1.5（wideMax 取 WideMax/WideMin 较大者）时改写为 narrowConfirmZoom。
- 新增 guide-speckle 并行确认机制：guideSpeckleConfirmEnabled 要求有定位（isHavePosition）、SpeckleCam 存在、非夜间且配置 GrDetectorInfo.GuideSpeckleConfirmEnable；满足时 TrackCam 搜 drone、SpeckleCam 按 SpecklePix 单独设 zoom（speckleGuideZoom 按当前目标距离 selectSpeckleZoom，仅 SetZoom 不再 ApproachPTZ，失败仅 Warn）。SpeckleCam 检出 drone 后由 enqueueSpeckleCamDroneConfirm 取最优 drone 框中心经 cam.PublicXy2Ptz + 光轴补偿（ApplyPtzCompensationBySN）构造确认 PTZ（Z 用 narrowConfirmZoom），sensor 激活或 guidepriority.Active() 时直接走 applyConfirmedDronePtzList/preemptToConfirmedDrone 并可能把 GRStatusWaiting 置回 Searching，否则入 EnqueueVerifyRequest 交给 TrackCam 复核。
- getCurPtz 旧实现（hasConfirmedDronePreempt / nextVerifyCapRequest / speckle 分支 / consumeNextCurPtz）重写为 aimPlan 决策结构：trackAim/trackOrAllCam（guide 模式下返回 ComboCamAllCam）产生 Kind+Ptz+SpeckleZoom+CapReq；prepareGuideTurn 统一搜索态门槛与非搜索态等待，非搜索态每 2s 打一次 Tracef 阻塞日志，并新增自愈逻辑——GRStatusTracking 但无 CurDroneID、无 confirmedDronePtzSearch、speckle 待复核 >0 且 TrackCam 处于 cam.ScanWorkMode 时 Warn 并把状态回退 GRStatusSearching。本批补全决策细节：nextAim 按“确认抢占队列（trackAim，纯跟踪不带 CapReq）→ nextVerifyCapRequest（经 trackAimReq，内含 fixNarrowConfirmZ）→ confirmedDronePtzSearch（已确认窄角点只走 TrackCam，不并行斑点、不受 SpeckleCam 扫描态阻挡）→ 非夜间 SpeckleCam 存在且非扫描态/缺失时 logScheduleBlocked 拒转 → wantSpeckleSchedule 走 nextAimFromSpeckleBranch → 无定位时拒绝 TrackCam 直接吃 Wide 倍率 list”的顺序裁决；nextAimFromSpeckleBranch 先消化 speckleChanged 的确认队列（consumeNextSpeckleCapReq 置 RequestBySpeckle 后经 trackAimReq 走 TrackCam，耗尽标记 markSpeckleRequestExhausted），再 consumeNextCurPtz(true) 扫 curPtzList，无定位给 SpeckleCam、有定位经 scanAim（guide 时 AllCam 附 speckleGuideZoom）；rotateAim 只转云台不抓帧——Track/AllCam 走 TrackCamCap、SpeckleCam 走 SpeckleCamCap，AllCam 时斑点镜头仅 applySpeckleGuideZoom（SetZoom）不再 ApproachPTZ。调度入口现由 nextAim/rotateAim/prepareGuideTurn 组合驱动，旧的 getCurPtz 分支结构已被取代。

### 3.6 续五（后续批次补全）

- droneResultProc 新增 demo/手动模式护栏：gGRDetectorMgr.DemoModeEnable 开启且未开 gvutils.DemoAutoDetectEnable 时，检测分支到 TrackGapTime 到期也不自动 SetWorkMode(TrackStartWorkMode)+StartScanTrack（注释明确：否则手动跟踪结束后画面中仍存在的无人机会被本分支再次自动拉起）。StartScanTrack 成功后新增并行 speckle 取消：moreCapInference 非空时调 cancelOthers(bluImg.Cam) 撤掉 MoreCap pending，否则 SpeckleCam 处于 HoldWorkMode 时经 restoreScanWorkMode 恢复其扫描态——避免 trackCam 已开追后斑点相机仍被并行请求占住。
- 新增 manualTrackProc：消费手动框选参数的跟踪入口（配合 gvutils.ManualTrackParam / ManualTrackEnabled / ResetManualTrackParam）。defer 统一释放帧（moreCapInference.finishFrame + cam.BluImageReleaseWithPool），覆盖全部早退出口——GRDetector 路径帧来自有界 MatPool，MoreCap 路径回图占推理请求槽位，不释放会抽干池子或使 busy() 永久为 true。守卫链：ManualTrackEnabled 关闭即返回；tracker 处于 Running/Starting 时绝不消费参数（换目标由 trackthread 层 retargetFromManualParam 在流上重发 INIT，本节点直接 StartScanTrack 会触发 “Tracker is running” 失败分支误 StopTracking 杀掉当前跟踪，且抢先 Reset 与用户框选存在竞态）；DemoStableCallback 非空且未稳定到 DEMO 运行态（如 SCAN→DEMO 过渡期）不消费，避免被切换清理杀掉。param.Rect.ToPixelRect(videoW, videoH) 换算像素框，Empty 则 ResetManualTrackParam 丢弃；MoreCap 路径 CapRequest 缺 Ptz 时经 parsePtzFromMsgId(RequestID) 解析补拍 PTZ（StartScanTrack 的 PublicXy2Ptz 依赖它把像素换算为云台角）；置 RequestByManualTrack、合成 Id=DETECTID_drone/Precision 1 的无人机检测框喂 StartScanTrack，失败 StopTracking，成功 ResetManualTrackParam 且非夜间时 SpeckleCam 复位 WideTrackZoom。审查注意：该路径的帧释放责任由本函数兜底，替换其调用方时不得再叠加释放；合成检测框的 Fstatus 为 DtReal，下游若按真实检测统计需区分。

### 3.6 续六（后续批次补全）

- gvs/grdetect/grdetector.go 的 getRawImgProc 本次 diff 给出完整重写形态：状态门槛与自愈逻辑统一收敛到 prepareGuideTurn(100ms)（含非搜索态 2s Tracef 阻塞日志与 stale tracking 回退），demo 分支整体抽为 demoRawImage()（保留 DemoDetectWithoutTrackingEnable 拒转、CheckScanMode 门槛、confirmed 抢占/verify 队列消费、confirmedDronePtzSearch 消费、原地 CaptureByFastCap 置 HoldWorkMode 的原语义）；正常分支简化为 plan := nextAim() → Kind==ComboCamNone 直接返回 → plan.Ptz.Z<=0 打 Warn 并 sleep 40ms 拒绝 → rotateAim(plan) 旋转 → captureLocalAim(plan) 抓拍的固定四步。旧内联的 demo/九宫格/speckle 分支代码已全部删除，调度裁决细节集中在 nextAim 内（续四/续五已述）。
- gvs/grdetect/grbasedetector.go 的 confirmedDronePtzList 视场获取同样由 TrackCam.GetView(int(center.Z), night) 改为 cam.ViewAtZoom(TrackCam, float64(center.Z), night)，即浮点倍率插值查表已覆盖到 confirmed 点列生成路径；camView 宽高非正时回退为仅返回 center 单点。
- gvs/grdetect/grbasedetector.go 新增 bluImgWriterForDebugProc(img)：defer cam.BluImageReleaseWithPool，仅在 DebugOn() 时打 Debugf 日志并调 datastorage.SaveRawBluImgForDebug(DATA_DEBUG, img)（无框原始帧调试落盘，对应续三的 SaveRawBluImgForDebug/doDebugSaveNoBox）。接线关系已核实：GRDetectorMoreCap.StartDetector 在 droneDetectFlowGraph 追加 `-> 5@bluImgWriterForDebug` 边、在 speckleDetectFlowGraph 追加 `-> 5@specklebluImgWriterForDebug` 边，两条图均生效接线；GRDetector 侧图本轮 diff 未见该节点，是否接入待后续批次核对。
- grdetector.go 的 StartDetector 在 droneDetectFlowGraph 注册 manualTrackProc 节点，流程图字符串中 manualTrackProc 无任何入边，即它作为源节点独立循环消费手动框选参数（续五 manualTrackProc 的守卫链语义），输入不是 bluImg；同时本批可见 droneDetectFlowGraph 的既有边（getRawImgProc → droneDetectProc → bboxDrawerDrone → droneResultProc → detWriterProc/bluImgWriter）保持不变。

### 3.6 续七（后续批次补全）



- captureLocalAim(plan) 为单帧检测抓拍实现：ComboCamSpeckleCam 分支不抓帧，仅记 sensorSpeckleDispatchEpoch 并把 SpeckleCam 置 HoldWorkMode（由 speckle 图再抓）；ComboCamTrackCam/AllCam 分支在 CapReq 为 nil 时用 buildCurPtzListCapRequest(plan.Ptz) 现造请求，改经 TrackCamCap.CaptureAfterRotate(*capReq) 抓帧（不再走 RotateCamAndCapBycapReq），成功后 TrackCam 置 HoldWorkMode，AllCam 且 SpeckleCam 非 nil 时同样记 epoch 并置 Hold。nextVerifyRawImage 抓帧前新增 fixNarrowConfirmZ(capReq.Ptz, capReq.ParentPtz.Z, DirectionInfo)，即复核 PTZ 在进入 verify 抓拍链路时即完成窄角确认倍率修正。
- rawImgToBluImgProc 签名扩展为三输出 (rawResult, bluResult, bluResult2 *cam.BluImage, resultErr error)：夜间红外裸帧（Night 且 len(Data)==IRWidth*IRHeight*3）成功经 NewMatFromBytes 还原后显式把 JPEG 解码 err 置 nil（注释明确避免误判丢弃该帧），还原失败才返回 matErr；fsImgToDrone 为 nil 或 Data 为 nil 均显式报错。手动框选路由：ManualTrackEnabled 且 rawImg.Cam==TrackCam 时返回 (nil, nil, fsImgToDrone, nil)，把帧只送到第三路 out2 供 manualTrackProc 消费；注释明确 SpeckleCam 图未连接 out2，若其帧也走第三路会泄漏池化拷贝并使 speckleDetectProc 拿到 nil 帧。grdetector_test.go 的夜间用例同步改为四返回值解包。
- speckleDetectProc 新增斑点帧 drone 检测：runDroneOnSpeckle = 无定位（!isHavePosition，必有结果交 TrackCam 确认）或有定位且 guideSpeckleConfirmEnabled（与 MoreCap 双模型一致的并行确认语义）；满足且 droneDnn 非 nil 时 DetectByAutoCrop，连接关闭类错误仅 Warn，检出 drone 则复制 BluImage、附 Dts 后 enqueueSpeckleCamDroneConfirm 并立即 BluImageReleaseWithPool 释放拷贝，随后照常跑 speckleDnn 斑点检测。
- GRDetectorMoreCap.rotateCamProc 已由让路循环改为 aim 调度入口：先 prepareGuideTurn(40ms)，再 CheckScanMode 校验 TrackCam；随后 plan := nextAim() 并打印含 plan.Kind/plan.Ptz/plan.SpeckleZoom/isHavePosition/isNeedSpeckle/directionEnable/guideSpeckleConfirm 的 Debugf。ComboCamNone 或 plan.Ptz.Z<=0 均记阻塞/告警并 sleep 40ms 返回；rotateAim(plan) 成功后按 Kind 分发推理：ComboCamTrackCam/AllCam 走 TrackCamCap，用 droneEngineName 与 MoreCapGrTrack/MoreCapGrTrackNight 调 dispatchInference 并保存 msgId；ComboCamSpeckleCam/AllCam 走 SpeckleCamCap，用 speckleEngineName+droneEngineName 与 MoreCapGrSpeckle 调 dispatchInference。AllCam 下若 speckle camCap 缺失、SendWorkMode/dispatch 失败或槽位 busy，只要 track msgId 已发出就返回 msgId 让 waitForInferDoneProc 认领清理；纯 SpeckleCam 失败则返回错误/空。最终注释明确 drone 图只等 TrackCam msgId，speckle 结果由 waitForInferSpeckleProc 认领。新增 dispatchInference(camera, ptz, epoch, models, frameCount)：统一以 camera.Name()+"_"+ptz.ToString()+"_"+时间戳构造 msgID，moreCapInference.start 占位后发送 Mode="detect"、Param=generateMoreCapParam(models,true,frameCount) 的 WorkModeRequestParams，SendWorkMode 失败会 finish(pending) 并返回 “SendWorkMode queue full or unavailable”。
- GRDetectorMoreCap.StartDetector 流程图新增边：`bboxDrawerDrone -> 5@bluImgWriterForDebug`（drone 图）与 `waitForInferSpeckleProc -> 5@specklebluImgWriterForDebug`（speckle 图）接通调试落盘节点；并新增手动跟踪链 manualTrackRequestProc -> waitManualTrackInferProc -> manualTrackProc 三个节点——注意这与 GRDetector 图不同：GRDetector 的 manualTrackProc 无任何入边、独立循环消费手动框选参数（续五），MoreCap 侧 manualTrackRequestProc 是手动框选帧源，语义已见：guidepriority.RLock 下取 gvutils.GetManualTrackParam，矩形无效 sleep 40ms；tracker Running 时不发 detect、不重置参数，交由 trackthread 的 retargetFromManualParam 在流上重发 INIT；DemoStableCallback 未稳定或 moreCapInference.busy() 时 sleep 让路；否则用 TrackCam.GetPTZ() 取当前 PTZ（Z<=0 跳过），按当前画面发一次 detect WorkMode，generateMoreCapParam(...,false,1) 不转动云台，moreCapInference.start(...,true) 带手动标记。waitManualTrackInferProc 只精确等待该 msgID，空 msgID 直接返回，避免抢占自动检测在途请求；waitForInferRequest 本体扩为三图输出 (blu, writer, writerDebug, err)：超时/取消/无效/stale 返回 nil；命中 result 时依次复制 writerDebug 与 writer 两路调试图，成功路径返回 (blu, writer, writerDebug, nil) 三帧全交调用方；writer 拷贝失败也以 (blu, nil, writerDebug, err) 带错交出，不在函数内自行释放——释放责任由调用方 manualTrackProc 的 defer 统一兜底（含出错路径，避免泄漏已复制的 writerDebug）。

### 3.6 续八（后续批次补全）



- gvs/grdetect/morecaprequest.go：moreCapInference.pending 由单槽 *moreCapRequest 改为 map[cam.Cam]*moreCapRequest，即每相机一路在途请求，TrackCam 与 SpeckleCam 可并行 MoreCap 推理（ComboCamAllCam）；busy() 判 len(pending)>0；新增 findLocked(camera)/findByCamNameLocked 查找助手；start 增加 variadic manualTrack 参数写入 moreCapRequest.manualTrack 新字段，空 msgId 的 waitRequest 显式跳过 manualTrack 请求（id=="" && req.manualTrack 返回 nil），保证自动检测分支不抢占手动框选回图。claim(id, cameraName, allowMissingID) 经 findByCamNameLocked(cameraName) 按相机名定位请求，仅 req.id==id、或 allowMissingID 且 id=="" 放行空 id 时命中，否则返回 nil。deliver/valid/finishFrame/finish 全部改为按 req.camera / image.Cam 经 findLocked 定位：deliver 要求 req 非 nil 且 findLocked(req.camera)==req，不匹配即释放 image；valid/finishFrame 对 nil image 直接返回 false/不动作，否则以 image.Cam 定位请求再比对；finish 仅当 findLocked(req.camera)==req 时回收。finishLocked 校验 pending[req.camera]==req 后 delete 该槽位、close(req.done) 并抽空 result；cancel() 遍历清空全部 pending；新增 cancelOthers(keep cam.Cam) 释放除 keep 外所有在途请求（注释明确语义：trackCam 开始跟踪后撤掉并发 speckle 推理），与续五 droneResultProc 的 cancelOthers(bluImg.Cam) 调用点对应。
- gvs/grdetect/grdetectormorecap.go 的 UpdateDetectResult：SpeckleCam 回图分支（req.camera==SpeckleCam）新增 detector.enqueueSpeckleCamDroneConfirm(bluDrone, visionID, droneID)——req 非 nil 时取 req.visionID/req.droneID，droneID 为空回退 CurDroneID()；即 MoreCap 斑点帧上的 drone 检出同样走 wide 确认路径交 TrackCam 复核，随后 image/unused 对调（斑点继续走 SpecklesBluImg），注释明确确认倍率不同。waitForInferSpeckleProc 签名由三返回扩为四返回 (*cam.BluImage×3, error)，ctx 取消与 40ms 超时无请求路径均返回三 nil。
- gvs/grdetect/grfactory.go：删除 currentDevicePitchRoll/warnGSensorFallback/warnInvalidRotation 及 atomic 节流变量，同时移除 GSensorUseEnable/GSensorEnable 门槛、BuildGSensorMatrix/ExtractDevicePitchRollDeg 矩阵换算与样本新鲜度（maxRuntimeGSensorAge）校验；CalculatePTZByCamOffset 改为强制 rtk.GetGlobalCompassDataHandler().GetSnapshot()，取不到直接 Errorf 并返回错误（不再是无 GSensor 时仅用 Heading 的静默回退），devicePitch/Roll 直接取 snapshot 字段；倾斜补偿简化为仅 useCamPosOffset 时走 ComposeCameraBaseToENU（compensated 标记），CamPosOffset 或 baseRotation 非法改直接 Errorf 不再节流抑制，Debugf 日志去掉 freshGSensor 字段；注释描述的三档（外参+GSensor Compose／仅 GSensor Euler／无 GSensor 仅 Heading）与可见实现不一致——代码中无 snapshot 即报错、且 switch 仅含 useCamPosOffset 一个 case，未见 Euler 分支，以代码为准。同函数删除大段注释掉的 fov/zoom 死代码。
- gvs/grdetect/grfactory.go 变焦选择重构：selectOptimalZoom 改为委托 selectZoomByPix(distance, directCam, 0)；selectSpeckleZoom（续四已述其用途）实现为 SpeckleCam nil/距离 ≤0/GuideZoomConfigInfo.SpecklePix ≤0 时返回 0，否则以 SpecklePix 作为白天目标像素宽调 selectZoomByPix；selectZoomByPix 的 targetPix>0 仅替换白天 ObjPix，夜间仍用 ObjPixNight。

### 3.6 续九（后续批次补全）

- gvs/grdetect/morecaprequest_test.go 为 nextAim 决策矩阵提供锁定用例：有定位且 directionEnable → ComboCamTrackCam（不吃 SpeckleCam-only，SpeckleZoom=0，Z 取 curPtzList 当前项）；无定位 → ComboCamSpeckleCam 走 Wide 搜索倍率；confirmedDronePtzSearch=true 时即使 GuideSpeckleConfirmEnable 开启也只走 TrackCam（不并行斑点，Z 取列表值）；GuideSpeckleConfirmEnable+有定位 → ComboCamAllCam，SpeckleZoom 经 selectSpeckleZoom（SpecklePix 目标像素宽）计算，与 TrackCam 侧 selectOptimalZoom（ObjPix）结果不同且 >1，无定位目标距离时 SpeckleZoom=0。
- TestEnqueueSpeckleCamDroneConfirmUsesDetectCenterPtz 验证 enqueueSpeckleCamDroneConfirm 在 guidepriority.Dispatch 锁+beginSensorGuide 后取最优 drone 框（最高 Precision）中心经相机 Xy2Ptz 偏移写入 curPredictPtz 并置 confirmedDronePtzSearch=true，测试经 xy2PtzCamera 桩注入 deltaP/deltaT 精确校验该换算；TestMoreCapAllowsConcurrentTrackAndSpeckle 验证 map pending 改造后 TrackCam 与 SpeckleCam 可各持有 start 成功的在途请求。
- inferenceTestCamera 补齐 GetPTZ/GetAbsolutePTZ 桩（返回 P0/T0/Z10）以适配 Cam 接口现有签名，新增 fovZoomCamera（Resolution/GetZoomPosByHFov 桩）与 xy2PtzCamera（GetPrivatePtz/GetPublicPtz/GetView/Xy2Ptz 桩）支撑上述决策与确认 PTZ 用例；用例通过 gvutils.SetGuideZoomConfigInfo/SetConfNightMode/SetPosition 与 t.Cleanup 隔离配置副作用。

### 3.6 续十（后续批次补全）

- gvs/gvhttp/handler.go 路由扩展：相机控制组新增 POST /cameras/:device_id/wiper 与 /cameras/:device_id/fan（分别走 camera.SetWiper/SetFan）；demo 组新增 POST /cameras/:device_id/demo/manual_track（demomode.ManualTrack）。camera.go 沿用既有控制接口的注册变量模式：新增包级 SetWiperHandler/SetFanHandler func(camName string, enable bool) error，handler 为 nil 返回 500 "handler not initialized"，camName 经 requestCamName 规范化后必填；本批补全其接线：gvs/main.go initHttpServer 将两者分别绑为 SetWiperWrap/SetFanWrap，wrap 经 splitHTTPCamName 解析 camName（type 为空报错）、validateCamera 校验后调 camera.SetWiper(enable)/SetFan(enable)。
- demomode.ManualTrack：要求 gvutils.GetConfig().DemoMode 开启，否则 400 "not open demomode"；ManualTrackRequest 为归一化框（xTop/yTop/xBottom/yBottom，0--1），校验 0≤Min<Max≤1 否则 400；通过后 SetGrMgrDemoModeEnable(true) + gvutils.NewManualTrackParam(ManualTrackNormRect) + gvutils.SetManualTrackParam。DemoMode/Guide/AutoDetect/AutoDetectWithoutTracking 四个入口在原有逻辑后统一追加 gvutils.ResetManualTrackParam()——模式切换、引导注入、自动检测开关时清掉挂起的手动框选，与续五/续六的消费链衔接。
- gvs/grdetect/type.go：ComboCamPtzType 新增 ComboCamAllCam（续八/续九已述其语义，枚举值在 TrackCam 之后）；kNoMsgTimeout 由 15*time.Second 改为 5*time.Second（注释“无新引导报文后停止搜索”）；新增 ManualTrackParam{rect image.Rectangle} 类型及 GRBaseDetector.manualTrackParam 字段——与 gvutils 侧 ManualTrackNormRect/SetManualTrackParam 并存，本批未见 grdetect 侧字段的读写点，不得据此外推其已接线。
- gvs/gvtypes/const.go：OSTrackStatus 枚举新增 OSTrackStatusCapture——注释明确它是 Lost→Track 后 recaptureWindow 内的虚拟态，仅供内部按距离缩放速度的门控，从不作为外部跟踪状态暴露；RequestByXxx 常量新增 RequestByManualTrack=3（即续五 manualTrackProc 合成检测框的 RequestBy 标记值）。
- gvs/gvtypes/detect.go：DetectOSDInfo 新增 SpeedScale float64 字段，是本轮 omni OSD 速度倍率标注的数据载体；gvs/gvtypes/types.go 新增 TrackBgClassifier{IsComplex bool; Score float32}，TrackDetectResult 新增 BgClassifier/BgClassifierValid——与 proto TrackBgClassifierResult（续三）对应：推理服务未返回分类时为零值+false，不得用零值冒充“未分类”。
- 测试补充：TestMoreCapAllowsConcurrentTrackAndSpeckle 进一步锁定 cancelOthers 语义——同相机重复 start 仍单槽失败（"same camera must stay single-pending"），cancelOthers(TrackCam) 后 SpeckleCam 在途请求被释放、TrackCam 请求保留（仍 HoldWorkMode）；新增 TestMoreCapClaimsMatchIDAndCamera 覆盖 claim 的 id+相机名双重匹配（moreCapFixture 复用）。

### 3.6 续十一（后续批次补全）

- gvs/gvutils/config.go：手动框选参数侧落地——新增 ManualTrackNormRect.ToPixelRect(w,h)（归一化框→像素 image.Rectangle，直接 int() 截断）与 ManualTrackParam{Enable bool; Rect ManualTrackNormRect}；gManualTrackParam 为 atomic.Pointer[ManualTrackParam]，配套 NewManualTrackParam(rect)（Enable=true）/SetManualTrackParam/GetManualTrackParam（nil 时零值）/ManualTrackEnabled（非 nil 且 Enable）/ResetManualTrackParam（存零值），Set/Reset 均打 Info 日志。DemoStableCallback 在 gvutils 声明、由 gvs/main.go initHttpServer 赋值：要求 gStateMachine 非 nil、CurrTask==TASKID_DEMO 且 CurrTaskStatus==TASKSTATUS_RUNNING 才算稳定，与续五 manualTrackProc 的消费守卫对应。
- PidConfig 新增距离分档速度缩放配置（重检捕获窗口内按目标偏离画面中心距离缩放云台速度）：DistanceScaleEnable（默认 false）、DistanceNearRatio(0.2)/DistanceMidRatio(0.5)/DistanceNearScale(1.0)/DistanceMidScale(0.6)/DistanceFarScale(0.4)，全部 (0,1] 语义；LoadConfig 内经 clampUnit 归一化（NaN/Inf/非正值回退默认、>1 钳 1），且强制 DistanceMidRatio≥DistanceNearRatio。gvs/track/tracker.go buildTrackerInitConfig 把六项全部透传进 tracker init config（DistanceSpeedScaleEnable 等字段名）。
- LoadConfig 旧配置升级路径行为变更：SingleTrackInfo 迁移回写时 WideCameras[i].IsTiltUpPositive 由 false 改为 true、NarrowCamera.IsTiltUpPositive 由 true 改为 false（沿用旧版升级逻辑的老配置会改变俯仰方向语义，需核对存量部署）。
- gvutils 新增 SetGuideZoomConfigInfo(info GuideZoomConfigInfo) 配置注入 setter（续九测试用其隔离配置副作用的依据）。

### 3.6 续十二（后续批次补全）

- gvs/track/trackerengine/ptz.go：PtzController.Update 返回签名由 (controlPan, controlTilt, zoomDir) 扩为四元组，末尾新增 speedScale；UpdateRequest 相应新增 EnableSpeedScale 字段。三处返回路径（remote-controlled 提前返回、死区内提前返回、正常 PID 路径）均在 math.Round 前将 controlPan/controlTilt 乘 speedScale，即缩放作用于最终下发速度。
- 新增 distanceSpeedScale(normX, normY)：p 为 nil 或 pidCfg.DistanceSpeedScaleEnable 关闭时返回 1；先做档位护栏——nearRatio 钳 ≥0、midRatio 钳到 [nearRatio, 1]。以 radius=hypot(normX, normY)（椭圆归一化后的中心偏差半径）分档：radius≤nearRatio 取 DistanceNearScale；near..mid 区间用 smoothStep 连续插值到 DistanceMidScale；midRatio<1 且 radius<1 时继续用 smoothStep 插值到 DistanceFarScale；其余（radius≥1 或 midRatio=1）取 DistanceFarScale。新增 smoothStep(t) 辅助函数（t≤0 返 0），跨档边界速度连续、无阶跃。EnableSpeedScale 的置位方已补全：DroneTracker.BuildUpdateResult 在 trackStatus==OSTrackStatusCapture（重检捕获窗口内，见续十三）时置 params.EnableSpeedScale=true 再调 PtzController.Update；buildTrtResponse（TRT 路径）显式置 false——TRT 不启用距离速度缩放，但仍调 updateRecaptureState 推进丢失/重检状态机，与 BuildUpdateResult 保持一致；UpdateResult.SpeedScale 随响应带出（Lost 提前返回路径与正常路径均填，默认 1.0）。
- ptz_test.go 全部 Update 调用点改为四返回值接收，属签名适配；本批无 distanceSpeedScale 的专项断言。
- gvs/track/tracker.go StartScanTrack：PublicXy2Ptz 失败日志加 "PublicXy2Ptz: " 前缀；起步 ApproachPTZ 前置条件由旧“RequestType==RequestByPtzList 且 !IsDirectTracker（DirectTracker 豁免，Absolute 路径被注释）”改为“RequestType != RequestByManualTrack”——即 DirectTracker 也不再豁免起步 ApproachPTZ，仅手动框选路径跳过（与续五 manualTrackProc 的合成框配合）。lastScanPtz 语义变更：由存公共 PTZ（lastPtz）改为 GetPrivatePtz(lastPtz) 私用坐标；Info 日志扩展为同时打印 RequestType、当前公共 PTZ、目标 PTZ、当前私用 PTZ；顺带删除一处重复的 SetWorkMode(TrackWorkMode)。
- stopTracking 回程由 ApproachPTZ(tracker.lastScanPtz) 改为 ApproachAbsolutePTZ(tracker.lastScanPtz)，注释明确 lastScanPtz 已是私用坐标系、ApproachAbsolutePTZ 直接消费私用坐标——与 StartScanTrack 的 GetPrivatePtz 改写配套，审查跟踪回程路径时需按私用坐标理解 lastScanPtz。

### 3.6 续十三（后续批次补全）

- gvs/track/trackerengine/trackerengine.go：DroneTracker 新增重检捕获状态机——recaptureMu 保护的 lost/recaptureUntil 字段，recaptureWindow=2*time.Second（注释明确用时间而非帧数：OSTrack 推理在远端 GPU 异步执行，帧率随负载/网络波动，帧数无法稳定对应物理时长）。updateRecaptureState(raw, now)：raw==Lost 置 lost=true；raw==Track 且 lost 时置 recaptureUntil=now+窗口、清 lost、打 "change to ReCapture" Info；now 在窗口内返回 OSTrackStatusCapture；now 由调用方注入便于测试窗口过期。三处重置点 ResetSession/InitTrt（成功路径）/resetTrtState 均调 resetRecaptureState。该状态机只影响速度缩放门控，不改变对外 TrackingStatus，与 gvtypes.OSTrackStatusCapture 注释（续十“仅供内部按距离缩放速度的门控的虚拟态”）吻合。
- DroneTracker 新增 Retarget(rect, zoom)：跟踪中换目标——要求 streamTracker 非 nil 且 trtInitialized，调 streamTracker.Retarget(rect) 在已建流上重发 INIT；重置 tid=0/count=0/oef=NewOneEuroFilter(50,3)；EnableOmni 时重建 PtzController（先 Close 旧 pcl，注释明确避免泄漏 pidrpc 连接与远端 PID 注册）。与 InitTrt 不同不需要本地帧（远端引擎按 SubName 自取图）。
- gvs/track/trackthread.go 新增 retargetFromManualParam(zoom)：消费手动框选参数的实现体（续五/续六提到的 trackthread 层换目标入口落地），尾部与调用点已由后续批次补全。归一化框基于窄相机跟踪画面，经 trackerFrameSize(tcam, Night) 换算像素框（与结果/OSD 同一基准）；空框 ResetManualTrackParam 并报错返回；tracker nil 亦 Reset；调 dt.tracker.Retarget(pixelRect, zoom) 失败时保留参数下一帧重试（注释明确远端流瞬断可自愈，不丢用户操作），同时按 2s 节流打 Errorf 日志（"retargetFromManualParam Retarget failed"），并返回 false——false 语义为继续正常处理旧目标结果，避免换目标失败时云台失控。成功则打 Info 日志、调 gvutils.ResetManualTrackParam() 清参数、返回 true。调用点 handleTrackResult：在取到 currPtz.Z 之后、一切结果处理（含 lost 早退）之前调用 retargetFromManualParam，注释明确手动框选换目标优先于一切结果处理，返回 true 则直接 return 丢弃本轮结果（本次结果仍属旧目标，仅已在流上重发 INIT）。DroneTrack 结构新增 lastRetargetFailLog time.Time 字段承载失败日志节流（注释：ProcessTrackResult 单 goroutine 访问，无锁）。handleTrackResult 的 OSD 输出构造同步新增 SpeedScale: res.SpeedScale，把续十二 PtzController 的距离速度倍率透出到跟踪 OSD 展示。Tracker.lastScanPtz 字段补注释 "private ptz"，与续十二私用坐标改写一致。

### 3.6 续十四（后续批次补全）


- hikproxy/hikProxy 二进制文件批次1发生变更（binary diff，无可读源码差异），批次2已被删除；既有 .gitignore 忽略规则（hikproxy/hikProxy）继续适用，属打包产物/预编译代理的例行变更，无接口语义变化证据。

### 3.6 续十三（批次1补全）

- 新增 gvs/cam/ptzsampler.go：PtzSampler 按相机持续采样 GetPrivatePTZ，默认 20ms 间隔、保留最近 2s 的 (UnixMilli, PTZ) 序列；StartPTZSampling(c Cam) 以 sync.Map 按 c.Name() 幂等启动，StopPTZSampling 关闭 stop chan，GetPTZByTime 取最后一个不晚于目标 ts 的采样、若目标 ts 早于整个缓冲则退回最旧一条，GetLatestPTZ 立即采样并写入缓冲。sampleNow 依据 cam.NightMode() 在 VT_LIGHT/VT_IRD 间选择 GetPrivatePTZ 参数，失败仅 Debug；pruneLocked 用 append(samples[:0], samples[idx:]...) 复用底层数组裁剪窗口。
- gvs/app.go loginCameras 在 narrow cam 登录成功后、SpeckleCam 空判断前调用 cam.StartPTZSampling(gNcam)；退出侧已由后续批次补全：gvs/main.go closeAll 在 gNcam.Logout() 之前调用 cam.StopPTZSampling(gNcam)，登出前关闭采样协程。
- gvs/cam/cam.go 的 BluImage 新增 DetectTimeStamp int64，与既有 Time time.Time 并存；消费链已由后续批次补全：gvs/track/trackthread.go handleTrackResult 写 trackerBluImg.DetectTimeStamp = trackResult.TimeStamp（值来自推理 DetectResult.time_stamp，经 osttrackstream.convertToTrackDetectResult 拷贝入 TrackDetectResult.TimeStamp），gvs/track/tracker.go writeTrackRecord 以它为基准调 cam.GetPTZByTime 回溯检测时刻 PTZ，与采样缓冲（续十三批次1）正式对齐。
- proto 再生成增量：DetectRequest 新增 field 11 time_stamp int64，DetectResult 在 field 11 track_bg_classifier 之外新增 field 12 time_stamp int64；TrackBgClassifierResult 与 DetectResult.track_bg_classifier 的注释进一步明确 nil/缺省表示未分类、不得用零值冒充。该事实使 GVS DetectRequest field 11 与 infer 侧 detect_with_track field 11 形成同号异义风险。

### 3.6 续十五（后续批次补全）

- gvtypes.TrackDetectResult 新增 TimeStamp int64 字段：ostrackstream.convertToTrackDetectResult 在拷贝 SequenceId 处同步拷贝 pb.DetectResult.TimeStamp，打通了推理服务时间戳到 GVS 内部结果的传递（与续十三批次1 proto DetectRequest/DetectResult 的 time_stamp 字段配套）。
- 跟踪记录文件功能（gvs/track/type.go Tracker 新增 trackRecordFile *os.File）：StartScanTrack 在生成 videoFilename 后调 openTrackRecordFile，按视频同名去扩展名加 "_gvs.txt" 后缀、存于 savePath；processTrackResultBluImg 每帧调 writeTrackRecord 追加一行；stopTracking 调 closeTrackRecordFile 关闭。writeTrackRecord 格式为 `timestamp,ptzTime,zoom,x,y,w,h`（实现 Fprintf "%d,%d,%.2f,%d,%d,%d,%d"，七列）：DetectTimeStamp 为 0 时退回 time.Now().UnixMilli()；cam.GetPTZByTime(tracker.trackCam, timestamp) 未命中退回 bluImg.Ptz，取 PTZ 成本打 Debug 计时日志；目标框取 bluImg.Dts[0].Rect；写盘失败 Errorf。注意函数注释称格式为 "timestamp,zoom,x,y,w,h" 六列，与实际写入七列不符，注释滞后于实现。
- trackerengine.DroneTracker.InitTrt 签名变更：frame gvcv.Mat 参数改为 bluImg *cam.BluImage（移除 gvcv import、加入 cam import），streamTracker.Init 改用 bluImg.Data；初始化框仅在 RequestType==RequestByManualTrack 时做 clampBBox 并构造 rect（注释 "manual Track send bbox"），其余路径 rect 为零值空矩形。调用方 trackthread.StartTracker 同步改为整体传 bluImg；审查其它 InitTrt 调用点需按新签名与“仅手动框选才携带 bbox”的语义核对。