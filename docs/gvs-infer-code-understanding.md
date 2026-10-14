<!-- SOURCE-REPOSITORY: lizheng/lzvsg/gvs_infer -->
<!-- SOURCE-SHA: 920c7a3fcd3eb2f567822659e55c7a14e0f88daf -->

# gvs_infer 代码理解

> 文档维护：2026-10-13。依据顶部 SOURCE-SHA 的本地 Git 对象核对关键协议与模型结构；未访问远端核验最新 main，不宣称重新完成全仓逐行审计。

本文描述该版本推理服务，供审查 Agent 作为全局理解上下文。与 GVS 通过 protobuf、模型配置和权重目录形成运行时契约（见 §4.1）。SOURCE-SHA 保持不变，维护日期不代表源码升级。

## 0. 文档方法论（维护约定）

按四层认知模型组织，审查时按层取用：

- **L0 系统上下文**：定位、部署形态、外部依赖、端口/路径清单。
- **L1 架构分层与模块目录**：分层职责；每模块的关键文件、核心类型、对外接口。
- **L2 调用链与数据流**：每个对外入口一条"入口→内部路径→出口"链；数据如何流动；并发/线程边界。
- **L3 契约与不变量**：跨仓库接口、配置与权重、错误语义、资源生命周期、构建产物。

更新规则：任何结论必须能在当前 SHA 的源码中指认文件/符号；改动代码后同步对应层级；保留顶部 SOURCE 标记。

## 1. L0 系统上下文

**定位**：C++17 + CUDA/TensorRT 推理服务进程。对外暴露 gRPC `InferService`（`proto/infer.proto`），对内通过 GStreamer 拉流/推流并驱动检测、跟踪、分类模型。同机部署在 gvs（Go 主服务）旁，gvs 侧经本进程完成模型推理与流媒体处理。

**进程形态**：单进程多线程（gRPC sync server + GStreamer 主循环 + 每相机推理 worker + 监控线程）。监听 `0.0.0.0:50051`（insecure，消息上下限 50 MiB；keepalive 8s/5s；单连接并发流上限 32；显式关闭 SO_REUSEPORT 以便检测端口占用）。同步服务中长流会长期占用处理能力，实际线程数量还取决于 gRPC 调度和内部 reader/sender 线程；不能简单按 RPC 数量精确推算线程数。

**外部依赖与关键路径**：

| 依赖/路径 | 用途 |
|---|---|
| TensorRT / CUDA / cuDNN | 推理运行时；`Config::GetTensorRTMajor()` 读 `/usr/local/tensorrt/include/NvInferVersion.h` 探测主版本 |
| GStreamer + NVIDIA 插件 | RTSP 拉流、解码、RTMP/文件输出；`gst_init` 于 `InitGstreamer()` |
| OpenCV | 帧处理、OSD 绘制、replay |
| gRPC/protobuf | 对外服务；生成代码随仓库提交（`proto/infer.pb.cc` 等，无单一生成源） |
| yaml-cpp / spdlog / nlohmann | 配置解析 / 滚动日志（`/opt/lz/log/gvs_infer.log`，5MB×10）/ JSON |
| `/opt/lz/config/gvs/gvs_module_service.yml` | 服务配置（不存在则用默认并回写） |
| `/opt/lz/config/gvs/weights/gvs_weights.yml` | 模型清单（旧版兼容 `gvs_infer.yml`） |
| `<weights_path>/dnnclasses.json` | 检测类别表，启动时 `InitDetectType` 加载，失败即退出 |
| `/opt/lz/testset`（`TESTSET_ROOT` 可覆盖） | `--test-model` 测试图片集 |

**启动模式**（`main()` 参数）：默认起 gRPC 服务；`--test-model [--with-infer] [--test-path P] [--gpu-id N] [--weights-path W]` 加载验证后退出；`--replay <mp4> [--night]` 本地文件回放（不起服务）；`-v` 版本。

## 2. L1 架构分层与模块目录

