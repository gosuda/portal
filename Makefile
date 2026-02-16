.PHONY: fmt vet lint test vuln tidy build proto all

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

all: fmt vet lint test vuln build
