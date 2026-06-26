# kenko-nvr AI 智能分析设计文档：目标检测 / 人脸识别 / 车牌识别

> 状态：设计草案（design draft）
> 适用范围：在保持 `CGO_ENABLED=0` 纯 Go 单二进制的前提下，为 kenko-nvr 引入 AI 目标检测（object detection）、人脸识别（face recognition）、车牌识别（license plate recognition / LPR）。
> 核心结论：**把"大脑"放到进程外，"管线"留在纯 Go 里**——通过 HTTP/gRPC 调用一个独立的检测 sidecar 进行推理，kenko 负责抽帧管线、运动门控（motion-gating）、事件/时间线集成与前端展示。owl（Go + 外部 Python gRPC 检测器、运动门控）是与本项目最贴近、且已被验证的范本。

---

## 1. 背景与硬约束

kenko-nvr 是一个纯 Go 的 NVR。在设计 AI 能力时，下列约束**必须主导每一个技术选择**，不能动摇：

| 约束 | 含义 | 对 AI 设计的影响 |
| --- | --- | --- |
| **`CGO_ENABLED=0`，静态编译，零 C 依赖** | 这是项目的硬性规则。二进制必须能 CGO-free 构建。 | 真正的推理运行时（TFLite、ONNX Runtime、OpenCV DNN、TensorRT、Coral EdgeTPU）几乎都要 cgo 或本地动态库。**不能把推理塞进 Go 进程内。** |
| **已经在 `os/exec` 外部进程 ffmpeg** | on-demand 直播转码、运动检测都靠外部 ffmpeg 进程完成。 | "纯 Go" 的边界是：**二进制 CGO-free 即可，调用外部程序是被允许的**。AI 推理作为外部 RPC 服务完全符合既定方向。 |
| **每路相机是一个 `core.Stream` 发布/订阅中枢** | 单次拉流，多消费者 fan-out；满缓冲的 reader 丢帧而非阻塞 ingest。 | 抽帧给检测器只需再 `AddReader` 一个消费者，不需要二次拉流。 |
| **已有 `internal/tsfeed`** | 把 `core.Stream` 序列化成 MPEG-TS，复用于转码/运动/重推流。 | 给检测 sidecar 喂帧时，可复用同一条 "TS → ffmpeg → 像素" 管线，无需在 Go 里解码。 |
| **已有运动检测 + 事件 + 时间线 + 通知** | `internal/motion` 产出事件、写时间线、触发通知与事件录制。 | AI 检测可以**复用整套事件/通知/录制基础设施**，只是把事件从 "motion" 扩展为带标签/边框的 "object/face/plate"。 |

> 这份内存笔记（`media-pipeline-direction`）已经明确定调：
> *"Go owns 'move bytes / speak protocol / manage sessions'；ffmpeg owns 'touch pixels/samples'。**AI inference is an external RPC service.**"*
> 本设计正是这条边界规则在 AI 领域的延伸。

### 1.1 既有管线的关键事实（设计的落脚点）

下面这些是从代码里读出来的、设计必须对齐的事实：

- **运动检测如何抽帧**（`internal/motion/motion.go`）：把 `core.Stream` 经 `tsfeed.Feed` 喂进 ffmpeg `stdin`，ffmpeg 用 `-vf fps=4,scale=64:36,format=gray -f rawvideo pipe:1` 输出极小的灰度裸帧，Go 侧 `io.ReadFull` 逐帧读出后在纯 Go 里做像素差分（`changeRatio`）。**这条 "TS-in → ffmpeg → rawvideo-out → 纯 Go 读帧" 的范式，就是 AI 抽帧要复用的模板**——只要把输出从 `gray 64x36` 换成 `bgr24`/JPEG 的检测分辨率即可。
- **运动事件如何落库与通知**（`internal/manager/runtime.go`）：`motion.Detector` 暴露 `OnStart(t)` / `OnEnd(t, score)` 两个回调；`onMotionStart` 调 `db.Events.Create(Event{Type: EventMotion, ...})` 并 `notifier.Notify(...)`，`onMotionEnd` 调 `db.Events.Finalize(id, end, score)`。
- **事件模型**（`internal/database/models.go`、`event_store.go`）：当前 `Event` 只有 `ID/CameraID/Type/StartTime/EndTime/Score/CreatedAt`，`EventType` 目前只有 `EventMotion` 一种。AI 需要在此之上扩展 `Label`、`Score`、边框、缩略图、子标签（人名/车牌）等字段。
- **通知**（`internal/notify/notify.go`）：`Notifier` 全部传输（SMTP/webhook/MQTT/WebPush）都是纯 Go；`Notification.Kind` 现在是 `"motion" | "offline"`，可直接加 `"object"`/`"face"`/`"plate"`。

---

## 2. kenko-nvr 的设计选项与取舍

下面逐一评估可行路径，并诚实标注它们对 "CGO-free 单二进制" 这一核心属性的影响。

### 选项 (a)：外部检测器 sidecar，经 HTTP/gRPC 调用 ✅ 推荐

kenko 抽帧（JPEG 或裸 BGR）后通过网络发给一个独立进程，由 sidecar 跑模型并回传检测结果。范本：Frigate 的 `zmq_ipc`/Deepstack detector、CodeProject.AI、DeepStack、go2rtc 的外部 NVR 思路，以及**最贴近的 owl（Go 主程 + Python gRPC 检测服务）**。

