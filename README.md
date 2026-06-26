# Kenko NVR

一个用 **纯 Go（无 CGO）** 实现的网络视频录像机（NVR）。支持 ONVIF 控制、RTSP/RTMP 接入、
录像与自定义分文件、滚动删除与存储阈值、录像上传 S3（支持 HTTP 代理），并自带 Web 管理 / 监看前端。
此外支持**移动侦测与事件录像、时间轴回放、多用户 RBAC、告警通知（邮件/Webhook/MQTT/Web Push）、
多协议对外再分发（RTSP/HTTP-FLV/WS-FLV/HTTP-TS）、WebRTC 低延迟监看与双向语音对讲**。

> 构建约束：`CGO_ENABLED=0`。SQLite 使用纯 Go 驱动 `modernc.org/sqlite`，整个程序可静态编译、零 C 依赖。

---

## 功能特性

| 能力 | 说明 |
| --- | --- |
| **RTSP 拉流** | 连接 IP 摄像头 RTSP 地址，支持 H.264 / H.265 视频 + AAC 音频，TCP/UDP/自动传输，断线自动重连（指数退避）。 |
| **RTMP 推流接入** | 内置 RTMP 服务器，编码器/摄像头推流到 `rtmp://<host>:1935/live/<摄像头ID>` 即可接入（H.264 + AAC）。 |
| **ONVIF 视频源 + 控制** | 来源类型选 ONVIF 即可：连接时自动通过 `GetStreamUri` 解析 RTSP 地址并拉流（每次重连重新解析），同时提供云台 PTZ。也支持局域网 WS-Discovery 发现与一次性“探测”填充。 |
| **录像** | 写入分片 MP4（fMP4），可自定义**单文件时长**与**文件命名规则**（占位符模板）；支持**按整点对齐切片**（如 10 分钟分段从 :00 :10 :20 起）。可选 **FFmpeg 转码录像**（统一为 H.264/H.265，需系统装有 `ffmpeg`，缺失时自动回退为流拷贝）。 |
| **滚动删除** | 按最大保留天数、最大总容量、最小剩余磁盘空间三种阈值自动清理最旧录像；可限定“仅删除已上传 S3 的录像”。 |
| **S3 上传** | 完成的录像自动上传到 S3 兼容存储，**支持配置 HTTP/HTTPS 代理**；可上传后删除本地文件。 |
| **Web 前端** | 实时监看（**单连接 WebSocket + MSE 推流 fMP4**，低延迟；不支持时回退 HLS）、摄像头管理、云台控制、录像回放/下载、系统设置。前端内嵌进二进制（`go:embed`）。 |
| **直播按需转码** | 非 H.264 摄像头（如 H.265）做浏览器监看时，**按需启动一份 FFmpeg 转码为 H.264**，多人观看共享同一份转码，无人观看自动停止；**录像始终保存原始码流**，转码只用于直播。H.264 摄像头直接分发不转码。 |
| **硬件加速自发现** | 转码编码器**启动时自动探测**（macOS VideoToolbox / Linux NVENC·QSV·VAAPI·V4L2 / Windows NVENC·QSV·AMF·MF），逐个实测、择优选用，失败回退软件 libx264。部署者无需配置，同一 CGO-free 二进制跨平台自适应。 |
| **移动侦测 + 事件录像** | 用一份轻量 FFmpeg 把流降采样成小灰度帧，在 **纯 Go** 中做帧差侦测移动（无需 CGO 解码）。可按摄像头设灵敏度；产生**事件**写入时间轴并可触发告警；录像模式可选**持续**或**移动事件触发**（仅在有移动时录像，带后摇）。 |
| **录像时间轴回放** | 录像页提供 **24 小时时间轴**：录像覆盖段 + 移动事件标记，点击任意时刻**跳转播放**到对应文件的偏移；保留按日筛选与列表/下载/删除。 |
| **多用户 / RBAC** | 数据库用户（**bcrypt** 哈希），三种角色 **管理员 / 操作员 / 访客**；按角色限制 REST 接口与前端操作；首个管理员从配置播种；不能删除/降级最后一个管理员。 |
| **告警通知** | 移动/离线事件可通过 **邮件（SMTP）、Webhook、MQTT、浏览器 Web Push（VAPID）** 推送；按摄像头+类型节流；前端可配置并**一键测试**、订阅当前浏览器推送。全部纯 Go。 |
| **多协议转封装再分发** | 把任一在线摄像头按标准协议**对外拉流**：**RTSP**（`rtsp://host:8554/<id>`）、**HTTP-FLV**、**WebSocket-FLV**、**HTTP-TS**。供 VLC/ffmpeg/flv.js/其它 NVR 接入；非 H.264 走 FLV 时按需转码。 |
| **WebRTC 直播** | 浏览器监看可切换 **WebRTC（WHEP）** 获得更低延迟（H.264 视频，按需 H.265→H.264 转码）；MSE/HLS 仍为默认与回退。 |
| **双向语音对讲** | 浏览器采集麦克风 → 降采样 8kHz → 经 WebSocket 送到服务端 → 转 **G.711** 经 RTSP/ONVIF **back channel** 喊话到摄像头（需设备支持 back channel；需 HTTPS 或 localhost 才能取麦克风）。 |
| **数据库** | SQLite（纯 Go），WAL 模式。 |