```
┌─ 接入层 ─────────────────────────────────────────────┐
│ src/service/infer_service_impl.{h,cpp}  InferService │
│   全部 11 个 RPC；SafeRpc 异常→INTERNAL 统一兜底       │
├─ 领域核心 ────────────────────────────────────────────┤
│ src/core/   Config(单例) / ModelManager(单例仓库) /    │
│   ModelFactory(注册表) / Model(纯虚基类) /             │
│   ExtendedModel(主模型+扩展链) / detect_config(类别表) │
│   / shm_utils(共享内存读图)                            │
│ src/models/ 6 个模型实现：yolov5 yolov26 deimv2        │
│   efficientnet ostrack motor_drone(条件编译)           │
├─ 流媒体层 streamer/ ──────────────────────────────────┤
│ StreamManager(相机/管道注册+内存监控+RTSP心跳)          │
│ VideoStreamPipeline(GStreamer 管道: 输入→解码→推理→输出)│
│ InferenceEngine(单相机推理 worker: 帧队列→模型调用→OSD) │
│ rtsp_input / rtmp_output / file_output /               │
│ compressed_recorder / element_factory                  │
│ TrackStreamContext / WorkModeStreamContext(流式RPC双线程)│
│ OSDSession(OSDStream 命令处理)                         │
├─ 支撑层 ──────────────────────────────────────────────┤
│ proto/ 接口定义与生成代码；libs/include 私有推理库头文件  │
│ test_model/ 模型加载/测速测试套件（含 trtexec 策略/gtest）│
│ third_party/ nlohmann + spdlog；config/ 配置样例       │
└───────────────────────────────────────────────────────┘
```

### 2.1 核心类型与接口

| 模块 | 关键类型 | 职责与对外接口 |
|---|---|---|
| `Config`（单例） | `ModelConfig{enabled, model_real_name, model_type, model_path, continuous, 尺寸, conf/iou_thresh, extend_models, extend_model_order, vision/decoder_engine_path...}` | `LoadServiceConfig/SaveServiceConfig`、`EnsureWeightsConfigFile`(旧版迁移) + `LoadWeightsConfigFile`、`GetModelRealName`（逻辑名→物理名，查不到原样返回）、`GetModelType`、`GetContinuous`、`SupportMultiFrame(is_night)`、`DetectModeString(cam_type)` |
| `ModelManager`（单例） | `model_store_ map<key, shared_ptr<Model>>`，`shared_mutex` | key 规则：`sub_name` 为空 → `model_name`，否则 `model_name\|sub_name`。`CreateModel/StoreLoadedModel/GetModel/RemoveModel`；AddModel 返回 0 成功 / -1 已存在 / -2 失败 |
| `ModelFactory` | `creators_ map` | `InitModelFactory()` 注册 deimv2/yolov5/yolov26/efficientnet/ostrack（motor_drone 需 `ENABLE_MOTOR_DRONE`） |
| `Model`（纯虚） | `New/Detect/Close/GetModelType` | 每个 RPC  Detect/New 最终落到具体模型的这三个方法 |
| `ExtendedModel` | primary + extensions + order(`before/after`) | 组合模型：Detect 按顺序链式调用；`PrimaryModel()` 供 GetMeta 解包 |
| 模型实现 | `Yolov5Model{Yolov5* dnn_, YoloMeta, mutex}`、`OSTrackModel{TrackEngineHandle}`（多一个 `InitTrack`）、`Deimv2Model`、`Yolov26Model`、`EfficientNetModel`、`MotorDroneModel` | 薄封装层：持有私有推理库（`libs/include/yolov5.h`、`track_c_api.h`）句柄；`GetModelType()` 返回类型名供 InitTrack/GetMeta 分派 |
| `StreamManager`（单例） | `cameras_`、`streams_ map<cam_name, shared_ptr<VideoStreamPipeline>>`、监控线程 | `syncCameraList(gpu_id, is_night, camera_map)` 增量建/删管道；`startTrack/stopTrack/getPipeline/clearOSD`；后台线程做内存泄漏检查与 RTSP 心跳 |
| `VideoStreamPipeline` | GStreamer `pipeline_`、`InferenceEngine`、输出组件集 | `run()` 建管（RTSP 输入→decode→appsink）；Pad Probe 取帧 → `processFrames()` → `InferenceEngine::pushFrame`；输出：`VideoOutput` 组件（RTMP/文件）+ `CompressedRecorder`（H.264 直存旁路）；`setMode/setOSDState/onOSDFeedbackReceived/waitForReplayEos/stop` |
| `InferenceEngine` | `worker_` 线程、`frame_queue_`（上限 3）、`AsyncDetectContext` | `pushFrame` 非阻塞（仅 GST 线程调用）；workerLoop 消费 → `runInference(mode)`：TRACK→`track()/initTrack()/detectInTracking()`；DETECT→异步批采集+`detect_inference_loop`；MANUAL_DETECT_ONLY→单模型检测；OSD 绘制（`drawOSD/drawBBoxWithOptions`）；OSD 反馈追踪（`last_confirmed_seq_`，kMaxPendingFrames=3，丢失则跳过推理直出原帧） |
| `model::ModelInfo`（单例） | `(ModelType, TimeType)→ModelConfig` 注册表 | 5 类用途（DETECT/TRACK/TRACK_DETECT/SPECKLE/EFFICIENT，各分昼/夜）的逻辑名注册，`InitModelInfo()` 建立用途与昼/夜映射（如 `drone.engine/droneNight.engine`、`droneTracking.trt`、`classifierDay.engine` 等） |
| `detect_config` | `g_detect_name_2_id / g_detect_info_map / g_catId2Name` | `dnnclasses.json` → 类别映射；特殊 cat：`drone=1, speckle=-1, plane=-2, bird=-3, UAO=-4, zoom_in_box=-10` |