- **优点**
  - **完美保住 CGO-free 单二进制**：所有 cgo/本地库（onnxruntime、tflite、CUDA、OpenVINO、Coral）都被隔离在 sidecar 里，kenko 自己一行 cgo 都不碰。
  - sidecar 可用任意技术栈（Python + ONNX Runtime / Ultralytics 最成熟），可独立升级模型、独立选择硬件加速。
  - 与既有架构同构：sidecar 之于 AI ≈ ffmpeg 之于转码，都是 "外部 leaf 变换"。
  - 天然支持多种部署：本机子进程、同主机另一个容器、甚至远程 GPU 主机。
- **缺点**
  - 多一个要部署/运维的进程；需要定义并维护一套 RPC 契约。
  - 跨进程传帧有序列化/拷贝开销（用运动门控 + 低帧率 + JPEG 压缩可把带宽压到很低）。
  - 端到端延迟略高于进程内推理（对 NVR 的 "事件级" 检测而言完全可接受，不是直播低延迟场景）。
- **结论**：**这是唯一能在不破坏硬约束的前提下提供真正 AI 能力的方案，选它。**

### 选项 (b)：纯 Go 的 ONNX/推理运行时（必须如实评估）

社区常被问到 "能不能纯 Go 跑模型"。如实结论：**绝大多数都不是真正 CGO-free 的，无法满足约束。**

| 方案 | 是否 CGO-free | 说明 |
| --- | --- | --- |
| `yalue/onnxruntime_go` | ❌ **不是** | 是对官方 onnxruntime C API 的 cgo 绑定，需链接 `libonnxruntime`。违反硬约束。 |
| `gomlx`（含 XLA 后端） | ❌ 通常不是 | 依赖 XLA/PJRT C++ 后端（cgo + 本地库）；纯 Go backend 不具备生产级算子覆盖与性能。 |
| `wasm` 化的 onnxruntime（经 `wazero` 纯 Go WASM runtime 跑 ORT-web/wasm） | ⚠️ 理论 CGO-free | `wazero` 本身纯 Go，无 cgo。但把 onnxruntime-web 的 wasm 跑起来体积大、SIMD/线程受限、性能远逊原生，且工程化（模型加载、算子）成本高。**可作为远期实验，不作为 v1 主路径。** |
| `gorgonia` / 纯 Go 张量库 | ⚠️ CGO-free 但不实用 | 无法直接加载主流 YOLO/ArcFace 等预训练模型，需手工搭网络，工程不现实。 |
| TFLite 的纯 Go 实现 | ❌ 不存在生产级 | 官方 tflite 走 C；社区无可用纯 Go 移植。 |

> **小结**：在 2026 年的现实里，"纯 Go 进程内跑生产级目标检测" 还不成立。坚持 CGO-free 就必须把推理外置。这恰好印证了选项 (a)。

### 选项 (c)：复用既有 ffmpeg 抽帧管线喂检测器 ✅（与 (a) 组合）

kenko 已经能用 ffmpeg 从 `core.Stream` 拿到解码后的帧（运动检测就这么干）。**复用这条管线给检测器抽帧，Go 侧无需任何解码（也就无需 cgo 解码库）。** 具体见 §6 的 `Detector` 设计——抽帧器把输出从 "gray 64x36" 改成 "检测分辨率的 JPEG/BGR"，按运动门控的节奏取关键帧，发给 sidecar。这是 (a) 的天然组成部分，不是独立路线。

### 选项 (d)：go2rtc 风格 / 委托给外部 NVR

把检测整体外包给一个外部 NVR（如让 Frigate 跑检测、kenko 只做录制/管理），或仿 go2rtc 把流交给外部组件。
- **优点**：零自研推理。
- **缺点**：等于把产品的核心价值让渡出去，且两套事件/时间线/录制系统割裂，集成体验差。**不符合 "kenko 自己拥有 AI 事件闭环" 的产品目标，仅作对比，不推荐。**

### 选项 (e)：随附自带运行时的伴生子进程 / 可选容器

把 sidecar 打包成一个**可选的官方伴生容器/子进程**（自带 Python + onnxruntime + 模型），用户开启 AI 时才启动。
- 这其实是 (a) 的**部署形态**而非独立方案：协议仍是 HTTP/gRPC，区别只是 "我们顺手提供一个开箱即用的检测器镜像"。
- **优点**：用户零配置即可获得 AI；与 Frigate 的 "一个镜像全包" 体验接近。
- **缺点**：kenko 主二进制的 "单文件" 卖点在 AI 开启时被削弱（多了一个容器）。可接受——**核心二进制依旧 CGO-free，AI 是可选增量**。

### 2.1 选项决策小结

```
进程内推理（cgo）         ✗ 破坏硬约束，淘汰
纯 Go 运行时 (b)         ✗ 现实中无生产级 CGO-free 方案
外部 sidecar over RPC (a) ✓ 推荐：唯一既满足约束又能力完整
  + 复用 ffmpeg 抽帧 (c)  ✓ 组合进 (a)
  + 自带可选容器 (e)      ✓ (a) 的开箱即用部署形态
委托外部 NVR (d)          ✗ 让渡核心价值，仅对比
```

