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

# Build WASM (if needed), precompress, and server
RUN if ls dist/[0-9a-f]*.wasm.br >/dev/null 2>&1; then \
      echo "[docker] Using prebuilt WASM artifacts in dist/"; \
    else \
      echo "[docker] No prebuilt WASM found; running make build-wasm compress-wasm"; \
      make build-wasm compress-wasm; \
    fi && \
    make build-server

FROM gcr.io/distroless/static-debian12:nonroot

# Copy server binary
COPY --from=builder /src/bin/relay-server /usr/bin/relay-server

# Set default environment variables
ENV STATIC_DIR=/app/dist
ENV PORTAL_UI_URL=http://localhost:4017
ENV PORTAL_FRONTEND_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=ws://localhost:4017/relay

# Expose ports
# 4017: relay server and portal frontend
EXPOSE 4017

ENTRYPOINT ["/usr/bin/relay-server"]
