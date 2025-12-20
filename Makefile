SHELL := /bin/sh

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
	@echo "  make clean             - Remove build artifacts"

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

# Build WASM artifacts with TinyGo (Default)
build-wasm:
	@echo "[wasm] building webclient WASM with TinyGo..."
	@mkdir -p cmd/relay-server/dist/wasm
	@rm -f cmd/relay-server/dist/wasm/*.wasm*
	
	@echo "[wasm] minifying JS assets..."
	@npx -y esbuild cmd/webclient/polyfill.js --minify --outfile=cmd/webclient/polyfill.min.js
	
	tinygo build -target=wasm -opt=2 -no-debug -o cmd/relay-server/dist/wasm/portal.wasm ./cmd/webclient; \
	
	@ls -lh cmd/relay-server/dist/wasm/portal.wasm | awk '{print "[wasm] Size (raw): " $$5}'
	
	@echo "[wasm] optimizing with wasm-opt (with the O4 flag)..."
	@if command -v wasm-opt >/dev/null 2>&1; then \
		wasm-opt -O4 --gufa --remove-unused-module-elements --remove-unused-names --enable-bulk-memory --strip-debug --strip-dwarf --strip-producers --vacuum --flatten --rereloop --converge -Oz \
			cmd/relay-server/dist/wasm/portal.wasm -o cmd/relay-server/dist/wasm/portal.wasm && \
		ls -lh cmd/relay-server/dist/wasm/portal.wasm | awk '{print "[wasm] Size (opt): " $$5}' && \
		echo "[wasm] optimization complete"; \
	else \
		echo "[wasm] WARNING: wasm-opt not found, skipping optimization"; \
		echo "[wasm] Install binaryen for smaller WASM files"; \
	fi
	
	@echo "[wasm] calculating SHA256 hash..."
	@WASM_HASH=$$(shasum -a 256 cmd/relay-server/dist/wasm/portal.wasm | awk '{print $$1}'); \
	echo "[wasm] SHA256: $$WASM_HASH"; \
	cp cmd/relay-server/dist/wasm/portal.wasm cmd/relay-server/dist/wasm/$$WASM_HASH.wasm; \
	rm -f cmd/relay-server/dist/wasm/portal.wasm; \
	echo "[wasm] content-addressed WASM: dist/wasm/$$WASM_HASH.wasm"
	
	@echo "[wasm] copying additional resources..."
	@echo "[wasm] copying and minifying additional resources..."
	@npx -y esbuild cmd/webclient/wasm_exec.js --minify --outfile=cmd/relay-server/dist/wasm/wasm_exec.js
	@npx -y esbuild cmd/webclient/service-worker.js --minify --outfile=cmd/relay-server/dist/wasm/service-worker.js
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
	brotli -f -Z "$$WASM_FILE" -o "cmd/relay-server/dist/wasm/$$WASM_HASH.wasm.br"; \
	echo "[wasm] brotli: $$(du -h "$$WASM_FILE" | cut -f1) -> $$(du -h "cmd/relay-server/dist/wasm/$$WASM_HASH.wasm.br" | cut -f1)"


# Build WASM artifacts with standard Go (Legacy)
build-wasm-std:
	@echo "[wasm] building webclient WASM with standard Go..."
	@mkdir -p cmd/relay-server/dist/wasm
	@rm -f cmd/relay-server/dist/wasm/*.wasm*
	
	@echo "[wasm] minifying JS assets..."
	@npx -y esbuild cmd/webclient/polyfill.js --minify --outfile=cmd/webclient/polyfill.min.js
	
	GOOS=js GOARCH=wasm go build -tags '!debug' -trimpath -ldflags "-s -w" -o cmd/relay-server/dist/wasm/portal.wasm ./cmd/webclient
	@ls -lh cmd/relay-server/dist/wasm/portal.wasm | awk '{print "[wasm] Size (raw): " $$5}'
	
	@echo "[wasm] optimizing with wasm-opt (with the O4 flag)..."
	@if command -v wasm-opt >/dev/null 2>&1; then \
		wasm-opt -O4 --enable-bulk-memory --strip-debug --strip-producers \
			cmd/relay-server/dist/wasm/portal.wasm -o cmd/relay-server/dist/wasm/portal.wasm && \
		ls -lh cmd/relay-server/dist/wasm/portal.wasm | awk '{print "[wasm] Size (opt): " $$5}' && \
		echo "[wasm] optimization complete"; \
	else \
		echo "[wasm] WARNING: wasm-opt not found, skipping optimization"; \
		echo "[wasm] Install binaryen for smaller WASM files"; \
	fi
	
	@echo "[wasm] calculating SHA256 hash..."
	@WASM_HASH=$$(shasum -a 256 cmd/relay-server/dist/wasm/portal.wasm | awk '{print $$1}'); \
	echo "[wasm] SHA256: $$WASM_HASH"; \
	cp cmd/relay-server/dist/wasm/portal.wasm cmd/relay-server/dist/wasm/$$WASM_HASH.wasm; \
	rm -f cmd/relay-server/dist/wasm/portal.wasm; \
	echo "[wasm] content-addressed WASM: dist/wasm/$$WASM_HASH.wasm"
	
	@echo "[wasm] copying additional resources..."
	@echo "[wasm] copying and minifying additional resources..."
	@npx -y esbuild cmd/webclient/wasm_exec.js --minify --outfile=cmd/relay-server/dist/wasm/wasm_exec.js
	@npx -y esbuild cmd/webclient/service-worker.js --minify --outfile=cmd/relay-server/dist/wasm/service-worker.js
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
	brotli -f -Z "$$WASM_FILE" -o "cmd/relay-server/dist/wasm/$$WASM_HASH.wasm.br"; \
	echo "[wasm] brotli: $$(du -h "$$WASM_FILE" | cut -f1) -> $$(du -h "cmd/relay-server/dist/wasm/$$WASM_HASH.wasm.br" | cut -f1)"


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
	@for GOOS in linux darwin; do \
		for GOARCH in amd64 arm64; do \
			OUT="cmd/relay-server/dist/tunnel/portal-tunnel-$${GOOS}-$${GOARCH}"; \
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
	rm -rf cmd/relay-server/dist/wasm
	rm -rf cmd/relay-server/dist/tunnel
