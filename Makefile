SHELL := /bin/sh

GO_TOOLCHAIN_ROOT := $(shell go env GOROOT 2>/dev/null)
ifeq ($(strip $(GO_TOOLCHAIN_ROOT)),)
$(error Unable to determine Go toolchain root; ensure Go is installed)
endif
export PATH := $(GO_TOOLCHAIN_ROOT)/bin:$(PATH)

# Force Go to store caches inside the repo so builds succeed in sandboxed envs.
GO_CACHE_DIR := $(CURDIR)/.cache/go-build
GO_MOD_CACHE := $(CURDIR)/.cache/gomod
GO_PATH_DIR := $(CURDIR)/.gopath
export GOCACHE := $(GO_CACHE_DIR)
export GOMODCACHE := $(GO_MOD_CACHE)
export GOPATH := $(GO_PATH_DIR)
$(shell mkdir -p $(GO_CACHE_DIR) $(GO_MOD_CACHE) $(GO_PATH_DIR))

DEFAULT_GOPATH := $(or $(shell env -u GOPATH -u GOMODCACHE go env GOPATH 2>/dev/null),$(HOME)/go)
DEFAULT_GOMODCACHE := $(DEFAULT_GOPATH)/pkg/mod

.PHONY: seed-go-mod-cache
seed-go-mod-cache:
	@mkdir -p "$(GO_MOD_CACHE)"
	@if [ -n "$(DEFAULT_GOMODCACHE)" ] && [ -d "$(DEFAULT_GOMODCACHE)" ]; then \
		awk '{if (NF >= 2) { ver = $$2; sub(/\/go.mod$$/, "", ver); print $$1" "ver; }}' go.sum | sort -u | while read -r module version; do \
			[ -n "$$module" ] || continue; \
			modver="$$module@$$version"; \
			src="$(DEFAULT_GOMODCACHE)/$$modver"; \
			dest="$(GO_MOD_CACHE)/$$modver"; \
			if [ -d "$$src" ] && [ ! -d "$$dest" ]; then \
				echo "[go-cache] seeding $$modver"; \
				mkdir -p "$$(dirname "$$dest")"; \
				cp -a "$$src" "$$dest"; \
			fi; \
			cache_src="$(DEFAULT_GOMODCACHE)/cache/download/$$module/@v"; \
			cache_dest="$(GO_MOD_CACHE)/cache/download/$$module/@v"; \
			if [ -d "$$cache_src" ]; then \
				mkdir -p "$$cache_dest"; \
				for ext in info mod zip ziphash; do \
					src_file="$$cache_src/$$version.$$ext"; \
					dest_file="$$cache_dest/$$version.$$ext"; \
					if [ -f "$$src_file" ] && [ ! -f "$$dest_file" ]; then \
						cp -a "$$src_file" "$$dest_file"; \
					fi; \
				done; \
			fi; \
		done; \
	else \
		echo "[go-cache] default Go module cache not found; relying on network downloads"; \
	fi

.PHONY: help run build build-wasm compress-wasm build-frontend build-tunnel build-server clean

.DEFAULT_GOAL := help

help:
	@echo "Available targets:"
	@echo "  make build             - Build everything (protoc, wasm, frontend, server)"
	@echo "  make build-protoc      - Generate Go code from protobuf definitions"
	@echo "  make build-wasm        - Build and compress WASM client with optimization"
	@echo "  make build-frontend    - Build React frontend (Tailwind CSS 4)"
	@echo "  make build-server      - Build Go relay server (includes frontend build)"
	@echo "  make run               - Run relay server"
	@echo "  make run-report-server - Run the benchmark report server"
	@echo "  make run-wasm-bench    - Run the WASM benchmark server"
	@echo "  make clean             - Remove build artifacts"

run-wasm-bench: build-wasm-bench
	@echo "[server] running WASM benchmark server..."
	go run ./cmd/wasm-bench-server


run-report-server: seed-go-mod-cache
	@echo "[server] running benchmark report server..."
	go run ./cmd/report-server

run:
	./bin/relay-server

# Convenience target
build: build-wasm build-frontend build-tunnel build-server

