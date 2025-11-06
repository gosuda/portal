FROM golang:1 AS builder

WORKDIR /src

# Install make and binaryen (for wasm-opt) to use Makefile
RUN apt-get update && apt-get install -y --no-install-recommends binaryen make && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build WASM and server
ARG BOOTSTRAPS=""
RUN make build-wasm BOOTSTRAPS="$BOOTSTRAPS"

# Build server
RUN make build-server

FROM gcr.io/distroless/static-debian12:nonroot

# Copy server binary
COPY --from=builder /src/bin/relay-server /usr/bin/relay-server

# Copy static files for portal frontend
COPY --from=builder /src/dist /app/dist

# Set default environment variables
ENV STATIC_DIR=/app/dist
ENV PORTAL_UI_URL=http://localhost:4017
ENV POSTAL_FRONTEND_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=ws://localhost:4017/relay,wss://some.app:21762/relay

# Expose ports
# 4017: relay server and portal frontend
EXPOSE 4017

ENTRYPOINT ["/usr/bin/relay-server"]