**对 "CGO-free 单二进制" 保全度最高的方案：进程外检测器 + kenko 提供抽帧管线/运动门控/事件集成/UI。大脑外置，管线纯 Go。**

---

## 3. 人脸识别与车牌识别的定位

一个关键工程事实：**人脸识别和车牌识别通常不是独立的第一阶段检测，而是建立在目标/人脸检测之上的后处理器（post-processor）。** 典型两阶段（甚至三阶段）流水线：

- **人脸识别**：目标检测先框出 `person` → 人脸检测器在人体框内找 `face` → 人脸对齐 → 嵌入模型（ArcFace/FaceNet）算出向量 → 与底库比对得出人名（作为事件的 `sub_label`）。
- **车牌识别（LPR）**：目标检测先框出 `car`/`truck` → 车牌检测（如 YOLO 小模型）框出车牌 → OCR（PaddleOCR 系）读出字符 → 正则/格式校验 → 车牌号作为 `sub_label`。

这意味着 kenko **不需要为人脸/车牌单独造管线**：它们是 detector sidecar 内部（或第二个专用 sidecar）的能力，kenko 只需在事件模型里容纳 `sub_label`/识别结果字段，并在 UI 上展示。提供这些能力的成熟外部服务：

| 能力 | 可用外部服务 / 技术 |
| --- | --- |
| 人脸识别 | **CompreFace**（自建 REST 服务）、**CodeProject.AI**、**DeepStack**、**InsightFace/ArcFace**（自托管）、dlib（HOG/CNN）、Frigate 内置 FaceNet(small)/ArcFace(large) |
| 车牌识别（LPR） | **PaddleOCR**（Frigate 的 LPR 即 YOLOv9 车牌检测 + PaddleOCR 三段式）、**CodeProject.AI ALPR**（`/v1/image/alpr`）、各类自建 OCR |

> 设计取向：v1 先做**目标检测**打通整条事件链路；人脸/车牌在 v3 作为 "可识别对象的后处理" 接入——既可以让同一个 sidecar 在检测到 `person`/`car` 后追加识别，也可以把识别做成 kenko 调用的第二个外部服务（CompreFace/CodeProject.AI）。kenko 侧的协议把这一层抽象成 "对某个裁剪图做识别，返回标签 + 置信度"。

---

## 4. 同类项目检测架构评估（基于实际阅读仓库）

下面六个仓库均已在本地克隆并实读其 README/docs/config/源码，逐一总结其检测架构。

### 4.1 Frigate（`tmp/frigate`）

- **语言/运行时**：Python 3.13+；推理用 ONNX Runtime 与 TensorFlow Lite；API 用 FastAPI。
- **检测器架构**：**进程内**为主——每类 detector 在 Frigate 自己派生的独立 Python 进程里跑（`multiprocessing.Queue` 通信），统一实现 `DetectionApi.detect_raw(tensor)`（`frigate/detectors/detection_api.py`）。**例外**：`zmq_ipc.py` 经 ZeroMQ REQ/REP 把推理外包给独立进程；`deepstack.py` 调远程 Deepstack HTTP。
- **detector 插件**（`frigate/detectors/plugins/`）：EdgeTPU(Coral)、TensorRT(Jetson/NVIDIA)、OpenVINO(Intel)、ONNX(自动选 CUDA/ROCm/OpenVINO)、CPU TFLite(兜底)、Hailo8/8L、RKNN(Rockchip)、DeGirum、Memryx、AXEngine、Synaptics、ZMQ-IPC、Deepstack。**硬件加速 = 多 detector 插件，各自管理 GPU/NPU 生命周期，无统一 HW 抽象层。**
- **默认模型**：新装默认 OpenVINO 版 **SSDLite MobileNet v2**（COCO 91 类）；也支持 YOLOv8/v9、YOLOx、YOLO-NAS、D-FINE、RF-DETR；推荐输入 320×320（靠运动区域裁剪+放大喂检测）。
- **运动门控**：**是。** OpenCV 运动检测（`frigate/motion/frigate_motion.py`，帧差阈值默认 30 + 轮廓面积过滤 + 运动掩膜）先产出 "运动框"，**目标检测只在运动框裁剪并放大后的区域上跑**，不是全帧推理——既省算力又对小目标更敏感。
- **人脸识别**：**支持。** FaceNet(small, TFLite, 128 维)/ArcFace(large, ONNX, 512 维)；含人脸检测 + LBF 关键点对齐；阈值/底库可配（最多每人 200 张训练图）。
- **车牌识别**：**支持，确认用 PaddleOCR。** YOLOv9 车牌检测（small/large ONNX）→ PaddleOCR 三段式（检测/方向分类/识别），结果写入 `recognized_license_plate` 或匹配为 `sub_label`。
- **事件/UI**：检测→Norfair/质心追踪生成 `TrackedObject`→区域(zone, 含 inertia/逗留/测速)/掩膜/对象过滤→写入 Peewee+SQLite 的 `Event` 表（`box/zones/sub_label/has_clip` 等 JSON 字段），MQTT 推 `frigate/events`；前端按 label/sub_label/zone/置信度过滤并叠加边框。

