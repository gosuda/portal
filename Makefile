SHELL := /bin/sh

.PHONY: build build-wasm build-server clean

# Convenience target: build wasm then server
build: build-protoc build-wasm build-server

build-protoc:
	protoc -I . \
	  --go_out=. \
	  --go_opt=paths=source_relative \
	  --go-vtproto_out=. \
	  --go-vtproto_opt=paths=source_relative \
	  portal/core/proto/rdsec/rdsec.proto \
	  portal/core/proto/rdverb/rdverb.proto

# Build WASM artifacts and copy to both embed dir and SDK dir (no external scripts)
build-wasm:
	@echo "[wasm] building with wasm-pack..."
	cd portal/wasm && wasm-pack build --target web --out-dir pkg
	@echo "[wasm] copying WASM artifacts to embed dirs..."
	mkdir -p cmd/relay-server/wasm
	rm -rf cmd/relay-server/wasm/* sdk/wasm/*
	cp -R portal/wasm/pkg/. cmd/relay-server/wasm/
	@echo "[wasm] copying service workers and E2EE proxy files..."
	cp portal/wasm/sw-proxy.js cmd/relay-server/wasm/
	cp portal/wasm/sw.js cmd/relay-server/wasm/
	@echo "[wasm] copying SecureWebSocket E2EE files..."
	cp portal/wasm/secure-websocket.js cmd/relay-server/wasm/
	cp portal/wasm/secure-websocket-sw.js cmd/relay-server/wasm/

# Build Go relay server (embeds WASM from cmd/relay-server/wasm)
build-server:
	@echo "[server] building Go portal..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/relay-server ./cmd/relay-server

# Build Portal Tunnel CLI (cloudflared-style tunnel)
build-tunnel:
	@echo "[tunnel] building Portal Tunnel CLI..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/portal-tunnel ./cmd/portal-tunnel

# Build all binaries
build-all: build-protoc build-wasm build-server build-tunnel

clean:
	rm -rf bin
	rm -rf cmd/relay-server/wasm
	rm -rf sdk/wasm
	rm -rf portal/wasm/pkg
