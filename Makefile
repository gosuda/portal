.PHONY: help fmt vet lint test tidy build release clean

.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)

help:
	@echo "Available targets:"
	@echo "  make build       - Build relay-server and portal-tunnel for current platform"
	@echo "  make build-all   - Build all binaries for all platforms (linux/darwin/windows, amd64/arm64)"
	@echo "  make release     - Create release archives in dist/"
	@echo "  make test        - Run tests"
	@echo "  make lint        - Run linter"
	@echo "  make fmt         - Format code"
	@echo "  make clean       - Remove build artifacts"
	@echo "  make run         - Run relay server locally"

fmt:
	@gofmt -w .
	@echo "Formatted"

vet:
	@go vet ./...
	@echo "Vet passed"

lint:
	@golangci-lint run

test:
	@go test -v -race ./...

tidy:
	@go mod tidy
	@go mod verify

build:
	@mkdir -p bin
	@echo "Building relay-server..."
	@CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/relay-server ./cmd/relay-server
	@echo "Building portal-tunnel..."
	@CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/portal-tunnel ./cmd/portal-tunnel
	@echo "Build complete: bin/"

build-all:
	@mkdir -p dist
	@echo "Building for all platforms..."
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			EXT=""; \
			if [ "$$os" = "windows" ]; then EXT=".exe"; fi; \
			echo "  $$os/$$arch..."; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "dist/relay-server-$$os-$$arch$$EXT" ./cmd/relay-server; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o "dist/portal-tunnel-$$os-$$arch$$EXT" ./cmd/portal-tunnel; \
		done; \
	done
	@echo "All builds complete: dist/"

release: clean build-all
	@echo "Creating release archives..."
	@mkdir -p dist/release
	@for os in linux darwin windows; do \
		for arch in amd64 arm64; do \
			EXT=""; \
			if [ "$$os" = "windows" ]; then EXT=".exe"; fi; \
			DIR="portal-$(VERSION)-$$os-$$arch"; \
			mkdir -p "dist/release/$$DIR"; \
			cp "dist/relay-server-$$os-$$arch$$EXT" "dist/release/$$DIR/relay-server$$EXT"; \
			cp "dist/portal-tunnel-$$os-$$arch$$EXT" "dist/release/$$DIR/portal-tunnel$$EXT"; \
			cp README.md LICENSE "dist/release/$$DIR/"; \
			if [ "$$os" = "windows" ]; then \
				(cd dist/release && zip -q "$$DIR.zip" "$$DIR"/*); \
			else \
				(cd dist/release && tar -czf "$$DIR.tar.gz" "$$DIR"); \
			fi; \
			rm -rf "dist/release/$$DIR"; \
		done; \
	done
	@echo "Release archives created: dist/release/"

run: build
	@./bin/relay-server

clean:
	@rm -rf bin dist
	@echo "Cleaned"