build-protoc:
	protoc -I . \
		--go_out=. \
		--go_opt=paths=source_relative \
		--go-vtproto_out=. \
		--go-vtproto_opt=paths=source_relative \
		portal/core/proto/rdsec/rdsec.proto \
		portal/core/proto/rdverb/rdverb.proto

# Build WASM artifacts with wasm-opt optimization and generate manifest
build-wasm: seed-go-mod-cache
	@echo "[wasm] building webclient WASM..."
	@mkdir -p cmd/relay-server/dist/wasm
	GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o cmd/relay-server/dist/wasm/portal.wasm ./cmd/webclient
	
	@echo "[wasm] optimizing with wasm-opt..."
	@if command -v wasm-opt >/dev/null 2>&1; then \
		wasm-opt -Oz --enable-bulk-memory cmd/relay-server/dist/wasm/portal.wasm -o cmd/relay-server/dist/wasm/portal.wasm.tmp && \
		mv cmd/relay-server/dist/wasm/portal.wasm.tmp cmd/relay-server/dist/wasm/portal.wasm; \
		echo "[wasm] optimization complete"; \
	else \
		echo "[wasm] WARNING: wasm-opt not found, skipping optimization"; \
		echo "[wasm] Install binaryen for smaller WASM files: brew install binaryen (macOS) or apt-get install binaryen (Linux)"; \
	fi
	
	@echo "[wasm] calculating SHA256 hash..."
	@WASM_HASH=$$(shasum -a 256 cmd/relay-server/dist/wasm/portal.wasm | awk '{print $$1}'); \
	echo "[wasm] SHA256: $$WASM_HASH"; \
	echo "[wasm] cleaning old hash files..."; \
	find cmd/relay-server/dist/wasm -name '[0-9a-f]*.wasm' ! -name "$$WASM_HASH.wasm" -type f -delete 2>/dev/null || true; \
	find cmd/relay-server/dist/wasm -name '[0-9a-f]*.wasm.br' ! -name "$$WASM_HASH.wasm.br" -type f -delete 2>/dev/null || true; \
	cp cmd/relay-server/dist/wasm/portal.wasm cmd/relay-server/dist/wasm/$$WASM_HASH.wasm; \
	rm -f cmd/relay-server/dist/wasm/portal.wasm; \
	echo "[wasm] content-addressed WASM: dist/wasm/$$WASM_HASH.wasm"
	
	@echo "[wasm] copying additional resources..."
	@cp cmd/webclient/wasm_exec.js cmd/relay-server/dist/wasm/wasm_exec.js
	@cp cmd/webclient/service-worker.js cmd/relay-server/dist/wasm/service-worker.js
	@cp cmd/webclient/index.html cmd/relay-server/dist/wasm/portal.html
	@cp cmd/webclient/portal.mp4 cmd/relay-server/dist/wasm/portal.mp4
	@echo "[wasm] build complete"

	@echo "[wasm] precompressing webclient WASM with brotli..."
	@WASM_FILE=$$(ls cmd/relay-server/dist/wasm/[0-9a-f]*.wasm 2>/dev/null | head -n1); \
	if [ -z "$$WASM_FILE" ]; then \
		echo "[wasm] ERROR: no content-addressed WASM found in cmd/relay-server/dist/wasm; run build-wasm first"; \
		exit 1; \
	fi; \
	WASM_HASH=$$(basename "$$WASM_FILE" .wasm); \
	if ! command -v brotli >/dev/null 2>&1; then \
		echo "[wasm] ERROR: brotli not found; install brotli to build compressed WASM"; \
		exit 1; \
	fi; \
	brotli -f "$$WASM_FILE" -o "cmd/relay-server/dist/wasm/$$WASM_HASH.wasm.br"; \
	echo "[wasm] brotli: cmd/relay-server/dist/wasm/$$WASM_HASH.wasm.br"

