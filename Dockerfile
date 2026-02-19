# syntax=docker/dockerfile:1

# Stage 1: Frontend build
FROM node:22-slim AS frontend-builder
WORKDIR /app
COPY cmd/relay-server/frontend/package*.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY cmd/relay-server/frontend/ ./
RUN npm run build

# Stage 2: Go build
FROM golang:1.26 AS go-builder
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
COPY --from=frontend-builder /dist/app ./cmd/relay-server/dist/app/

RUN make build-tunnel

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /src/bin/relay-server ./cmd/relay-server

# Stage 3
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=go-builder /src/bin/relay-server /usr/bin/relay-server

ENV PORTAL_URL=http://localhost:4017
ENV PORTAL_APP_URL=http://*.localhost:4017
ENV BOOTSTRAP_URIS=https://localhost:4017/relay
ENV ADMIN_SECRET_KEY=
ENV NOINDEX=false
ENV TZ=UTC

EXPOSE 4017
ENTRYPOINT ["/usr/bin/relay-server"]
