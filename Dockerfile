# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1 AS builder
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
  nodejs npm brotli make binaryen && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/root/.npm \
  make build-frontend

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  make build-wasm && \
  GOOS=${TARGETOS} GOARCH=${TARGETARCH} make build-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /src/bin/relay-server /usr/bin/relay-server

ENV PORTAL_URL=http://localhost:4017
ENV PORTAL_APP_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=ws://localhost:4017/relay
ENV NOINDEX=false
ENV TZ=UTC

EXPOSE 4017
ENTRYPOINT ["/usr/bin/relay-server"]
