FROM golang:1 AS builder

WORKDIR /src

# Install make, binaryen (wasm-opt), and brotli CLI for WASM build/compression
RUN apt-get update && apt-get install -y --no-install-recommends \
    binaryen \
    brotli \
    make \
  && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build WASM, precompress, and server
RUN make build-wasm compress-wasm build-server

FROM gcr.io/distroless/static-debian12:nonroot

# Copy server binary
COPY --from=builder /src/bin/relay-server /usr/bin/relay-server

# Copy static files for portal frontend
COPY --from=builder /src/dist /app/dist

# Set default environment variables
ENV STATIC_DIR=/app/dist
ENV PORTAL_UI_URL=http://localhost:4017
ENV PORTAL_FRONTEND_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=ws://localhost:4017/relay

# Expose ports
# 4017: relay server and portal frontend
EXPOSE 4017

ENTRYPOINT ["/usr/bin/relay-server"]
