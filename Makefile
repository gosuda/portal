SHELL := /bin/sh
.PHONY: help server-up server-down server-build client-run client-build chat-run chat-build fmt tidy

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
BACKEND_PORT ?= 8081
CLIENT_FLAGS := \
	--server-url $(SERVER_URL) \
	--port $(BACKEND_PORT)

client-run:
	go run ./cmd/example_http_client $(CLIENT_FLAGS)

client-build:
	go build -trimpath -o bin/relaydns-client ./cmd/example_http_client

# ---------- Chat (local go) ----------
CHAT_PORT ?= 8091
CHAT_NAME ?= demo-chat

CHAT_FLAGS := \
	--server-url $(SERVER_URL) \
	--port $(CHAT_PORT) \
	--name $(CHAT_NAME)

chat-run:
	go run ./cmd/example_chat $(CHAT_FLAGS)

chat-build:
	go build -trimpath -o bin/relaydns-chat ./cmd/example_chat

# ---------- Dev helpers ----------
fmt:
	go fmt ./...

tidy:
	go mod tidy

help:
    @echo "Server:"
    @echo "  make server-up        # build and start relayserver (docker compose)"
    @echo "  make server-down      # stop and remove containers"
    @echo "\nClients (optional):"
    @echo "  make client-run       # run example_http_client locally"
    @echo "  make client-build     # build example_http_client to ./bin/relaydns-client"
    @echo "  make chat-run         # run example_chat locally (WS UI + advertiser)"
    @echo "  make chat-build       # build example_chat to ./bin/relaydns-chat"
    @echo "\nFlags (override with make VAR=value):"
	@echo "  SERVER_URL BACKEND_PORT CHAT_PORT CHAT_NAME"