### 4.2 Viseron（`tmp/viseron`）

- **语言/运行时**：Python 3.10+；多进程/子进程 worker；SQLAlchemy ORM（默认 PostgreSQL，也支持 SQLite）。
- **检测器架构**：**组件化（component）混搭**，既有进程内也有外部服务：
  - 进程内：**Darknet/YOLO**（`components/darknet/`，OpenCV DNN 或原生 Darknet，默认 YOLOv3）、**EdgeTPU/Coral**（`components/edgetpu/`，TFLite，TPU 用 MobileDet、CPU 用 EfficientDet-Lite3）、**YOLO**（`components/yolo/`，需指定模型与设备）、**Hailo**（`components/hailo/`）、**dlib**（人脸）。
  - 外部 HTTP 服务（sidecar）：**CodeProject.AI**（`components/codeprojectai/`，默认端口 32168，默认模型 `ipcam-general`）、**DeepStack**（`components/deepstack/`）。
- **运动门控**：**可选。** 每个 detector 可配 `scan_on_motion_only: true`，此时依赖运动域（MOG2 背景减除）触发；否则全帧/常跑。
- **人脸识别**：**支持，多后端**：DeepStack、CodeProject.AI、**CompreFace**（`components/compreface/`，REST，含检测/相似度阈值）、**dlib**（HOG/CNN，CUDA 可选）。在检测出的 `person` 上触发。
- **车牌识别（LPR）**：**支持，专走 CodeProject.AI**（`components/codeprojectai/license_plate_recognition.py`，端点 `/v1/image/alpr`），在检测出的 `car` 上触发。
- **事件/UI**：检测发 `EVENT_OBJECT_DETECTOR_RESULT`→后处理器监听 FOV/Zone 事件；label 级配置 `trigger_event_recording`/`store`/`require_motion`/`store_interval`；区域/掩膜/对象尺寸过滤；落 SQLAlchemy。

### 4.3 DeepCamera（`tmp/DeepCamera`）

- **语言/运行时**：Python 3.9+（ultralytics/torch）；早期有 Docker CLI 封装（sharpai-hub）。
- **目标检测**：**YOLO26**（2026.01 模型，nano/small/medium/large，COCO 80+ 类）；自动加速到 TensorRT(NVIDIA)/CoreML(Apple Silicon)/OpenVINO(Intel)/ONNX(AMD/CPU)。
- **人脸识别**：**InsightFace(ArcFace)** + MTCNN 人脸检测；嵌入 + SVM 分类（含不均衡数据上采样）。
- **ReID**：**有。** YOLOv7-ReID + FastReID 做人员重识别（跨摄像头/跨时间）。
- **边缘取向**：**强。** 面向边缘设备（Jetson Nano/Xavier、RPi4、甚至 ESP32-CAM），强调 CPU 兜底、无需商业 GPU。
- **进程内/sidecar**：**可插拔 "skill" 架构（sidecar 模式）**——桌面 App 经 JSONL stdin/stdout 协议 IPC 调用各检测 skill，多管线并行。
- **运动门控**：以帧率治理为主（skill 默认约 5 FPS）；按 skill 配置。
- **LPR**：路线图中（📐），开源版尚未提供。

### 4.4 owl（`tmp/owl`）⭐ 最贴近 kenko 的范本

owl 是一个 **Go 的 GB28181 视频平台**，把 AI 推理委托给一个**独立的 Python gRPC 服务**——**这正是 "Go 主程 + 外部 Python 检测器经 gRPC" 的现成模板，对 kenko 最有参考价值。**

- **双进程设计**
  - Go 进程（平台，端口 15123）：HTTP API、GB28181 SIP、媒体集成、事件库。
  - Python 进程（AI 微服务，端口 50051）：gRPC 服务端、YOLO(ONNX/TFLite) 检测、从 RTSP 抽帧、运动门控。
- **控制面（Go → Python）= gRPC**（`tmp/owl/protos/analysis.proto`）。服务与关键消息：

  ```protobuf
  service AnalysisService {
    rpc StartCamera(StartCameraRequest) returns (StartCameraResponse);
    rpc StopCamera(StopCameraRequest) returns (StopCameraResponse);
    rpc GetStatus(StatusRequest) returns (StatusResponse);
  }

  message StartCameraRequest {
    string camera_id = 1;
    string camera_name = 2;
    string rtsp_url = 3;                 // sidecar 自己去拉这路 RTSP
    float  detect_interval_seconds = 4;  // 抽帧/检测间隔，默认 2.0s
    repeated string labels = 5;          // 要检测的标签，如 ["person","car"]
    float  threshold = 6;                // 置信度阈值，默认 0.5
    repeated AnalysisZone zones = 7;     // 多边形 ROI
    int32  retry_limit = 8;
    float  alert_cooldown_seconds = 9;   // 告警去重冷却，默认 30s
    string callback_url = 10;            // 检测结果回调地址
    string callback_secret = 11;         // 回调签名密钥
  }

  message AnalysisZone {
    repeated float points = 1;           // 归一化多边形坐标 [x1,y1,x2,y2,...]
    repeated string labels = 2;          // 该区域专属标签
    string name = 3;
  }

  message StartCameraResponse {
    bool  success = 1;
    string message = 2;
    int32 source_width = 3;
    int32 source_height = 4;
    float source_fps = 5;
  }
  ```

  Go 客户端见 `tmp/owl/internal/rpc/rpc.go`（`AIClient` 包装 `AnalysisServiceClient`，insecure gRPC 连 `localhost:50051`）。
