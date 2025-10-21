# RelayDNS
> A lightweight, DNS-driven peer-to-peer proxy layer built on libp2p.

`relaydns` provides a minimal DNS-entry proxy that routes traffic between arbitrary nodes over **libp2p**.  
It lets you expose and discover TCP services (like SSH, API endpoints, etc.) even behind NAT,  
without depending on centralized reverse-proxy services.

## Features

- 🛰 **Peer-to-peer routing** over libp2p (supports hole punching, relay, pubsub)
- 🧩 **DNS-driven entrypoint** (server acts as a lightweight coordinator)
- 🔄 **Automatic peer advertisement** via GossipSub
- 🔌 **Pluggable client SDK** — embed the relaydns client directly into your Go applications
- 🪶 **Lightweight** and dependency-minimal (Cobra CLI + Go libp2p only)

## Architecture Overview

```
┌──────────────┐      pubsub (GossipSub)      ┌──────────────┐
│  relaydns    │ <--------------------------> │   client(s)  │
│  server      │                              │ (imported in │
│ (director)   │                              │  your app)   │
└──────────────┘                              └──────────────┘
       │                                              │
       │   TCP stream (e.g. SSH, HTTP, custom)        │
       ▼                                              ▼
   Your users                                Your local service
```

## Getting Started

### 1️⃣ Run the Server (Docker Compose)

The server accepts incoming TCP connections and forwards them over libp2p to available clients.

Option A — using Makefile:
```bash
make server-up           # build + start
make server-down         # stop
```

Option B — raw docker compose:
```bash
docker compose up --build -d
docker compose logs -f relayserver
```

Published ports:
- Admin HTTP: `8080`
- HTTP ingress (tcp-level): `8082`
- libp2p TCP/QUIC: `4001/tcp`, `4001/udp`

To add bootstraps, edit `docker-compose.yml` and append repeated `--bootstrap` flags under `relayserver.command`.

### 2️⃣ Run the Example Client (Local Go)

The example client runs a local HTTP backend and advertises it via libp2p.

Option A — using Makefile (recommended):
```bash
make client-run \
  BACKEND_HTTP=:8081 \
  SERVER_URL=http://localhost:8080 \
  BOOTSTRAPS="/dnsaddr/your.bootstrap/p2p/12D3Koo..."
```

Option B — go run directly:
```bash
go run ./cmd/example_client \
  --backend-http :8081 \
  --server-url http://localhost:8080 \
  --bootstrap /dnsaddr/your.bootstrap/p2p/12D3Koo... \
  
```

The client exposes a tiny local HTTP server at `--backend-http` and tunnels traffic from the server to this address via libp2p streams.

### 3️⃣ Embed the Client SDK in Your App

Install the module:
```bash
go get github.com/gosuda/relaydns
```

Minimal snippet:
```go
package main

import (
    "context"
    "time"
    "github.com/gosuda/relaydns/relaydns"
    "github.com/libp2p/go-libp2p"
)

func main() {
    ctx := context.Background()
    h, _ := libp2p.New(libp2p.EnableHolePunching(), libp2p.EnableNATService())
    client, _ := relaydns.NewClient(ctx, h, relaydns.ClientConfig{
        Protocol:       "/relaydns/http/1.0",
        Topic:          "relaydns.backends",
        AdvertiseEvery: 5 * time.Second,
        TargetTCP:      "127.0.0.1:8081",
        Name:           "demo-http",
    })
    defer client.Close()
    select {}
}
```

## Configuration Reference

Server flags (see `docker-compose.yml`):
- `--admin-http` Admin API listen address (default `:8080`)
- `--ingress-http` HTTP ingress listen address (default `:8082`)
- `--bootstrap` Repeatable multiaddr with `/p2p/`

Example client flags (see `make client-run`):
- `--server-url` Admin base URL to fetch `/health` (default `http://localhost:8080`)
- `--bootstrap` Repeatable multiaddr with `/p2p/`
- `--backend-http` Local backend HTTP listen address (default `:8081`)

## Logging

This project uses `zerolog` for structured logging. Binaries initialize a human-friendly console logger by default.

## Development

Useful commands:
```bash
make server-up      # build and start the server via docker compose
make server-down    # stop the compose stack

make client-run     # run the example client locally
make client-build   # build the example client to ./bin/relaydns-client
```