---

## 架构

```
                      ┌──────────────────────────────────────────────┐
   RTSP camera ─pull─▶│ rtsp.Source ─┐                                │
   encoder ────push──▶│ rtmp.Server ─┤                                │
                      │              ▼                                │
                      │        core.Stream  (发布/订阅中枢, 丢帧不阻塞)  │
                      │         │        │                            │
                      │         ▼        ▼                            │
                      │  recording.Recorder   hls.Muxer               │
                      │   (fMP4 分段录像)     (浏览器低延迟监看)         │
                      └──────────────────────────────────────────────┘
   manager.Manager 监督每路摄像头的生命周期与重连；database 持久化；
   retention 滚动删除；storage 上传 S3；api 提供 REST/WebSocket 与静态前端。
```

每路 `core.Stream` 是一个发布/订阅中枢：源（RTSP/RTMP）把媒体单元写入，消费者（录像、HLS）订阅。
写入对慢消费者非阻塞（缓冲满则丢帧并计数），保证录像/监看不会拖垮接入。

### 技术栈（全部纯 Go）

- RTSP（拉流 + 服务端再发布 + back channel 对讲）：`github.com/bluenviron/gortsplib/v5`
- fMP4 / HLS / MPEG-TS / FLV / G.711：`github.com/bluenviron/mediacommon/v2` + `github.com/bluenviron/gohlslib/v2`
- ONVIF：`github.com/use-go/onvif`
- RTMP：`github.com/yutopp/go-rtmp`
- WebRTC：`github.com/pion/webrtc/v4`
- MQTT：`github.com/eclipse/paho.mqtt.golang`
- Web Push（VAPID）：`github.com/SherClockHolmes/webpush-go`
- 密码哈希：`golang.org/x/crypto/bcrypt`
- S3：`github.com/minio/minio-go/v7`
- SQLite：`modernc.org/sqlite`
- Web：`github.com/go-chi/chi/v5` + `github.com/gorilla/websocket` + 内嵌 `hls.js`

---

## 快速开始

```bash
# 1. 构建（纯 Go，无 CGO）
make build           # 等价于 CGO_ENABLED=0 go build -o kenko-nvr ./cmd/nvr

# 2. 准备配置
cp config.example.yaml config.yaml   # 按需修改端口、登录口令、存储路径

# 3. 运行
./kenko-nvr -config config.yaml
```

打开 `http://localhost:8080`，默认账号 `admin / admin`（请在 `config.yaml` 中修改）。