- **数据面（Python → Go）= HTTP POST 回调**（异步、带重试/退避）。Python 把检测结果 POST 到 `http://127.0.0.1:15123/ai/events`，JSON 形如：

  ```json
  {
    "camera_id": "camera-001",
    "timestamp": 1719432000000,
    "detections": [
      { "label": "person", "confidence": 0.95,
        "box": {"x_min":100,"y_min":200,"x_max":500,"y_max":800},
        "norm_box": {"x":0.3,"y":0.5,"w":0.4,"h":0.6}, "area": 280000 }
    ],
    "snapshot": "<base64 JPEG，已画框>",
    "snapshot_width": 1920, "snapshot_height": 1080
  }
  ```

  Go 侧（`tmp/owl/internal/web/api/webhook_event.go`）解码快照存盘，并对每个 detection 调 `eventCore.AddEventAndNotify(... Label, Score, Zones, ImagePath ...)` 落库+通知。
- **抽帧方式**：**Python sidecar 自己用 ffmpeg 拉 RTSP** 解码——`ffmpeg -rtsp_transport tcp -i <rtsp> -f rawvideo -pix_fmt bgr24 -r <fps> pipe:1`，按 `detect_interval_seconds` 控制帧率（队列长度 1，只留最新帧）。RTSP URL 由 Go 媒体服务提供（`rtsp://127.0.0.1:<port>/rtp/<channel>`）。
- **运动门控**：**有。** sidecar 内置背景减除 + 轮廓面积过滤（默认最小 500 像素）+ 多边形区域掩膜；只有检测到运动才跑 YOLO 推理。告警再经 IoU>0.3 + 冷却去重。
- **模型**：默认 ONNX（YOLO11/v8），也支持 TFLite（YOLO 单输出或 SSD 多输出）；按优先级在 `configs/` 下查找 `owl.onnx`/`owl.tflite`。**硬件加速**：当前 ONNX 走 CPUExecutionProvider（4 intra-op 线程），TFLite 支持 uint8 量化。
- **人脸/LPR**：**无。** 仅 COCO 目标检测。

> **对 kenko 的启示**：owl 证明了 "Go 控制面用 gRPC 下发 start/stop/zones/labels、数据面用 HTTP 回调 events（含 base64 快照）、sidecar 内部做抽帧+运动门控+去重" 这套组合在 Go NVR 上是可落地且自洽的。kenko 唯一要改进的点是抽帧来源——owl 让 sidecar 二次拉 RTSP，而 **kenko 可以直接复用 `core.Stream`（单次拉流）把帧推给 sidecar，避免第二次拉流**。

### 4.5 SentryShot（`tmp/sentryshot`）

- **语言/运行时**：Rust 1.85+；CDylib 插件（dlopen/libloading）架构。
- **目标检测**：**TFLite 模型**（插件 `plugins/object_detection_tflite/`），引用了专门的 CCTV 模型；有 Coral/Edge TPU 时经 `ai-edge-litert`+`libedgetpu` 委托，否则 CPU 兜底。
- **进程内/sidecar**：**进程内插件**——Rust cdylib 与核心一起编译、运行时 `libloading` 加载。
- **运动门控**：**有。** 存在运动插件（`plugins/motion/`），按条件触发检测。
- **人脸/ReID/LPR**：**开源版均无。**
- **边缘取向**：明确面向 EdgeTPU/Coral USB，足迹轻、适合嵌入式 Linux。

### 4.6 ZLMediaKit（`tmp/ZLMediaKit`）

- **语言**：C++11；RTSP/RTMP/HLS/WebRTC 流媒体服务器。
- **开源版 AI**：**没有。** OSS 核心是纯媒体传输，源码树无任何 AI 目录。
- **闭源专业版 AI**：YOLO 推理插件（人/车检测、目标跟踪、多边形电子围栏、OCR），加速走 TensorRT(CUDA)/ONNXRuntime/Ascend CANN——**但这属于需联系厂商的付费闭源版**。
- **人脸/ReID**：OSS 与文档化的专业版均无。
- **结论**：媒体服务器范畴，**AI 不在开源核心里，仅付费版**。对 kenko 的参考价值在于 "媒体内核与 AI 解耦" 这一点本身。

### 4.7 同类项目检测架构对比表

