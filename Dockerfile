# ---- frontend build stage ----
# Builds the SolidJS UI into internal/web/dist so the Go stage can embed it.
FROM node:24-alpine AS frontend
RUN corepack enable
WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build
# -> /src/internal/web/dist

# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# CGO disabled: pure-Go build, fully static binary.
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Bring in the built frontend (the repo only commits a .gitkeep placeholder).
COPY --from=frontend /src/internal/web/dist ./internal/web/dist
RUN go build -ldflags "-s -w" -o /out/kenko-nvr ./cmd/nvr

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/kenko-nvr /app/kenko-nvr

# 8080: web UI / API / HLS   1935: RTMP ingest
EXPOSE 8080 1935
VOLUME ["/app/data"]

ENTRYPOINT ["/app/kenko-nvr", "-config", "/app/config.yaml"]
