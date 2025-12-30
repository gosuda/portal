fmt:
    golangci-lint run --fast-only --allow-parallel-runners --fix > /dev/null || true
    gofmt -w .

lint:
    golangci-lint run -D errcheck --allow-parallel-runners

lint-fix:
    golangci-lint run --allow-parallel-runners --fix

tidy:
    go mod tidy

vet:
    go vet ./...

test:
    go test -race -v ./...

all: fmt lint-fix vet test
