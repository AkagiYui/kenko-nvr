# Offline CPU face-analysis sidecar for kenko-nvr.
#
# The InsightFace model pack (buffalo_l: SCRFD detection + ArcFace r50, 512-d)
# is baked into the image at build time, so the running container needs no
# network access — it is fully offline. Override FACE_MODEL_PACK to bake a
# different pack (e.g. antelopev2 for higher accuracy).
FROM python:3.11-slim AS base

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    INSIGHTFACE_HOME=/models \
    FACE_MODEL_PACK=buffalo_l

# build-essential: insightface builds a small Cython extension from sdist.
# ffmpeg/libGL/libglib: OpenCV runtime deps.
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential \
        libgl1 \
        libglib2.0-0 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=ghcr.io/astral-sh/uv:latest /uv /usr/local/bin/uv

WORKDIR /app
COPY pyproject.toml ./
COPY app ./app

# Install the project and its dependencies into the system environment.
RUN uv pip install --system .

# Pre-fetch and cache the model pack into INSIGHTFACE_HOME so the container is
# self-contained and starts without any download.
RUN python -c "from app.main import _load; _load()"

EXPOSE 8077
ENV FACE_DET_SIZE=640 FACE_DET_THRESH=0.5 FACE_PROVIDER=cpu
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8077"]
