SHELL := /bin/sh

.PHONY: server-up server-down server-build client-run chat-run build tidy clean

# Docker Compose command (override with: make DC="docker-compose")
DC ?= docker compose

# --- Server (Docker) ---
server-up:
	$(DC) up -d --build

server-down:
	$(DC) down

server-build:
	$(DC) build

# --- Go SDK examples ---
client-run:
	go run ./sdk/go/examples/http-client

chat-run:
	go run ./sdk/go/examples/chat

# --- Build binaries ---
build:
	@echo "Building binaries..."
	@mkdir bin
	go build -trimpath -o bin/relaydns-server ./cmd/server
	go build -trimpath -o bin/relaydns-client ./sdk/go/examples/http-client
	go build -trimpath -o bin/relaydns-chat ./sdk/go/examples/chat
	@echo "Done: ./bin"

# --- Module maintenance ---
tidy:
	@echo "Syncing workspace and tidying modules..."
	@{ test -f go.work && go work sync || true; }
	go mod tidy
	(cd sdk/go && go mod tidy)

clean:
	rm -rf bin coverage.out coverage.html

