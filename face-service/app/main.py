"""Stateless face-analysis sidecar for kenko-nvr.

It exposes one real endpoint, ``POST /analyze``, which takes a batch of JPEG
frames and returns, per frame, the detected faces with a 512-d L2-normalised
ArcFace embedding (plus landmarks, detection score and a sharpness measure the
Go side folds into a quality score). All heavy lifting — SCRFD detection,
5-point alignment and ArcFace recognition — is done by InsightFace on
onnxruntime, CPU-only and fully offline once the model pack is cached.

The service holds no business state: the Go orchestrator owns tracking,
the identity gallery, clustering and storage. Keeping inference here (and only
here) lets the main NVR binary stay CGO-free.

Environment:
  FACE_MODEL_PACK   InsightFace model pack name (default: buffalo_l).
  FACE_DET_SIZE     Detector input square size in px (default: 640).
  FACE_DET_THRESH   Detector score threshold (default: 0.5).
  FACE_PROVIDER     onnxruntime execution provider: "cpu" (default), "coreml"
                    (Apple Neural Engine / GPU — macOS host only, not in a
                    Linux container), "openvino" (Intel CPU), or "auto".
  FACE_THREADS      Best-effort cap on internal threads (OpenCV + BLAS). The
                    hard CPU cap in production is a container quota (--cpus).
  INSIGHTFACE_HOME  Where model packs are cached (default: ~/.insightface).
"""

from __future__ import annotations

import base64
import os
from contextlib import asynccontextmanager
from typing import Optional

# Thread limiting (best-effort): set before importing cv2/numpy so their thread
# pools honour it. The authoritative CPU cap in production is a container quota
# (docker --cpus / compose cpus), which bounds onnxruntime's intra-op pool too;
# FACE_THREADS additionally bounds OpenCV and the numpy/BLAS backends here.
_FACE_THREADS = os.environ.get("FACE_THREADS", "").strip()
if _FACE_THREADS and _FACE_THREADS != "0":
    for _var in (
        "OMP_NUM_THREADS",
        "OPENBLAS_NUM_THREADS",
        "MKL_NUM_THREADS",
        "NUMEXPR_NUM_THREADS",
        "VECLIB_MAXIMUM_THREADS",
    ):
        os.environ.setdefault(_var, _FACE_THREADS)

import platform

import cv2
import numpy as np
import onnxruntime as ort
from fastapi import FastAPI
from pydantic import BaseModel

from insightface.app import FaceAnalysis

MODEL_PACK = os.environ.get("FACE_MODEL_PACK", "buffalo_l")
DET_SIZE = int(os.environ.get("FACE_DET_SIZE", "640"))
DET_THRESH = float(os.environ.get("FACE_DET_THRESH", "0.5"))
PROVIDER = os.environ.get("FACE_PROVIDER", "cpu").lower()

if _FACE_THREADS and _FACE_THREADS != "0":
    try:
        cv2.setNumThreads(int(_FACE_THREADS))
    except Exception:
        pass

_state: dict = {}


def _providers() -> list[str]:
    """Resolve the onnxruntime execution providers from FACE_PROVIDER, only
    selecting an accelerator that is actually available (else plain CPU).

    - "coreml"/"metal": Apple CoreML EP — uses the Neural Engine / GPU (Metal).
      Only available when running natively on macOS; a Linux container (incl.
      Docker on a Mac) does not have it and falls back to CPU.
    - "openvino": Intel CPU acceleration (needs the openvino extra).
    - "auto": CoreML on macOS, otherwise CPU.
    """
    avail = set(ort.get_available_providers())
    want = PROVIDER
    if want == "auto":
        want = "coreml" if platform.system() == "Darwin" else "cpu"
    if want in ("openvino", "ov") and "OpenVINOExecutionProvider" in avail:
        return ["OpenVINOExecutionProvider", "CPUExecutionProvider"]
    if want in ("coreml", "metal", "mps") and "CoreMLExecutionProvider" in avail:
        return ["CoreMLExecutionProvider", "CPUExecutionProvider"]
    return ["CPUExecutionProvider"]


