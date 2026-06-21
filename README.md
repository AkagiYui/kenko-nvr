# Kenko NVR

一个用 **纯 Go（无 CGO）** 实现的网络视频录像机（NVR）。支持 ONVIF 控制、RTSP/RTMP 接入、
录像与自定义分文件、滚动删除与存储阈值、录像上传 S3（支持 HTTP 代理），并自带 Web 管理 / 监看前端。

> 构建约束：`CGO_ENABLED=0`。SQLite 使用纯 Go 驱动 `modernc.org/sqlite`，整个程序可静态编译、零 C 依赖。

---

## 功能特性

| 能力 | 说明 |
| --- | --- |
| **RTSP 拉流** | 连接 IP 摄像头 RTSP 地址，支持 H.264 / H.265 视频 + AAC 音频，TCP/UDP/自动传输，断线自动重连（指数退避）。 |
| **RTMP 推流接入** | 内置 RTMP 服务器，编码器/摄像头推流到 `rtmp://<host>:1935/live/<摄像头ID>` 即可接入（H.264 + AAC）。 |
| **ONVIF** | 局域网 WS-Discovery 设备发现；探测设备 Profile 并自动获取 RTSP 地址；云台 PTZ 控制（连续移动 / 停止 / 绝对移动 / 预置位）。 |
| **录像** | 写入分片 MP4（fMP4），可自定义**单文件时长**与**文件命名规则**（占位符模板）。 |
| **滚动删除** | 按最大保留天数、最大总容量、最小剩余磁盘空间三种阈值自动清理最旧录像；可限定“仅删除已上传 S3 的录像”。 |
| **S3 上传** | 完成的录像自动上传到 S3 兼容存储，**支持配置 HTTP/HTTPS 代理**；可上传后删除本地文件。 |
| **Web 前端** | 实时监看（HLS，低延迟）、摄像头管理、云台控制、录像回放/下载、系统设置。前端内嵌进二进制（`go:embed`）。 |
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

- RTSP：`github.com/bluenviron/gortsplib/v5`
- fMP4 / HLS：`github.com/bluenviron/mediacommon/v2` + `github.com/bluenviron/gohlslib/v2`
- ONVIF：`github.com/use-go/onvif`
- RTMP：`github.com/yutopp/go-rtmp`
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

### 录像与分文件
系统设置 → 录像设置：
- **单文件时长（秒）**：每个录像文件的目标时长（按关键帧切分，保证每个文件以关键帧开头）。
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
GET/PUT /api/settings/{retention|s3|recording}
POST   /api/settings/s3/test            测试 S3 连接
GET    /api/cameras/{id}/hls/index.m3u8 实时 HLS
GET    /api/ws                          状态推送 (WebSocket)
```

---

## 项目结构

```
cmd/nvr            程序入口、装配各组件、信号优雅退出
internal/core      媒体抽象：Track / Unit / Stream（发布订阅中枢）/ Source 接口
internal/rtsp      RTSP 拉流源
internal/rtmp      RTMP 推流接入服务器 + FLV 解复用
internal/recording fMP4 分段录像、命名规则、滚动删除/存储阈值
internal/hls       HLS 实时监看
internal/onvif     设备发现、Profile/StreamURI、PTZ
internal/storage   S3 上传（含代理）与上传 worker
internal/database  SQLite 持久化（cameras / recordings / settings）
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

本版本是首个可用成品，以下为已知边界（便于后续迭代）：

1. **RTMP 仅支持推流接入**：上游库 `go-rtmp` 的客户端不提供 play 拉流 API，故未实现“作为播放端从 RTMP 源拉流”。RTSP 拉流已完整支持。
2. **RTMP 接入仅 H.264 + AAC**：增强型 RTMP（H.265 FourCC）暂未支持；RTSP 路径支持 H.265。
3. **A/V 同步**：录像按各轨道首样本归零时间线，极端情况下可能有不超过一个分片的音视频偏移；后续可改为基于公共 NTP 原点对齐。
4. 鉴权为内存态 Bearer Token（重启需重新登录），单管理员账户。
5. 暂无 HTTPS（建议置于反向代理之后）。

---

## License

MIT
