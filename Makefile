.PHONY: fmt vet lint lint-fix test vuln tidy build build-cmd proto lint-frontend build-frontend frontend all

FRONTEND_DIR := cmd/relay-server/frontend
BIN_DIR := bin

fmt:
	gofmt -w .
	goimports -w .

vet:
	go vet ./...

lint:
	golangci-lint run

lint-fix:
	golangci-lint run --fix

test:
	go test -v -race -coverprofile=coverage.out ./...

vuln:
	govulncheck ./...

tidy:
	go mod tidy
	go mod verify

build:
	$(MAKE) build-cmd

build-cmd:
	mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/relay-server ./cmd/relay-server
	go build -o $(BIN_DIR)/portal-tunnel ./cmd/portal-tunnel
	go build -o $(BIN_DIR)/demo-app ./cmd/demo-app
	go build -o $(BIN_DIR)/vanity-id ./cmd/vanity-id

proto:
	buf generate
	buf lint

lint-frontend:
	cd $(FRONTEND_DIR) && npm run lint

build-frontend:
	cd $(FRONTEND_DIR) && npm run build

frontend: lint-frontend build-frontend

all: fmt vet lint test vuln build frontend