| 项目 | 语言 | 检测运行时 | 进程内 vs Sidecar | 硬件加速 | 运动门控 | 人脸识别 | 车牌(LPR) | ReID |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **Frigate** | Python | ONNX RT / TFLite | 进程内多进程（+ZMQ-IPC/Deepstack 外部） | Coral/TensorRT/OpenVINO/ONNX(CUDA/ROCm)/Hailo/RKNN… | ✅ OpenCV 运动框裁剪 | ✅ FaceNet/ArcFace | ✅ YOLOv9 + **PaddleOCR** | ✗ |
| **Viseron** | Python | Darknet/TFLite/PyTorch + 外部服务 | 混搭（Darknet/EdgeTPU/dlib 进程内；CodeProject.AI/DeepStack 外部） | Coral/CUDA/OpenVINO/Hailo | ✅ 可选 `scan_on_motion_only` | ✅ CompreFace/DeepStack/CodeProject.AI/dlib | ✅ CodeProject.AI ALPR | ✗ |
| **DeepCamera** | Python | YOLO26 + InsightFace | sidecar(skill, JSONL IPC) | TensorRT/CoreML/OpenVINO/ONNX | ⚠️ 帧率治理 | ✅ InsightFace/ArcFace | 📐 路线图 | ✅ FastReID |
| **owl** ⭐ | **Go + Python** | YOLO ONNX/TFLite | **外部 Python gRPC sidecar** | CPU(可扩) | ✅ 背景减除 + IoU 去重 | ✗ | ✗ | ✗ |
| **SentryShot** | Rust | TFLite | 进程内 cdylib 插件 | Edge TPU/CPU | ✅ 运动插件 | ✗ | ✗ | ✗ |
| **ZLMediaKit** | C++ | （仅付费版）TensorRT/ONNX | OSS 无 AI | （付费版）CUDA/Ascend | — | ✗ | （付费版 OCR） | ✗ |
| **kenko-nvr（本设计）** | **Go(CGO-free) + sidecar** | sidecar: ONNX RT(Python) | **外部 sidecar over HTTP/gRPC** | sidecar 内自管(CPU/CUDA/OpenVINO/Coral) | ✅ 复用既有像素运动门控 | ✅(v3，CompreFace/CodeProject.AI/sidecar 内置) | ✅(v3，PaddleOCR/CodeProject.AI) | 远期 |

> 横向看：**纯 Go/Rust 系（owl、SentryShot）都没有把生产级推理塞进主二进制**——owl 干脆外置成 Python 进程，SentryShot 用 cdylib（Rust 可以 FFI，但 kenko 的 CGO-free 约束更严，等价于 owl 的外置路线）。这从侧面印证：**对一个坚持 CGO-free 的 Go NVR 而言，外置检测器是被同类项目反复选择的、正确的工程方向。**

---

## 5. kenko-nvr 的推荐方案与分阶段路线图

**总原则**：大脑外置（sidecar 跑模型，CGO-free 不受影响），管线纯 Go（kenko 提供 `core.Stream` 抽帧、运动门控、`Detector` 抽象、事件/时间线/通知/录制集成、前端边框与过滤）。控制面用 gRPC，数据面（结果回传）走同一条 gRPC 流式响应或 HTTP 回调——见 §5.2。

### 5.1 分阶段路线图

| 阶段 | 目标 | 交付内容 |
| --- | --- | --- |
| **Phase 0 · 抽帧基建** | 在 Go 侧从 `core.Stream` 抽出检测分辨率的帧 | 新增 `internal/aiframe`：复用 `tsfeed.Feed` → ffmpeg（`-vf fps=N,scale=W:H -f mjpeg pipe:1` 或 `bgr24 rawvideo`）→ Go 读出 JPEG/BGR 帧；可按 "仅关键帧/限频" 取帧。**完全复用 motion.go 的 TS-in→ffmpeg→读帧范式。** |
| **Phase 1 · 运动门控的目标检测（MVP）** | 只在像素运动触发时跑模型，省 GPU/CPU | (1) `Detector` 接口 + **HTTP 实现**（先 HTTP，最简单）；(2) 运动门控：复用 `motion.Detector` 的 `OnStart`，运动期间才抽帧送检；(3) 事件落库：扩展 `Event` 增加 `Label/SubLabel/Score/BBox/Thumbnail`，新增 `EventObject` 类型；(4) 通知接 `Kind:"object"`，可按对象类型过滤。 |
| **Phase 2 · gRPC + 区域/掩膜 + 对象过滤录制** | 生产化协议与精细控制 | (1) `Detector` 增加 **gRPC 实现**（双向流，控制+结果）；(2) 区域(zone)/掩膜(mask) 配置（多边形，下发给 sidecar 或在 Go 侧做点-在-多边形过滤）；(3) 对象过滤的事件录制——只有出现 `person`/`car` 等指定标签才触发事件录制（接 `RecordMode:"motion"` 的同款机制）；(4) 同对象 IoU+冷却去重。 |
| **Phase 3 · 人脸识别 + 车牌识别** | 在目标/人脸检测之上做识别后处理 | (1) `Recognizer` 接口（对裁剪图返回标签+置信度+sub_label）；(2) 人脸：sidecar 内置或调 **CompreFace/CodeProject.AI**；车牌：**PaddleOCR/CodeProject.AI ALPR**；(3) 底库管理（人名/已知车牌）；(4) 事件 `SubLabel` 落库；(5) 前端按人名/车牌过滤与展示。 |
| **Phase 4 · 前端与体验** | 把检测呈现给用户 | (1) 直播叠加边框（结果经 WS 推前端实时绘制）；(2) 时间线/事件页按 `label/sub_label/zone/置信度` 过滤；(3) 事件缩略图（sidecar 回传的已画框 JPEG）；(4) 每相机 AI 开关与模型/标签/区域设置卡片（仿 `recordingSettingsCard`）。 |
| **Phase 5（远期，可选）** | 加速与高级能力 | sidecar 硬件加速档位（CUDA/OpenVINO/Coral/TensorRT）；多 sidecar 负载分配；ReID/跨相机检索；官方 detector 镜像（开箱即用部署形态，见选项 (e)）。 |

