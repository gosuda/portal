SHELL := /bin/sh

.PHONY: run build build-wasm compress-wasm build-server clean

run:
	./bin/relay-server

# Convenience target: build wasm, compress, then server
build: build-protoc build-wasm compress-wasm build-server build-tunnel

build-protoc:
	protoc -I . \
	  --go_out=. \
	  --go_opt=paths=source_relative \
	  --go-vtproto_out=. \
	  --go-vtproto_opt=paths=source_relative \
	  portal/core/proto/rdsec/rdsec.proto \
	  portal/core/proto/rdverb/rdverb.proto

# Build WASM artifacts with wasm-opt optimization and generate manifest
build-wasm:
	@echo "[wasm] building webclient WASM..."
	@mkdir -p dist

	GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o dist/portal.wasm ./cmd/webclient
	
	@echo "[wasm] optimizing with wasm-opt..."
	@if command -v wasm-opt >/dev/null 2>&1; then \
		wasm-opt -Oz --enable-bulk-memory dist/portal.wasm -o dist/portal.wasm.tmp && \
		mv dist/portal.wasm.tmp dist/portal.wasm; \
		echo "[wasm] optimization complete"; \
	else \
		echo "[wasm] WARNING: wasm-opt not found, skipping optimization"; \
		echo "[wasm] Install binaryen for smaller WASM files: brew install binaryen (macOS) or apt-get install binaryen (Linux)"; \
	fi
	
	@echo "[wasm] calculating SHA256 hash..."
	@WASM_HASH=$$(shasum -a 256 dist/portal.wasm | awk '{print $$1}'); \
	echo "[wasm] SHA256: $$WASM_HASH"; \
	echo "[wasm] cleaning old hash files..."; \
	find dist -name '[0-9a-f]*.wasm' ! -name "$$WASM_HASH.wasm" -type f -delete 2>/dev/null || true; \
	cp dist/portal.wasm dist/$$WASM_HASH.wasm; \
	rm -f dist/portal.wasm; \
	echo "[wasm] content-addressed WASM: $$WASM_HASH.wasm"
	
	@echo "[wasm] copying additional resources..."
	@cp cmd/webclient/wasm_exec.js dist/wasm_exec.js
	@cp cmd/webclient/service-worker.js dist/service-worker.js
	@cp cmd/webclient/index.html dist/portal.html
	@cp cmd/webclient/portal.mp4 dist/portal.mp4
	
	@echo "[wasm] build complete"

# Precompress content-addressed WASM with brotli
compress-wasm:
	@echo "[wasm] precompressing webclient WASM with brotli..."
	@WASM_FILE=$$(ls dist/[0-9a-f]*.wasm 2>/dev/null | head -n1); \
	if [ -z "$$WASM_FILE" ]; then \
		echo "[wasm] ERROR: no content-addressed WASM found in dist; run build-wasm first"; \
		exit 1; \
	fi; \
	WASM_HASH=$$(basename "$$WASM_FILE" .wasm); \
	if ! command -v brotli >/dev/null 2>&1; then \
		echo "[wasm] ERROR: brotli not found; install brotli to build compressed WASM"; \
		exit 1; \
	fi; \
	brotli -f "$$WASM_FILE" -o "dist/$$WASM_HASH.wasm.br"; \
	rm -f "$$WASM_FILE"; \
	echo "[wasm] brotli: dist/$$WASM_HASH.wasm.br (original removed)"

# Build Go relay server (embeds WASM from cmd/relay-server/static)
build-server:
	@echo "[server] building Go portal..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/relay-server ./cmd/relay-server

# Build Portal Tunnel CLI (cloudflared-style tunnel)
build-tunnel:
	@echo "[tunnel] building Portal Tunnel CLI..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/portal-tunnel ./cmd/portal-tunnel

clean:
	rm -rf bin
	rm -rf dist