Docker：

```bash
docker build -t kenko-nvr .
docker run -p 8080:8080 -p 1935:1935 -v $PWD/data:/app/data -v $PWD/config.yaml:/app/config.yaml kenko-nvr
```

---

## 使用说明

### 添加 RTSP 摄像头
摄像头管理 → 添加 → 来源类型选 **RTSP 拉流** → 填写 `rtsp://...` 地址与账号密码。
若设备支持 ONVIF，可勾选“启用 ONVIF 控制”，填写设备地址后点“探测 ONVIF”自动获取 RTSP 地址与 Profile。

### 添加 ONVIF 摄像头（自动获取视频流 + 云台）
摄像头管理 → 添加 → 来源类型选 **ONVIF** → 仅需填写 ONVIF 设备地址（`host:port`）与账号密码，
（可选）指定 Profile Token，留空则自动用第一个。系统在连接时通过 ONVIF `GetStreamUri` 解析出
RTSP 地址再拉流，并自动启用云台控制。

> 说明：ONVIF 协议本身（SOAP/HTTP）不传输视频，它负责发现/配置/PTZ 并**告诉你 RTSP 地址**；
> 真正的视频走 RTSP(RTP)。本系统的 ONVIF 来源即“用 ONVIF 解析地址 + 用 RTSP 拉流 + 用 ONVIF 控制”。

### 接入 RTMP 推流
摄像头管理 → 添加 → 来源类型选 **RTMP 推流**。保存后用编码器推流到：

```
rtmp://<本机IP>:1935/live/<摄像头ID>
```

（流名即摄像头 ID，可在列表中查看。）例如用 ffmpeg 推测试流：

```bash
ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=25 -f lavfi -i sine=frequency=440 \
  -c:v libx264 -profile:v baseline -g 25 -c:a aac -ar 44100 \
  -f flv rtmp://127.0.0.1:1935/live/<摄像头ID>
```

### 云台（PTZ）
对启用了 ONVIF 的摄像头，实时监看卡片下方会出现方向/缩放按钮（按下移动、松开停止）。

### 直播转码与硬件加速（浏览器兼容）
浏览器普遍不能播放 H.265。当一路摄像头的视频不是 H.264 时，系统会在**有人打开监看**时
启动**一份** FFmpeg 把它转码成 H.264 再分发；**同一路的多个观看者共享这一份转码**，最后一个
观看者离开后短暂宽限即停止。H.264 摄像头则原样分发、完全不转码。**录像始终保存摄像头的原始
码流（不重编码）**，转码只服务于直播。

编码器**无需手动配置**：进程启动时按平台顺序逐个“实测”候选硬件编码器（真正编码几帧到 `null`），
选用第一个可用的，全部失败则回退软件 `libx264`。因此同一个 CGO-free 二进制可在不同机器上自动用上
各自的硬件加速：

| 平台 | 候选（按序探测） |
| --- | --- |
| macOS | `h264_videotoolbox` → libx264 |
| Linux | `h264_nvenc` → `h264_qsv` → `h264_vaapi` → `h264_v4l2m2m` → libx264 |
| Windows | `h264_nvenc` → `h264_qsv` → `h264_amf` → `h264_mf` → libx264 |

在 `config.yaml` 的 `transcode` 段可选地覆盖：`hwaccel: auto`（默认，自动探测）/ `none`（强制软件）/
指定编码器名；并可调 `live_bitrate_kbps`、`live_gop`。

> 注意：FFmpeg 转码依赖系统已安装 `ffmpeg`；未安装时非 H.264 摄像头将原样分发（交给浏览器尽力播放）。
> 在 macOS 上用 Docker 运行无法访问 VideoToolbox，需以原生二进制运行才能用上硬件编码。

