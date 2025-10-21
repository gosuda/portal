SHELL := /bin/sh
.PHONY: help server-up server-down server-build client-run client-build fmt tidy

# Detect docker compose command (override with `make DC="docker-compose"` if needed)
DC ?= docker compose

# ---------- Server (docker) ----------
server-up:
	$(DC) up -d --build

server-down:
	$(DC) down

server-build:
	$(DC) build

# ---------- Client (local go) ----------
# Default flags (override via `make VAR=value`)
SERVER_URL ?= http://localhost:8080
BACKEND_HTTP ?= :8081
# Optional repeated flags: BOOTSTRAPS="/dnsaddr/example/p2p/12D3... /ip4/1.2.3.4/tcp/4001/p2p/12D3..."
BOOTSTRAPS ?=
CLIENT_BOOTSTRAPS_FLAGS := $(foreach b,$(BOOTSTRAPS),--bootstrap $(b))

CLIENT_FLAGS := \
	--server-url $(SERVER_URL) \
	--backend-http $(BACKEND_HTTP) \
	$(CLIENT_BOOTSTRAPS_FLAGS)

client-run:
	go run ./cmd/example_client $(CLIENT_FLAGS)

client-build:
	go build -trimpath -o bin/relaydns-client ./cmd/example_client

# ---------- Dev helpers ----------
fmt:
	go fmt ./...

tidy:
	go mod tidy

help:
	@echo "Server:"
	@echo "  make server-up        # build and start relayserver (docker compose)"
	@echo "  make server-down      # stop and remove containers"
	@echo "\nClient:"
	@echo "  make client-run       # run example_client locally with minimal flags"
	@echo "  make client-build     # build example_client to ./bin/relaydns-client"
	@echo "\nFlags (override with make VAR=value):"
	@echo "  SERVER_URL BACKEND_HTTP BOOTSTRAPS"
