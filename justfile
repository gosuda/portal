frontend_dir := "cmd/relay-server/frontend"
bin_dir := "bin"

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
    just build-cmd

build-cmd:
    mkdir -p {{bin_dir}}
    go build -o {{bin_dir}}/relay-server ./cmd/relay-server
    go build -o {{bin_dir}}/portal-tunnel ./cmd/portal-tunnel
    go build -o {{bin_dir}}/demo-app ./cmd/demo-app
    go build -o {{bin_dir}}/vanity-id ./cmd/vanity-id

proto:
    buf generate
    buf lint

lint-frontend:
    cd {{frontend_dir}} && npm run lint

build-frontend:
    cd {{frontend_dir}} && npm run build

frontend: lint-frontend build-frontend

all: fmt vet lint test vuln build frontend