### 录像与分文件
系统设置 → 录像设置：
- **单文件时长（秒）**：每个录像文件的目标时长（按关键帧切分，保证每个文件以关键帧开头）。
- **按整点对齐切片**：开启后分段在墙钟整点边界切割（如 10 分钟分段从 :00 :10 :20 起，便于归档检索）；
  流拷贝模式下切点落在边界后的第一个关键帧。
- **转码录像**（可选，需 `ffmpeg`）：用 FFmpeg 重新编码再切片，可把录像统一成浏览器通用的 **H.264**（或 H.265），
  并可设置 CRF 质量与编码预设。更耗 CPU；未安装 ffmpeg 时自动回退为流拷贝。
- **文件命名规则**：支持占位符 `{camera} {camera_id} {year} {month} {day} {hour} {minute} {second} {unix}`，
  例如 `{camera}/{year}-{month}-{day}/{camera}_{hour}{minute}{second}.mp4`。

### 滚动删除 / 存储阈值
系统设置 → 存储保留策略：可同时设置最大保留天数、最大总容量(GB)、最小剩余磁盘空间(GB)；
勾选“仅删除已上传到 S3 的录像”可避免误删未备份数据。后台每分钟执行一次。

### 上传 S3（含代理）
系统设置 → S3 录像上传：填写 Endpoint / Region / Bucket / Key、AccessKey / SecretKey；
**HTTP 代理**字段可填 `http://user:pass@proxy:3128`，所有 S3 流量将经此代理（HTTPS 自动用 CONNECT 隧道）。
可“测试连接”，也可设置“上传后删除本地文件”。后台每 30 秒扫描未上传的已完成录像并上传。

---

## REST API（简要）

所有 `/api/*`（除 `/api/login`）需 `Authorization: Bearer <token>`；媒体与 WebSocket 端点也接受 `?token=`。

```
POST   /api/login                      {username,password} -> {token}
GET    /api/cameras                     摄像头列表（含实时状态）
POST   /api/cameras                     新增
PUT    /api/cameras/{id}                修改
DELETE /api/cameras/{id}                删除
GET    /api/status                      所有摄像头实时状态
POST   /api/cameras/{id}/ptz            {action: move|stop|absolute|preset, pan,tilt,zoom,presetToken}
GET    /api/cameras/{id}/ptz/presets    预置位列表
GET    /api/onvif/discover              局域网发现 ONVIF 设备
POST   /api/onvif/probe                 {xaddr,username,password} -> profiles + RTSP 地址
GET    /api/recordings?cameraId=&from=&to=&limit=
GET    /api/recordings/{id}/file        播放/下载（支持 Range）
DELETE /api/recordings/{id}
GET    /api/events?cameraId=&from=&to=  移动事件（时间轴）
GET/PUT /api/settings/{retention|s3|recording|notifications}   (admin)
POST   /api/settings/s3/test            测试 S3 连接
POST   /api/settings/notifications/test 发送测试通知
GET    /api/me                          当前用户与角色
POST   /api/logout                      注销当前 token
GET/POST/PUT/DELETE /api/users[/{id}]   用户管理 (admin)
GET    /api/notifications/vapid         Web Push 公钥
POST   /api/notifications/subscribe     注册浏览器推送
GET    /api/cameras/{id}/hls/index.m3u8 实时 HLS
GET    /api/cameras/{id}/mse            实时 MSE（fMP4 over WS）
POST   /api/cameras/{id}/webrtc         WebRTC WHEP（SDP offer→answer）
GET    /api/cameras/{id}/flv            HTTP-FLV 拉流
GET    /api/cameras/{id}/flv.ws         WebSocket-FLV 拉流
GET    /api/cameras/{id}/ts             HTTP-TS 拉流
GET    /api/cameras/{id}/talk           双向对讲 (WebSocket, operator)
GET    /api/ws                          状态推送 (WebSocket)
```

> 角色：`viewer` 仅可查看；`operator` 另可管理摄像头、PTZ、对讲；`admin` 另可管理用户与系统设置。
> 外部 RTSP 拉流走独立端口 `:8554`（`rtsp://host:8554/<cameraID>`）。