## 3. L2 调用链与数据流

### 3.1 启动序列（`main()`）

```
SIGPIPE 忽略（入口处 + gst_init 后各一次）
→ LoadServiceConfig(gvs_module_service.yml) → SaveServiceConfig（确保文件存在）
→ 权重配置：EnsureWeightsConfigFile(gvs_weights.yml, 旧 gvs_infer.yml) → LoadWeightsConfigFile
  （--weights-path W 覆盖 weights_path_ 并切到 W/gvs_weights.yml）
→ InitLogger（spdlog 滚动文件）→ InitModelFactory（注册 5/6 类模型）
→ InitGstreamer → InitModelInfo（初始化昼/夜逻辑模型映射）→ PrintConfig
→ InitCudaBuf（Yolov5InitCudaBuffer + DebugMode）
→ 再次应用 --weights-path 覆盖（优先级高于 yml WeightsPath）
→ 分支：
   --test-model → TestLoadAllModels(gpu, test_path, with_infer) → ModelTestSuite::TestAll → 退出码 0/-1
   否则 → InitDetectType(weights_path/dnnclasses.json)（失败 → exit -1）
        → --replay → ReplayLocalFile（手动 AddModel+New 加载 DETECT/TRACK 模型 → VideoStreamPipeline 回放 → 显式 RemoveModel 规避 CUDA 静态析构卸载竞态）
        → RunServer()：ServerBuilder(50051) → RegisterService(InferServiceImpl) → Wait()
```

### 3.2 单帧数据流（DETECT/TRACK 稳态）

```
RTSP 源 → rtsp_input → decodebin → appsink(onNewSample)
  → VideoStreamPipeline::processFrames（BGR cv::Mat + pts/duration/capture_ts）
  → InferenceEngine::pushFrame（入帧队列，深度 3，满则丢帧）
  → workerLoop 取帧 → runInference(mode)
      TRACK:  need_init_ ? initTrack(首帧+初始框 via TrackStream INIT 命令)
              : track()（ostrack TrackEngineHandle，feed 帧率 EMA 控制）
              可选 detectInTracking（TRACK_DETECT 模型，detect_with_trajectory_ 控制）
              → extractZoomRegion（ZOOM 352×288）
      DETECT: AsyncDetectContext 采集 N 帧（N=各模型 continuous，受 frame_count 限制）
              → detect_inference_loop 逐模型 detect()
      MANUAL_DETECT_ONLY: 单模型 detect() 指定帧保存
      detect() 内部: MatToProtoMat 或共享内存路径(data_path)
              → ModelManager::GetModel(real_name, sub) → Model::Detect
              → 私有推理库（yolov5/deimv2/... .so）→ TensorRT
  → drawOSD / drawBBoxWithOptions（osd_feedback_lost 时直出原帧）
  → InferenceResult 回调 → VideoStreamPipeline 分发：
      → rtmp_output（target_raw_url / target_zoom_url 推流）
      → file_output / CompressedRecorder（raw/overlay 录像，文件名来自 TrackCommand/WorkModeRequest）
      → TrackStream 结果队列（TrackStreamContext::enqueueResult → senderLoop → gvs）
      → WorkModeStream 事件（capture_done / detect_done → WorkModeStreamContext）
```

