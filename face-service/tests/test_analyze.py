"""Model-correctness checks for the face sidecar.

Uses InsightFace's bundled sample image (a group photo with several distinct
faces), so it is deterministic and offline once the model pack is cached. It
asserts that (1) faces are detected, (2) embeddings are 512-d and unit-norm,
and (3) embeddings are discriminative: a face matches itself with cosine ~1 and
two different people score well below the recognition threshold.
"""

import base64

import cv2
import numpy as np
from fastapi.testclient import TestClient
from insightface.data import get_image

from app.main import app


def _b64(img: np.ndarray) -> str:
    ok, buf = cv2.imencode(".jpg", img)
    assert ok
    return base64.b64encode(buf.tobytes()).decode()


def _cos(a, b):
    a = np.asarray(a, np.float32)
    b = np.asarray(b, np.float32)
    return float(a @ b)


def test_detect_embed_and_discriminate():
    img = get_image("t1")  # group photo bundled with insightface
    with TestClient(app) as client:
        resp = client.post(
            "/analyze", json={"frames": [{"id": 0, "image_b64": _b64(img)}]}
        )
        assert resp.status_code == 200
        faces = resp.json()["frames"][0]["faces"]

        # (1) several faces detected
        assert len(faces) >= 2

        # (2) embeddings are 512-d unit vectors
        for f in faces:
            emb = np.asarray(f["embedding"], np.float32)
            assert emb.shape == (512,)
            assert abs(np.linalg.norm(emb) - 1.0) < 1e-2

        # (3) discriminative: self-match ~1, different people clearly lower
        e0 = faces[0]["embedding"]
        assert _cos(e0, e0) > 0.99
        others = [_cos(e0, f["embedding"]) for f in faces[1:]]
        assert max(others) < 0.5