build-wasm-bench: seed-go-mod-cache
	echo "[wasm-bench] building benchmark WASM..."
	mkdir -p cmd/wasm-bench-server/dist/wasm
	GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o cmd/wasm-bench-server/dist/wasm/bench.wasm ./cmd/wasm-bench-server/ && \
	echo "[wasm-bench] copying wasm_exec.js" && \
	cp cmd/webclient/wasm_exec.js cmd/wasm-bench-server/dist/wasm/ && \
	echo "[wasm-bench] calculating SHA256 hash..." && \
	WASM_HASH=$$(shasum -a 256 cmd/wasm-bench-server/dist/wasm/bench.wasm | awk '{print $$1}') && \
	echo "[wasm-bench] SHA256: $$WASM_HASH" && \
	echo "[wasm-bench] cleaning old hash files..." && \
	find cmd/wasm-bench-server/dist/wasm -name '[0-9a-f]*.wasm' ! -name "$$WASM_HASH.wasm" ! -name 'bench.wasm' -type f -delete && \
	find cmd/wasm-bench-server/dist/wasm -name '[0-9a-f]*.wasm.br' ! -name "$$WASM_HASH.wasm.br" -type f -delete && \
	cp cmd/wasm-bench-server/dist/wasm/bench.wasm cmd/wasm-bench-server/dist/wasm/$$WASM_HASH.wasm && \
	echo "[wasm-bench] content-addressed WASM: dist/wasm/$$WASM_HASH.wasm" && \
	echo "[wasm-bench] precompressing benchmark WASM with brotli..." && \
	WASM_FILE=$$(ls cmd/wasm-bench-server/dist/wasm/$$WASM_HASH.wasm 2>/dev/null | head -n1) && \
	if [ -z "$$WASM_FILE" ]; then \
		echo "[wasm-bench] ERROR: no content-addressed WASM found"; \
		exit 1; \
	fi; \
	if ! command -v brotli >/dev/null 2>&1; then \
		echo "[wasm-bench] ERROR: brotli not found; install brotli to build compressed WASM"; \
		exit 1; \
	fi; \
	brotli -f "$$WASM_FILE" -o "cmd/wasm-bench-server/dist/wasm/$$WASM_HASH.wasm.br" && \
	rm -f "$$WASM_FILE" && \
	rm -f cmd/wasm-bench-server/dist/wasm/bench.wasm && \
	echo "[wasm-bench] brotli: cmd/wasm-bench-server/dist/wasm/$$WASM_HASH.wasm.br"


# Build React frontend with Tailwind CSS 4
build-frontend:
	@echo "[frontend] building React frontend..."
	@mkdir -p cmd/relay-server/dist/app

# Build portal-tunnel binaries for distribution
build-tunnel: seed-go-mod-cache
	@echo "[tunnel] building portal-tunnel binaries..."
	@mkdir -p cmd/relay-server/dist/tunnel
	@for GOOS in linux darwin; do \
		for GOARCH in amd64 arm64; do \
			OUT="cmd/relay-server/dist/tunnel/portal-tunnel-$${GOOS}-$${GOARCH}"; \
			echo " - $${OUT}"; \
			CGO_ENABLED=0 GOOS=$${GOOS} GOARCH=$${GOARCH} go build -trimpath -ldflags "-s -w" -o "$${OUT}" ./cmd/portal-tunnel; \
		done; \
	done

# Build Go relay server
build-server: seed-go-mod-cache
	@echo "[server] building Go portal..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/relay-server ./cmd/relay-server

clean:
	rm -rf bin
	rm -rf cmd/relay-server/dist/app
	rm -rf cmd/relay-server/dist/wasm
	rm -rf cmd/relay-server/dist/tunnel

# Benchmarking
build-bench-reporter: seed-go-mod-cache
	@echo "[bench] building bench-reporter..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/bench-reporter ./cmd/bench-reporter

bench-portal: build-bench-reporter seed-go-mod-cache
	@echo "[bench] running portal benchmarks..."
	@go test -v -run=^$ -bench=. -benchmem -cpuprofile=cpu.prof -memprofile=mem.prof ./portal/... 2>&1 | tee /tmp/bench.out
	@cat /tmp/bench.out | ./bin/bench-reporter
	@echo "[bench] cleaning up profiles..."
	@rm -f cpu.prof mem.prof /tmp/bench.out
