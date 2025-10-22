SHELL := /bin/sh
.PHONY: help server-up server-down server-build client-run client-build chat-run chat-build \
        fmt tidy lint lint-fix test test-race test-coverage build-all clean \
        install-tools pre-commit-install ci-local

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
client-run:
	go run ./cmd/example_http_client

client-build:
	go build -trimpath -o bin/relaydns-client ./cmd/example_http_client

# ---------- Chat (local go) ----------
chat-run:
	go run ./cmd/example_chat

chat-build:
	go build -trimpath -o bin/relaydns-chat ./cmd/example_chat

# ---------- Build all binaries ----------
build-all: clean
	@echo "Building all binaries..."
	@mkdir -p bin
	go build -trimpath -o bin/relaydns-server ./cmd/server
	go build -trimpath -o bin/relaydns-client ./cmd/example_http_client
	go build -trimpath -o bin/relaydns-chat ./cmd/example_chat
	@echo "Binaries built successfully in ./bin/"

# ---------- Dev helpers ----------
fmt:
	@echo "Formatting Go code..."
	@command -v gofumpt >/dev/null 2>&1 || { echo "Installing gofumpt..."; go install mvdan.cc/gofumpt@latest; }
	gofumpt -l -w .
	@command -v goimports >/dev/null 2>&1 || { echo "Installing goimports..."; go install golang.org/x/tools/cmd/goimports@latest; }
	goimports -local github.com/gosuda/relaydns -w .

tidy:
	@echo "Tidying go.mod..."
	go mod tidy

# ---------- Linting ----------
lint:
	@echo "Running linters..."
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run --timeout=5m --config=.golangci.yml

lint-fix:
	@echo "Running linters with auto-fix..."
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not found. Install: https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run --timeout=5m --config=.golangci.yml --fix

# ---------- Testing ----------
test:
	@echo "Running tests..."
	go test -v -timeout=5m ./...

test-race:
	@echo "Running tests with race detector..."
	go test -v -race -timeout=5m ./...

test-coverage:
	@echo "Running tests with coverage..."
	go test -v -race -timeout=5m -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
	@go tool cover -func=coverage.out | grep total

# ---------- Static analysis & sanitizers ----------
vet:
	@echo "Running go vet..."
	go vet ./...

staticcheck:
	@echo "Running staticcheck..."
	@command -v staticcheck >/dev/null 2>&1 || { echo "Installing staticcheck..."; go install honnef.co/go/tools/cmd/staticcheck@latest; }
	staticcheck ./...

gosec:
	@echo "Running gosec (security scanner)..."
	@command -v gosec >/dev/null 2>&1 || { echo "Installing gosec..."; go install github.com/securego/gosec/v2/cmd/gosec@latest; }
	gosec ./...

govulncheck:
	@echo "Checking for known vulnerabilities..."
	@command -v govulncheck >/dev/null 2>&1 || { echo "Installing govulncheck..."; go install golang.org/x/vuln/cmd/govulncheck@latest; }
	govulncheck ./...

# ---------- Pre-commit hooks ----------
pre-commit-install:
	@echo "Installing pre-commit hooks..."
	@command -v pre-commit >/dev/null 2>&1 || { echo "pre-commit not found. Install: pip install pre-commit"; exit 1; }
	pre-commit install
	@echo "Pre-commit hooks installed successfully"

pre-commit-run:
	@echo "Running pre-commit hooks on all files..."
	@command -v pre-commit >/dev/null 2>&1 || { echo "pre-commit not found. Install: pip install pre-commit"; exit 1; }
	pre-commit run --all-files

# ---------- Tool installation ----------
install-tools:
	@echo "Installing development tools..."
	go install golang.org/x/tools/cmd/goimports@latest
	go install mvdan.cc/gofumpt@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/gordonklaus/ineffassign@latest
	@echo "Tools installed successfully"
	@echo "Note: Install golangci-lint separately: https://golangci-lint.run/usage/install/"
	@echo "Note: Install pre-commit separately: pip install pre-commit"

# ---------- CI local simulation ----------
ci-local: fmt tidy vet lint test-race
	@echo "Local CI checks completed successfully"

# ---------- Cleanup ----------
clean:
	@echo "Cleaning build artifacts..."
	rm -rf bin/
	rm -f coverage.out coverage.html
	rm -f gosec-report.json
	@echo "Cleanup completed"

# ---------- Help ----------
help:
	@echo "RelayDNS Makefile"
	@echo ""
	@echo "Server:"
	@echo "  make server-up        # build and start relayserver (docker compose)"
	@echo "  make server-down      # stop and remove containers"
	@echo "  make server-build     # build server image only"
	@echo ""
	@echo "Clients (optional):"
	@echo "  make client-run       # run example_http_client locally"
	@echo "  make client-build     # build example_http_client to ./bin/relaydns-client"
	@echo "  make chat-run         # run example_chat locally (WS UI + advertiser)"
	@echo "  make chat-build       # build example_chat to ./bin/relaydns-chat"
	@echo ""
	@echo "  Tips: override via variables, ARGS or use -- to pass extra flags"
	@echo "    make chat-run CHAT_NAME=alice CHAT_PORT=8099"
	@echo "    make chat-run ARGS=\"--name alice --port 8099\""
	@echo "    make chat-run -- --name alice --port 8099"
	@echo ""
	@echo "Build:"
	@echo "  make build-all        # build all binaries (server, client, chat)"

# Swallow extra words after `--` so make doesn't error on them
# This keeps explicit targets intact; only unknown goals match this pattern.
%::
	@:
	@echo ""
	@echo "Development:"
	@echo "  make fmt              # format Go code with gofmt and goimports"
	@echo "  make tidy             # tidy go.mod"
	@echo "  make vet              # run go vet"
	@echo "  make lint             # run golangci-lint"
	@echo "  make lint-fix         # run golangci-lint with auto-fix"
	@echo ""
	@echo "Testing:"
	@echo "  make test             # run unit tests"
	@echo "  make test-race        # run tests with race detector"
	@echo "  make test-coverage    # run tests with coverage report"
	@echo ""
	@echo "Static Analysis & Security:"
	@echo "  make staticcheck      # run staticcheck"
	@echo "  make gosec            # run gosec security scanner"
	@echo "  make govulncheck      # check for known vulnerabilities"
	@echo ""
	@echo "Pre-commit:"
	@echo "  make pre-commit-install  # install pre-commit hooks"
	@echo "  make pre-commit-run      # run pre-commit on all files"
	@echo ""
	@echo "Tools:"
	@echo "  make install-tools    # install development tools"
	@echo ""
	@echo "CI/CD:"
	@echo "  make ci-local         # run local CI checks (fmt, tidy, vet, lint, test-race)"
	@echo ""
	@echo "Cleanup:"
	@echo "  make clean            # remove build artifacts and reports"
	@echo ""
	@echo "Flags (override with make VAR=value):"
	@echo "  SERVER_URL BACKEND_PORT CHAT_PORT CHAT_NAME"