### 5.2 Sidecar 协议选择

**推荐：gRPC 作为主协议（控制 + 结果双向流），同时提供 HTTP/JSON 兼容回退。**

- **为什么 gRPC 优于纯 HTTP 回调（owl 用的是 gRPC 控制 + HTTP 回调的混合）**
  - 单条**双向流**就能同时承载 "下发配置/帧" 与 "回传检测结果"，避免 owl 那样 sidecar 反向 POST 回 Go（少一个 Go 侧入站 webhook 端点、少一套回调签名）。
  - protobuf 强类型契约、低开销、易演进（owl 的 `.proto` 即现成范式，可直接借鉴消息定义）。
  - 流式天然适合 "运动期间持续推帧、持续回结果"。
- **两种抽帧拓扑**（二选一或都支持）
  1. **kenko 推帧（推荐，单次拉流）**：kenko 用 Phase 0 的 `aiframe` 把 JPEG 帧经 gRPC 流 `SendFrame` 给 sidecar，sidecar 推理后在同一流回 `Detections`。优点：**不二次拉流**，与 `core.Stream` 单次拉流原则一致；缺点：帧走网络（JPEG 压缩 + 运动门控 + 低帧率后带宽很低）。
  2. **sidecar 自拉（owl 式）**：kenko 只下发 `rtsp_url + labels + zones + interval`，sidecar 自己 ffmpeg 拉流。优点：实现最简单、Go 侧零抽帧；缺点：**二次拉流**（与本项目 "单次拉流" 取向相悖），且要把内部 RTSP 暴露给 sidecar。
  - **结论**：v1 可先按 owl 式（拓扑 2）快速打通，**目标态收敛到拓扑 1（kenko 推帧）**以贯彻单次拉流原则。

### 5.3 Go `Detector` 接口草图（伪代码）

放在新包 `internal/detect`。先有 HTTP 实现，再加 gRPC 实现；运动门控在调用方（manager 运行时）控制。

```go
package detect

import (
    "context"
    "image"
    "time"
)

// Box 是归一化坐标（0..1），与 core.Stream 分辨率无关，便于前端按原图缩放绘制。
type Box struct {
    X, Y, W, H float64
}

// Detection 是一帧里的一个检测结果。
type Detection struct {
    Label    string  // "person" / "car" / "face" / "license_plate" ...
    Score    float64 // 0..1
    Box      Box
    SubLabel string  // 识别后处理填充：人名 / 车牌号；初检为空
    Track    string  // 可选：同一物体的跟踪 ID，用于去重/路径
}

// Frame 是送检的一帧及其元数据。Pixels 由 internal/aiframe 从 core.Stream 抽出。
type Frame struct {
    CameraID  string
    Time      time.Time
    JPEG      []byte      // 优先：sidecar 直接吃 JPEG；二选一
    Image     image.Image // 备选：已解码帧
    SrcW, SrcH int
}

// Detector 是可插拔的检测后端。HTTP 实现先行，gRPC 实现随后；
// 任何实现都在 kenko 进程之外完成推理，从而保住 CGO-free。
type Detector interface {
    // Detect 对单帧推理，返回该帧的检测结果。
    Detect(ctx context.Context, f Frame, opts Options) ([]Detection, error)
    // Healthy 供管理面探活与降级。
    Healthy(ctx context.Context) bool
    Close() error
}

// Options 是按相机/按调用的检测参数（标签白名单、阈值、区域）。
type Options struct {
    Labels    []string  // 只关心这些标签；空=全部
    Threshold float64   // 置信度下限
    Zones     []Zone    // 多边形 ROI（归一化坐标）；Go 侧或 sidecar 侧过滤
}

type Zone struct {
    Name   string
    Points []float64 // [x1,y1,x2,y2,...] 归一化
    Labels []string  // 该区域专属标签
}

// Recognizer 是 Phase 3 的后处理：对裁剪图（人脸/车牌）做识别。
// 它同样在进程外执行（sidecar 内置或 CompreFace/CodeProject.AI/PaddleOCR）。
type Recognizer interface {
    // Recognize 接收一个裁剪区域，返回 sub_label（人名/车牌号）与置信度。
    Recognize(ctx context.Context, crop Frame, kind string) (subLabel string, score float64, err error)
}
```

**HTTP 实现骨架**（v1）：

```go
type HTTPDetector struct {
    Endpoint string        // e.g. http://127.0.0.1:8770/v1/detect
    Client   *http.Client  // 纯 Go net/http，零 cgo
    Secret   string
}

func (d *HTTPDetector) Detect(ctx context.Context, f Frame, opts Options) ([]Detection, error) {
    // multipart：JPEG 帧 + JSON(opts)；或 application/json with base64。
    // POST -> 解析 sidecar 返回的 detections（归一化 box + label + score）。
    // 全程 net/http + encoding/json，纯 Go。
}
```

**与 manager 运行时的接线**（复用既有 motion 回调，做运动门控）：

