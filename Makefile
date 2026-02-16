.PHONY: fmt vet lint test vuln tidy build proto lint-frontend build-frontend frontend all

FRONTEND_DIR := cmd/relay-server/frontend

fmt:
	gofmt -w .
	goimports -w .

vet:
	go vet ./...

lint:
	golangci-lint run

test:
	go test -v -race -coverprofile=coverage.out ./...

vuln:
	govulncheck ./...

tidy:
	go mod tidy
	go mod verify

build:
	go build ./...

proto:
	buf generate
	buf lint

lint-frontend:
	cd $(FRONTEND_DIR) && npm run lint

build-frontend:
	cd $(FRONTEND_DIR) && npm run build

frontend: lint-frontend build-frontend

all: fmt vet lint test vuln build frontend
