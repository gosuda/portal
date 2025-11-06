SHELL := /bin/sh

.PHONY: run build build-wasm build-server clean

run:
	./bin/relay-server

# Convenience target: build wasm then server
build: build-protoc build-wasm build-server build-tunnel

build-protoc:
	protoc -I . \
	  --go_out=. \
	  --go_opt=paths=source_relative \
	  --go-vtproto_out=. \
	  --go-vtproto_opt=paths=source_relative \
	  portal/core/proto/rdsec/rdsec.proto \
	  portal/core/proto/rdverb/rdverb.proto

BOOTSTRAPS ?= ""

# Build WASM artifacts with wasm-opt optimization and generate manifest
build-wasm:
	@echo "[wasm] building webclient WASM..."
	@mkdir -p dist

	# Prepare optional link flags for bootstrap injection
	@WASM_LDFLAGS=""; \
	if [ -n "$(BOOTSTRAPS)" ]; then \
		WASM_LDFLAGS="-X main.bootstrapServersCSV=$(BOOTSTRAPS)"; \
		echo "[wasm] injecting bootstraps: $(BOOTSTRAPS)"; \
	fi; \
	GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w $$WASM_LDFLAGS" -o dist/portal.wasm ./cmd/webclient
	
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
	cp dist/portal.wasm dist/$$WASM_HASH.wasm; \
	echo "{\"wasmFile\":\"$$WASM_HASH.wasm\",\"hash\":\"$$WASM_HASH\"}" > dist/manifest.json; \
	echo "[wasm] manifest created"
	
	@echo "[wasm] copying additional resources..."
	@cp cmd/webclient/wasm_exec.js dist/wasm_exec.js
	@cp cmd/webclient/service-worker.js dist/service-worker.js
	@cp cmd/webclient/index.html dist/portal.html
	@cp cmd/webclient/portal.mp4 dist/portal.mp4
	
	@echo "[wasm] build complete"

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