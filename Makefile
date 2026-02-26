.PHONY: help fmt vet lint test vuln tidy all run build build-frontend build-tunnel build-server clean

.DEFAULT_GOAL := help

help:
	@echo "Available targets:"
	@echo "  make build             - Build everything (frontend, tunnel, server)"
	@echo "  make build-frontend    - Build React frontend (Tailwind CSS 4)"
	@echo "  make build-server      - Build Go relay server (includes frontend build)"
	@echo "  make run               - Run relay server"
	@echo "  make clean             - Remove build artifacts"

fmt:
	gofmt -w .
	goimports -w .

vet:
	go vet ./...

lint:
	golangci-lint run

test:
	go test -v -race -coverprofile=coverage.out ./...

vuln:
	govulncheck ./...

tidy:
	go mod tidy
	go mod verify

all: fmt vet lint test vuln build

run:
	./bin/relay-server

# Convenience target
build: build-frontend build-tunnel build-server

# Build React frontend with Tailwind CSS 4
build-frontend:
	@echo "[frontend] building React frontend..."
	@mkdir -p cmd/relay-server/dist/app
	@cd cmd/relay-server/frontend && npm i && npm run build
	@echo "[frontend] build complete"

# Build portal-tunnel binaries for distribution
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

# Build Go relay server
build-server:
	@echo "[server] building Go portal..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/relay-server ./cmd/relay-server

clean:
	rm -rf bin
	rm -rf cmd/relay-server/dist/app
	rm -rf cmd/relay-server/dist/tunnel