### 3.3 对外 RPC 调用链（逐个）

| RPC | 入口 → 内部路径 → 出口 |
|---|---|
| `Connect` | → `Config::SetEnableCaptureMore` → `SupportMultiFrame(is_night)`（昼：deimv2 且 TRT≥10；夜：yolov26）→ 回版本/git/构建时间 + `models_` 全表 + support_multi_frame |
| `New` | 逻辑名→`GetModelRealName` → `ModelManager::GetModel`（已存在→OK 幂等）→ `CreateModel(model_type)`（不支持→INVALID_ARGUMENT）→ `model->New(request,response)`（加载引擎，失败原样返回）→ `StoreLoadedModel`（-1→OK 并发幂等；-2→INTERNAL） |
| `Detect` | `GetModel(real_name, sub)` → 未加载 **NOT_FOUND "Model not loaded, please call New interface first"** → `model->Detect`（ExtendedModel 按 order 链式调用主/扩展模型） |
| `InitTrack` | GetModel → 类型必须是 `ostrack`（否则 INTERNAL "ModelType invalid"）→ `OSTrackModel::InitTrack`（模板帧+初始框） |
| `Close` | **当前是 no-op**：只打日志返回 OK，不释放引擎。客户端不能据此认为显存已释放 |
| `GetMeta` | GetModel(real_name, "") → ExtendedModel 解包 primary → 仅 yolov5/yolov26 填 `YoloMeta`（classes/diou_threshold/location_id/version），其他类型返回空 OK |
| `SyncCameraList` | proto 相机表 → `streamer::Camera` map → `StreamManager::syncCameraList`（增量 diff：新建/更新管道、移除消失相机、起内存监控线程） |
| `TrackStream`（双流） | `TrackStreamContext`：readerLoop 读 `TrackCommand`（INIT：sub_name+cam、首帧+初始框 → pipeline startTrack + setTrackCallback；STOP：stopTrack 关流）→ 推理结果经 `enqueueResult` → senderLoop 发 `TrackStreamResult{detect_result}` |
| `StopTrack` | `StreamManager::stopTrack(sub_name)`（按相机名停跟踪） |
| `OSDStream`（双流） | `OSDSession::process`：UPDATE → 转 OSDItem → `StreamManager` 找管道 → `InferenceEngine::updateOSDItems/setOSDState`（回 OSDStreamResult）；CLEAR → clearOSD；读失败/处理失败退出并 clearOSD |
| `WorkModeStream`（双流） | `WorkModeStreamContext`：readerLoop 收 `WorkModeRequest{msg_id, cam_name, mode, param}` → pipeline `setMode(StringToWorkMode(mode), param)`；param 为分号键值串（detect: `model_name:a,b;enable_continuous:true;frame_count:5`；manual_detect_only: `mode_name;raw_file_name;overlay_file_name`）→ 事件经 WorkModeStreamCallback → senderLoop 回 `WorkModeResponse{msg_id,status,results,image}` |

### 3.4 并发与线程边界