def _load() -> FaceAnalysis:
    fa = FaceAnalysis(name=MODEL_PACK, providers=_providers())
    # ctx_id=-1 forces CPU. det_size is the detector's working resolution.
    fa.prepare(ctx_id=-1, det_size=(DET_SIZE, DET_SIZE), det_thresh=DET_THRESH)
    return fa


@asynccontextmanager
async def lifespan(_app: FastAPI):
    fa = _load()
    _state["fa"] = fa
    _state["model"] = MODEL_PACK
    # ArcFace recognition output is 512-d for every shipped pack; read it off the
    # loaded recognition model so a different pack reports its true dimension.
    dim = 512
    rec = fa.models.get("recognition") if hasattr(fa, "models") else None
    if rec is not None and getattr(rec, "output_shape", None):
        try:
            dim = int(rec.output_shape[-1])
        except (TypeError, ValueError, IndexError):
            dim = 512
    _state["dim"] = dim
    yield
    _state.clear()


app = FastAPI(title="kenko-nvr face-service", lifespan=lifespan)


class FrameIn(BaseModel):
    id: int
    image_b64: str


class AnalyzeIn(BaseModel):
    frames: list[FrameIn]
    # Optional per-request override: drop faces whose min(width,height) in pixels
    # is below this. The Go side usually applies its own size gate too.
    min_face: Optional[int] = None


@app.get("/healthz")
def healthz():
    ready = "fa" in _state
    return {
        "status": "ok" if ready else "starting",
        "model": _state.get("model", MODEL_PACK),
        "dim": _state.get("dim", 512),
        "provider": _providers()[0],
        "det_size": DET_SIZE,
        "det_thresh": DET_THRESH,
        "threads": _FACE_THREADS or "all",
    }


def _sharpness(crop: np.ndarray) -> float:
    """Variance of the Laplacian — a cheap blur measure (higher = sharper)."""
    if crop is None or crop.size == 0:
        return 0.0
    gray = cv2.cvtColor(crop, cv2.COLOR_BGR2GRAY)
    return float(cv2.Laplacian(gray, cv2.CV_64F).var())


@app.post("/analyze")
def analyze(req: AnalyzeIn):
    fa: FaceAnalysis = _state["fa"]
    frames_out = []
    for fr in req.frames:
        faces_out = []
        try:
            raw = base64.b64decode(fr.image_b64)
            arr = np.frombuffer(raw, np.uint8)
            img = cv2.imdecode(arr, cv2.IMREAD_COLOR)
        except Exception:
            img = None
        if img is not None:
            try:
                for f in fa.get(img):
                    x1, y1, x2, y2 = (float(v) for v in f.bbox)
                    w = max(0.0, x2 - x1)
                    h = max(0.0, y2 - y1)
                    if req.min_face and min(w, h) < req.min_face:
                        continue
                    crop = img[max(0, int(y1)):int(y2), max(0, int(x1)):int(x2)]
                    kps = (
                        [[float(p[0]), float(p[1])] for p in f.kps]
                        if f.kps is not None
                        else []
                    )
                    pose = None
                    if getattr(f, "pose", None) is not None:
                        pose = [float(v) for v in f.pose]
                    faces_out.append({
                        "bbox": [x1, y1, x2, y2],
                        "det_score": float(f.det_score),
                        "kps": kps,
                        "embedding": f.normed_embedding.astype(np.float32).tolist(),
                        "sharpness": _sharpness(crop),
                        "pose": pose,
                    })
            except Exception:
                faces_out = []
        frames_out.append({"id": fr.id, "faces": faces_out})
    return {"frames": frames_out, "model": _state["model"], "dim": _state["dim"]}
