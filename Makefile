.PHONY: fmt lint lint-fix test vuln tidy build build-tunnel proto frontend all

FRONTEND_DIR := cmd/relay-server/frontend
BIN_DIR := bin

fmt:
	gofmt -w .
	goimports -w .

lint:
	golangci-lint run

lint-fix:
	golangci-lint run --fix

test:
	go test -v -race -coverprofile=coverage.out ./...

vuln:
	govulncheck ./...

tidy:
	go mod tidy
	go mod verify

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/relay-server ./cmd/relay-server
	go build -o $(BIN_DIR)/portal-tunnel ./cmd/portal-tunnel

build-tunnel:
	@echo "[tunnel] building portal-tunnel binaries..."
	@mkdir -p cmd/relay-server/dist/tunnel
	@for GOOS in linux darwin windows; do \
		for GOARCH in amd64 arm64; do \
			EXT=""; \
			if [ "$${GOOS}" = "windows" ]; then EXT=".exe"; fi; \
			OUT="cmd/relay-server/dist/tunnel/portal-tunnel-$${GOOS}-$${GOARCH}$${EXT}"; \
			echo " - $${OUT}"; \
			CGO_ENABLED=0 GOOS=$${GOOS} GOARCH=$${GOARCH} go build -trimpath -ldflags "-s -w" -o "$${OUT}" ./cmd/portal-tunnel; \
		done; \
	done

proto:
	buf generate
	buf lint

frontend:
	cd $(FRONTEND_DIR) && npm run lint && npm run build

all: fmt lint test vuln build frontend