- **同步 gRPC 服务**：长流会占用处理线程；TrackStream/WorkModeStream 内部另有 reader/sender 线程和结果队列。线程规模应以实际配置和测量为准。
- **每相机**：GStreamer 管道线程（pull/decode/push）+ `VideoStreamPipeline` worker（processFrames）+ `InferenceEngine` worker + detect 模式额外 `inference_thread`。
- **共享状态**：`ModelManager`（shared_mutex）、`Config`（单例无锁，启动后只读为主；`SetEnableCaptureMore` 由 Connect 写）、`StreamManager`（mutex + 监控线程）、`InferenceEngine` 帧队列（mutex+cv，深度 3 满丢帧）。
- **OSD 反馈闭环**：gvs 经 OSDStream 回包 → `VideoStreamPipeline::onOSDFeedbackReceived` → `InferenceEngine::notifyOSDFeedback(sequence_id)`；连续 3 帧未确认 → `osd_feedback_lost_` → 跳过推理直出原帧（防积压）。
- **退出路径**：replay 模式必须 `pipeline.reset()` 后 `RemoveModel`，避免静态析构期 CUDA driver 已卸载（`cudaErrorCudartUnloading`）。

## 4. L3 契约与不变量

### 4.1 跨仓库契约（gvs ↔ gvs_infer）

- 传输：infer 监听 `0.0.0.0:50051`，insecure；GVS c948e000 的 `gvs/communication/grpc.go` 直接连接 `127.0.0.1:50051`。监听所有网卡与客户端走回环是不同边界。该 GVS 快照中的 qatool `-bench_port` 仍默认 9979，不能假定 QA 链路已同步迁移。
- proto：infer 使用 `proto/infer.proto`，GVS 使用 `gvs/communication/proto/infer.proto`；各自有源定义和生成代码，目前没有单一共享协议来源。RPC 全集：`Connect / New / Detect / Close / GetMeta / InitTrack / SyncCameraList / TrackStream(双向流) / StopTrack / OSDStream(双向流) / WorkModeStream(双向流)`（`SetWorkMode` 已注释下线，由 WorkModeStream 取代）。
- **proto 漂移（infer 920c7a3f 与 GVS c948e000 对照）**：`DetectRequest.detect_with_track`(11)、`DetectResponse.error_message`(2) 为 infer 侧新增；`TrackStreamResult` 与 gvs 侧 oneof 定义不同。新增字段保持线协议兼容；字段号不可复用，删除字段需 `reserved`；双向流 message 结构必须两端同步，否则静默忽略。
- 模型命名：`New/Detect/Close/InitTrack` 必须使用一致的 `model_name/sub_name`；逻辑名→物理名映射由 `gvs_weights.yml` 的 `model_real_name` 决定，前缀规则（`deimv2_`、`deimv2_ground_`、`yolov26_` 等）是跨仓库接口，不能单仓修改。
- 多帧语义：`continuous`（命中帧数）与 `detect_uuid`（流 ID，变化即清服务端连续帧状态）共同决定多帧检测行为；`Connect.support_multi_frame` 昼侧要求 deimv2+TRT≥10、夜侧要求 yolov26。
- 权重：默认根目录 `/opt/lz/config/gvs/weights`（`Config::weights_path_` 默认值）；`--weights-path` 覆盖同时切换该目录下的 `gvs_weights.yml`。部署覆盖必须同时影响 infer 加载/盘点/`--test-model` 与 qatool 校验读取。
- 动态库与 TensorRT 主版本绑定：库名按 `cucodes_${TRT_TAG}`、`yolov5_det_${TRT_TAG}`、`enet_classifier_${TRT_TAG}`、`ostrack_${TRT_TAG}` 组织，`TRT_TAG` 默认 `trt8`（CMake 未传时 `-Dtrt8`）。engine 与运行时主版本必须一致；x86_64/aarch64、TRT8/TRT10 产物不可按文件名相似推断互换。

### 4.2 错误语义

- `NOT_FOUND`：模型未加载/名称映射失败（Detect/InitTrack/GetMeta），文案固定 "Model not loaded, please call New interface first"。
- `INVALID_ARGUMENT`：不支持的模型名（New 时 CreateModel 返回空）。
- `INTERNAL`：StoreLoadedModel 失败、InitTrack 类型非 ostrack、SafeRpc 捕获的未预期异常（"Unexpected error in RPC handling: ..."）。
- `DetectResponse.error_message`（field 2）是响应内业务错误，必须与 transport status 区分处理。
- 模型不存在 vs 未检测到目标是两个不同状态：前者 NOT_FOUND，后者空 `results` + OK。
- `Close` 幂等 no-op，不代表资源释放；资源释放发生在进程退出或 replay 的 RemoveModel。