```go
// 在 camRuntime 里，AI 检测器只在运动活跃时抽帧送检：
det := &motion.Detector{
    Source:  stream,
    OnStart: rt.onMotionStart,   // 既有：落 motion 事件 + 通知
    OnEnd:   rt.onMotionEnd,
}
// 新增：运动期间，aiframe 以低帧率抽帧 -> detector.Detect -> 结果落 EventObject。
go rt.runAIDetection(ctx, stream)  // 内部 gate on rt.motionIsActive()
```

### 5.4 默认模型建议

| 用途 | 默认模型 | 理由 |
| --- | --- | --- |
| 目标检测 | **YOLO（YOLOv8/YOLO11 nano/small，ONNX）**，COCO 80 类 | 精度/速度均衡，ONNX Runtime 生态最成熟；与 owl、DeepCamera 一致。CPU 可跑 nano，有 GPU 升 small/medium。 |
| 轻量兜底 | SSDLite MobileNet v2（COCO） | 极低算力设备的兜底（Frigate 默认即此），可作为 sidecar 的 CPU 档。 |
| 人脸检测+嵌入 | SCRFD/RetinaFace + **ArcFace**（或 FaceNet 轻量档） | 与 InsightFace/Frigate 一致，底库比对成熟。 |
| 车牌 | YOLO 车牌检测 + **PaddleOCR** | Frigate 同款；中文车牌 PaddleOCR 支持好。也可整体走 CodeProject.AI ALPR。 |

### 5.5 与既有系统的集成点（落到具体文件）

| 既有系统 | 文件 | AI 集成方式 |
| --- | --- | --- |
| 抽帧 | 新 `internal/aiframe`（仿 `internal/motion/motion.go`） | 复用 `tsfeed.Feed`+ffmpeg，把 `-vf` 输出从 `gray 64x36` 改为检测分辨率 JPEG/BGR；按运动门控限频抽帧。 |
| 运动门控 | `internal/motion/`、`internal/manager/runtime.go` | 复用 `motion.Detector.OnStart/OnEnd` 与 `rt.motionIsActive()`：**仅在运动活跃时调 `Detector.Detect`**，从根上省 GPU/CPU（与 Frigate/owl 一致）。 |
| 事件模型 | `internal/database/models.go`、`event_store.go` | `EventType` 加 `EventObject`/`EventFace`/`EventPlate`；`Event` 增 `Label/SubLabel/BBox(JSON)/Thumbnail` 字段；新增对应迁移（`migrations.go`）。`EventStore` 与 `EventFilter` 支持按 `Type/Label/SubLabel` 过滤（前端时间线/事件页用）。 |
| 录制 | `internal/recording/`、`RecordMode` | 复用 "motion 触发事件录制 + 后摇(post-roll)" 机制，扩展为 "出现指定对象标签才录" 的对象过滤录制。 |
| 通知 | `internal/notify/notify.go` | `Notification.Kind` 加 `"object"/"face"/"plate"`；按对象类型/区域/已知人名命中触发；沿用 per-(camera,kind) 节流。全链路仍纯 Go。 |
| 时间线/前端 | `internal/api/events.go`、`internal/web`（SolidJS） | 事件 API 返回 `label/sub_label/box/thumbnail`；前端时间线/事件页加对象过滤器与边框叠加；直播页经 WS 实时绘制边框；每相机 AI 设置卡片（仿 `recordingSettingsCard`）。 |
| 探活/降级 | `internal/manager` | `Detector.Healthy` 失败时降级为 "仅运动检测"，不影响录制/直播——AI 是可选增量，核心 NVR 不受其拖累。 |

---

## 6. 结论

1. **保住 CGO-free 单二进制的唯一正解，是把推理放到进程外、经 RPC 调用**（选项 a，组合复用 ffmpeg 抽帧 c、可选自带容器 e）。纯 Go 进程内推理（选项 b）在 2026 年仍无生产级 CGO-free 方案——`yalue/onnxruntime_go`、`gomlx` 都依赖 cgo/本地库，wasm 化只能作远期实验。
2. **人脸识别与车牌识别是目标/人脸检测之上的后处理器**，kenko 无需为其单独造管线，只需在事件模型容纳 `sub_label` 并接 CompreFace/CodeProject.AI/PaddleOCR 等外部识别服务。
3. **owl 是与 kenko 最贴近、且已验证的范本**：Go 主程 + 外部 Python gRPC 检测器、运动门控、检测结果回 Go 落库通知。kenko 应采纳其 "gRPC 控制 + 结构化检测结果 + 运动门控 + IoU/冷却去重" 模式，并在抽帧上更进一步——**复用 `core.Stream` 单次拉流推帧给 sidecar，而非像 owl 那样让 sidecar 二次拉 RTSP**。
4. **分阶段落地**：Phase 0 抽帧基建 → Phase 1 运动门控目标检测 MVP（HTTP `Detector`，扩 `Event`，接通知）→ Phase 2 gRPC + 区域/掩膜 + 对象过滤录制 → Phase 3 人脸/车牌识别 → Phase 4 前端边框与过滤 → Phase 5 加速与 ReID。每一阶段，**kenko 主二进制始终 CGO-free，AI 始终是可降级的可选增量**——大脑外置，管线纯 Go。
