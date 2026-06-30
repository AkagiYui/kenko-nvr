# kenko-nvr face-service

A small, **stateless, CPU-only, offline** inference sidecar. Given a batch of
JPEG frames it returns the faces in each, with a 512-d L2-normalised ArcFace
embedding per face. Everything else — sampling frames from recordings,
tracking, the identity gallery, clustering, corrections and storage — lives in
the Go NVR. Keeping neural inference in this separate process is what lets the
main `kenko-nvr` binary stay CGO-free.

Models (via [InsightFace](https://github.com/deepinsight/insightface), run on
onnxruntime):

| Stage       | Model (default `buffalo_l`) |
|-------------|-----------------------------|
| Detection   | SCRFD-10GF (+ 5-pt landmarks) |
| Alignment   | 5-point similarity → 112×112 |
| Recognition | ArcFace `w600k_r50`, 512-d |

For higher accuracy at more CPU, set `FACE_MODEL_PACK=antelopev2` (ArcFace R100).

## Run (Docker, recommended — fully offline)

The model pack is baked into the image at build time, so the container needs no
network at runtime.

```bash
docker build -t kenko-face-service ./face-service
docker run --rm -p 8077:8077 kenko-face-service
curl localhost:8077/healthz
```

Or with Compose (CPU by default; OpenVINO profile for Intel CPU acceleration):

```bash
cd face-service
docker compose up -d                     # CPU
docker compose --profile openvino up -d  # Intel CPU (OpenVINO, 2-4x)
```

## Run (uv, local dev)

```bash
cd face-service
uv run uvicorn app.main:app --host 127.0.0.1 --port 8077
```

The model pack downloads once into `INSIGHTFACE_HOME` (default `~/.insightface`)
and is cached thereafter. For an air-gapped host, copy that directory across.

## API

`GET /healthz` → `{status, model, dim, provider, det_size, det_thresh}`

`POST /analyze`
```json
{ "frames": [ { "id": 0, "image_b64": "<base64 JPEG>" } ], "min_face": 50 }
```
→
```json
{ "model": "buffalo_l", "dim": 512,
  "frames": [ { "id": 0, "faces": [
    { "bbox": [x1,y1,x2,y2], "det_score": 0.99,
      "kps": [[x,y], ...5], "embedding": [..512..],
      "sharpness": 184.2, "pose": [yaw,pitch,roll] } ] } ] }
```

Embeddings are already L2-normalised, so cosine similarity is a plain dot
product on the Go side.

## Environment

| Var | Default | Meaning |
|-----|---------|---------|
| `FACE_MODEL_PACK` | `buffalo_l` | InsightFace pack name |
| `FACE_DET_SIZE` | `640` | detector input square (px) |
| `FACE_DET_THRESH` | `0.5` | detector score threshold |
| `FACE_PROVIDER` | `cpu` | `cpu` or `openvino` (Intel CPU speedup; needs the `openvino` extra) |
| `FACE_THREADS` | (all) | best-effort cap on OpenCV/BLAS threads |
| `INSIGHTFACE_HOME` | `~/.insightface` | model cache dir |

## Limiting CPU

Inference uses all CPU cores by default (onnxruntime parallelises each detection
+ embedding across the machine), so a scan can saturate the host. The reliable
cap is a **container CPU quota**, which also bounds onnxruntime's thread pool:

```bash
docker run --cpus=2 -p 8077:8077 kenko-face-service   # or `cpus: "2"` in compose
```

You can also reduce total work from the NVR's face settings (lower **sample fps**
/ **max frames per recording**), and co-locate the sidecar with the NVR so the
sampled frames it receives never leave the host/LAN.

## Tests

```bash
uv run --extra dev pytest      # detection + embedding sanity on a bundled sample
```
