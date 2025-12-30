fmt:
    gofmt -w .

lint:
    golangci-lint run

lint-fix:
    golangci-lint run --fix

tidy:
    go mod tidy

vet:
    go vet ./...

test:
    go test -race -v ./...

all: fmt lint-fix vet test
