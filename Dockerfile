# syntax=docker/dockerfile:1

# Frontend build stage
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend-builder

WORKDIR /src

# Copy frontend package files
COPY cmd/relay-server/frontend/package*.json ./cmd/relay-server/frontend/

# Install frontend dependencies
RUN --mount=type=cache,target=/root/.npm \
  cd cmd/relay-server/frontend && \
  npm ci

# Copy frontend source code
COPY cmd/relay-server/frontend ./cmd/relay-server/frontend

# Build frontend
RUN cd cmd/relay-server/frontend && npm run build

# Go builder stage
FROM --platform=$BUILDPLATFORM golang:1 AS builder

WORKDIR /src

# Install make, binaryen (wasm-opt), and brotli CLI for WASM build/compression
RUN apt-get update && apt-get install -y --no-install-recommends \
  binaryen \
  brotli \
  make \
  && rm -rf /var/lib/apt/lists/*

# Set GOMODCACHE to cache Go modules in cache volume
RUN go env -w GOMODCACHE=/root/.cache/go-build

# Copy go.mod and go.sum
COPY go.mod go.sum ./

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

# Copy the rest of the source code
COPY . .

# Copy built frontend from frontend-builder stage
COPY --from=frontend-builder /src/cmd/relay-server/app ./cmd/relay-server/app

RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  make build-wasm

ARG TARGETPLATFORM
ARG TARGETOS
ARG TARGETARCH

RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  GOOS=${TARGETOS} GOARCH=${TARGETARCH} make build-server

FROM gcr.io/distroless/static-debian12:nonroot

# Copy server binary
COPY --from=builder /src/bin/relay-server /usr/bin/relay-server

# Set default environment variables
ENV PORTAL_UI_URL=http://localhost:4017
ENV PORTAL_FRONTEND_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=ws://localhost:4017/relay

# Expose ports
# 4017: relay server and portal frontend
EXPOSE 4017

ENTRYPOINT ["/usr/bin/relay-server"]
