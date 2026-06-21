# ---- build stage ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# CGO disabled: pure-Go build, fully static binary.
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags "-s -w" -o /out/kenko-nvr ./cmd/nvr

# ---- runtime stage ----
FROM gcr.io/distroless/static-debian12
WORKDIR /app
COPY --from=build /out/kenko-nvr /app/kenko-nvr

# 8080: web UI / API / HLS   1935: RTMP ingest
EXPOSE 8080 1935
VOLUME ["/app/data"]

ENTRYPOINT ["/app/kenko-nvr", "-config", "/app/config.yaml"]
