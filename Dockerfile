# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1 AS builder
WORKDIR /src

RUN apt-get update && apt-get install -y --no-install-recommends \
  nodejs npm brotli make && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

RUN --mount=type=cache,target=/root/.npm \
  cd cmd/relay-server/frontend && npm ci && npm run build

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -trimpath -ldflags="-s -w" -o /src/bin/relay-server ./cmd/relay-server

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /src/bin/relay-server /usr/bin/relay-server

ENV PORTAL_URL=http://localhost:4017
ENV PORTAL_APP_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=https://localhost:4017/relay
ENV ADMIN_SECRET_KEY=
ENV NOINDEX=false
ENV TZ=UTC

EXPOSE 4017
ENTRYPOINT ["/usr/bin/relay-server"]