### 4.3 构建产物

- CMake ≥3.10，C++17，`BVTOOLCHAIN` 未设置直接 FATAL；默认 `RelWithDebInfo`；`Debug` 带 ASan+UBSan 倾向的 `-fsanitize=address`。
- 链接目录 `libs/${CMAKE_SYSTEM_PROCESSOR}`（x86_64/aarch64 私有推理库随仓库发布）；依赖 gRPC/GStreamer/OpenCV/TensorRT/cuDNN/yaml-cpp 由工具链文件注入路径。
- 测试：`include(CTest)`；`test_model/latency_benchmark_policy_test.cpp` 为 gtest 单测（辅助函数级）；`--test-model` 为加载/推理集成验证；真实 ground SDK 测速失败必须非零退出（不得在报告中显示 [OK]/N/A 通过）。

## 5. 审查高风险边界

- 同步 RPC 与长流 reader/sender 的阻塞操作会放大处理线程占用；评估新增磁盘/设备 I/O 的时延与取消边界。
- `osd_feedback_lost_` 直出原帧是背压保护，修改 OSD 闭环逻辑必须同步验证该路径。
- `Close` no-op、`GetModelRealName` 查不到原样返回（拼写错误会静默落到错误模型名）——错误处理与名称解析是审查重点。
- `SharedMutex` 保护的 ModelManager：Detect 热路径持锁语义变化会影响吞吐。
- 帧队列深度 3 + 丢帧策略、detect 批采集 `AsyncDetectContext` 状态机（kIdle/kCapturing/kInferring/interrupted）是时序敏感区。
- TensorRT/CUDA 资源只在本进程管理；新增模型类必须实现 Close 并在析构中调用；replay/退出路径注意 `cudaErrorCudartUnloading`。
- proto 变更必须同时比较 gvs 侧 proto、重新生成双端 stub、披露滚动升级顺序。
- `--test-model`/trtexec 测速：版本探测（TRT 主版本）、ground SDK 失败退出码、物理引擎延迟不得复用他人结果，均为规则强制项。


## 6. AI 审查集成与核验边界

[项目说明](../GO_AI_PR_Review.md) 与 [技术框架](../技术框架层级说明.md) 描述审查系统。本文只提供推理服务的版本化背景，不代表线上验收。源码引用均相对 infer 仓库根目录；跨仓库判断应同时标注两端 SHA。

2026-10-13 已从本地 Git 对象核对 11 个 RPC（含 GetMeta）、6 个模型实现文件组、5 类 ModelType、Close no-op，以及两端 proto 漂移。其余详细时序、运行时依赖和性能相关描述沿用此 SHA 的既有理解，变更审查时必须回到对应文件核验，不作为新的全仓审计或性能测量结果。

AI_IR_REPLAY 的 AgentsFor 已补 src/models、src/core、src/service、streamer、test_model 的 infer 专项路径，以及 C++ 测试文件和 tests 目录。并发、资源角色参与所有包，数据流角色参与这些 infer 路径；具体执行仍受角色开关和轮次预算约束。路径分配测试只验证角色接入，不代表模型检出率。反思器仅对 .go 文件的建议执行 Go 语法校验，C++ 示例保留但未经语法或编译验证。

全局理解依赖 understanding 开关及来源 SHA；持久化更新只读访问目标 Codeup 仓库并写 AI_IR_REPLAY 本地文档。失败时 strict=false 使用带旧 SHA 的快照（若有），strict=true 阻断。当前来源仓库隔离测试失败，必须独立配置知识路径并人工核对仓库标记，不能只凭 SHA 认定知识来源正确。跨仓库约束应在实际变更中给出协议、调用方和部署证据。知识首次初始化与更新范围见 [关键设计](review-design.md)，运行验收见 [部署与验收](deployment-and-acceptance.md)。本次只同步审查系统集成说明，未重新核验 infer 业务源码，不改变顶部 SOURCE-SHA。
