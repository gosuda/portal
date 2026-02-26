# syntax=docker/dockerfile:1

# Stage 1: Build frontend (Node.js)
FROM --platform=$BUILDPLATFORM node:22-slim AS frontend-builder
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
  make && rm -rf /var/lib/apt/lists/*

COPY cmd/relay-server/frontend ./cmd/relay-server/frontend
COPY Makefile ./

RUN --mount=type=cache,target=/root/.npm \
  make build-frontend

# Stage 2: Build Go artifacts
FROM --platform=$BUILDPLATFORM golang:1 AS go-builder
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
  brotli make && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN rm -rf bin/
COPY --from=frontend-builder /src/cmd/relay-server/dist/app ./cmd/relay-server/dist/app

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  make build-tunnel && \
  GOOS=${TARGETOS} GOARCH=${TARGETARCH} make build-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=go-builder /src/bin/relay-server /usr/bin/relay-server

ENV PORTAL_URL=http://localhost:4017
ENV PORTAL_APP_URL=http://*.localhost:4017
ENV ADMIN_SECRET_KEY=
ENV NOINDEX=false
ENV TZ=UTC

EXPOSE 4017
ENTRYPOINT ["/usr/bin/relay-server"]
