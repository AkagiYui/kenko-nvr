# Pure-Go build: CGO is disabled everywhere so the binary has no C dependencies
# (modernc.org/sqlite is a CGO-free SQLite driver).
export CGO_ENABLED := 0

BINARY := kenko-nvr
PKG    := ./cmd/nvr
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build frontend run test test-race vet fmt clean tidy linux

all: vet test build

# Build the SolidJS frontend into internal/web/dist (embedded by the Go binary).
# Requires Node + pnpm (corepack enable). Run this before build/linux to refresh
# the embedded UI.
frontend:
	cd frontend && pnpm install --frozen-lockfile && pnpm build

build: frontend
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

run: build
	./$(BINARY) -config config.yaml

test:
	go test ./...

# The race detector needs CGO; this is test-only and does not affect the
# pure-Go production binary.
test-race:
	CGO_ENABLED=1 go test -race ./internal/core/... ./internal/recording/... ./internal/database/...

vet:
	go vet ./...

fmt:
	gofmt -w internal cmd

tidy:
	go mod tidy

# Cross-compile a fully static Linux amd64 binary.
linux: frontend
	GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-linux-amd64 $(PKG)

clean:
	rm -f $(BINARY) $(BINARY)-linux-amd64
