SHELL := /bin/sh

.PHONY: build build-wasm build-server clean

# Convenience target: build wasm then server
build: build-wasm build-server

# Build WASM artifacts and copy to both embed dir and SDK dir (no external scripts)
build-wasm:
	@echo "[wasm] building with wasm-pack..."
	cd relaydns/wasm && wasm-pack build --target web --out-dir pkg
	@echo "[wasm] copying WASM artifacts to embed dirs..."
	mkdir -p cmd/relay-server/wasm
	rm -rf cmd/relay-server/wasm/* sdk/wasm/*
	cp -R relaydns/wasm/pkg/. cmd/relay-server/wasm/
	@echo "[wasm] copying service workers and E2EE proxy files..."
	cp relaydns/wasm/sw-proxy.js cmd/relay-server/wasm/
	cp relaydns/wasm/sw.js cmd/relay-server/wasm/

# Build Go relay server (embeds WASM from cmd/relay-server/wasm)
build-server:
	@echo "[server] building Go relayserver..."
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/relayserver ./cmd/relay-server

clean:
	rm -rf bin
	rm -rf cmd/relay-server/wasm
	rm -rf sdk/wasm
	rm -rf relaydns/wasm/pkg