---

## 项目结构

```
cmd/nvr            程序入口、装配各组件、信号优雅退出
internal/core      媒体抽象：Track / Unit / Stream（发布订阅中枢）/ Source 接口
internal/rtsp      RTSP 拉流源
internal/rtmp      RTMP 推流接入服务器 + FLV 解复用
internal/recording fMP4 分段录像、命名规则、滚动删除/存储阈值、移动事件门控
internal/hls       HLS 实时监看
internal/mse       fMP4-over-WebSocket（MSE）实时监看
internal/motion    移动侦测（FFmpeg 灰度帧 + 纯 Go 帧差）
internal/tsfeed    把 core.Stream 序列化为 MPEG-TS（供转码/侦测/再发布复用）
internal/restream  对外再发布：HTTP-FLV / WebSocket-FLV
internal/rtspserver 对外 RTSP 再发布服务器
internal/webrtc    WebRTC（WHEP）低延迟监看
internal/backchannel 双向对讲（RTSP/ONVIF back channel，G.711）
internal/notify    告警通知（邮件 / Webhook / MQTT / Web Push）
internal/onvif     设备发现、Profile/StreamURI、PTZ
internal/storage   S3 上传（含代理）与上传 worker
internal/database  SQLite 持久化（cameras / recordings / settings / users / events / push）
internal/manager   控制平面：每路摄像头的监督、重连、消费者装配
internal/api       REST / WebSocket / 内嵌前端
internal/web       内嵌的单页前端（go:embed）
```

---

## 测试

```bash
make test        # 单元测试（config / database / core / recording / onvif / rtmp / storage）
make test-race   # 竞态检测（测试期临时开启 CGO，不影响纯 Go 产物）
```

端到端验证（已实测）：用 ffmpeg 向内置 RTMP 服务器推 H.264+AAC 流，可正确接入、生成可被 ffprobe
正常解码的分段 MP4、按时长切分、提供低延迟 HLS 监看。

---

## v1 已知范围与后续可优化点

本版本在首个可用成品基础上新增了移动侦测/事件录像、时间轴回放、多用户 RBAC、
告警通知、多协议再分发、WebRTC 与双向对讲。以下为已知边界（便于后续迭代）：

1. **对外再发布含 RTSP / HTTP-FLV / WebSocket-FLV / HTTP-TS**；**未提供原生 RTMP 拉流服务**——上游库 `go-rtmp` 仅面向推流接入、服务端 play 写回路径不完整，RTMP 等价载荷请用 HTTP-FLV（同为 H.264/AAC）。RTMP 推流**接入**仍完整支持。
2. **RTMP 接入仅 H.264 + AAC**：增强型 RTMP（H.265 FourCC）暂未支持；RTSP 路径支持 H.265。
3. **WebRTC 仅视频（H.264）**：WebRTC 需 Opus/G.711 音频，而内部音频为 AAC，本版本 WebRTC 不带声音（MSE/HLS 仍带声音）。
4. **双向对讲依赖设备 back channel**：仅对暴露 G.711 back channel 的 RTSP/ONVIF 摄像头有效；浏览器取麦克风需 **HTTPS 或 localhost**。Web Push 同样需要安全上下文。
5. **移动侦测需要 `ffmpeg`**：缺失时移动侦测/事件录像不可用（持续录像不受影响）；侦测为帧差，非 AI 目标检测。
6. **A/V 同步**：录像按各轨道首样本归零时间线，极端情况下可能有不超过一个分片的音视频偏移；后续可改为基于公共 NTP 原点对齐。
7. 鉴权为内存态 Bearer Token（重启需重新登录）；用户/角色已持久化（bcrypt）。
8. 暂无内置 HTTPS（建议置于反向代理之后；WebRTC 媒体走临时 UDP 端口，跨网络需配置 STUN/host 网络）。

---

## License

MIT
