# Multi-stage build for relayserver
FROM golang:1 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o /out/relayserver ./cmd/server

# Minimal runtime image
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/relayserver /usr/bin/relayserver

EXPOSE 8080 8082 4001/tcp 4001/udp

ENTRYPOINT ["/usr/bin/relayserver"]
