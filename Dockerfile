#############################################
# Stage 1: Build WASM artifacts
#############################################
FROM rust:1-bullseye AS wasm-builder

WORKDIR /src

# Install dependencies: make + wasm-pack
RUN apt-get update && apt-get install -y --no-install-recommends make protobuf-compiler && rm -rf /var/lib/apt/lists/*
RUN cargo install wasm-pack

# Build wasm artifacts
COPY . .
RUN make build-wasm

# Stage 2: Build Go relayserver binary (embeds WASM)
FROM golang:1 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
COPY --from=wasm-builder /src/cmd/relay-server/wasm /src/cmd/relay-server/wasm

# Install make to use Makefile and build the server
RUN apt-get update && apt-get install -y --no-install-recommends make && rm -rf /var/lib/apt/lists/*

RUN --mount=type=cache,target=/go/pkg/mod \
    make build-server && install -D bin/relayserver /out/relayserver

# Stage 3: Minimal runtime image
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/relayserver /usr/bin/relayserver

ENTRYPOINT ["/usr/bin/relayserver"]
