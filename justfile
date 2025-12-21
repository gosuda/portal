# Justfile for Portal project

# Build everything (protoc, wasm, frontend, server)
build:
    make build

# Run the relay server
run:
    make run

# Run the demo app
demo:
    go run ./cmd/demo-app

# Run linters and fix issues
lint:
    golangci-lint run --fix

# Format code
fmt:
     golangci-lint fmt ./...

# Run all tests (Go unit tests, WASM tests)
test:
    go test -v -race -coverprofile=coverage.out ./...
    node --test cmd/webclient/*.test.js
    GOCACHE=/tmp/portal-go-cache GOOS=js GOARCH=wasm go test -exec "node `go env GOROOT`/lib/wasm/wasm_exec_node.js" ./cmd/webclient
