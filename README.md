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

```bash
docker compose up --build -d
```

Published ports:
- Admin/UI + HTTP proxy: `8080`
- libp2p TCP/QUIC: `4001/tcp`, `4001/udp`

### 2️⃣ Run the Example Client (Makefile)

The example client runs a tiny local HTTP backend and advertises it over libp2p.

```bash
make client-run
# Optional (override on demand):
# make client-run BACKEND_HTTP=:8081 SERVER_URL=http://localhost:8080 \
#   BOOTSTRAPS="/dnsaddr/your.bootstrap/p2p/12D3Koo..."
```

The client exposes a tiny local HTTP server and tunnels traffic to it via libp2p streams.

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
)

func main() {
    ctx := context.Background()
    client, _ := relaydns.NewClient(ctx, relaydns.ClientConfig{
        Protocol:       "/relaydns/http/1.0",
        Topic:          "relaydns.backends",
        AdvertiseEvery: 5 * time.Second,
        TargetTCP:      "127.0.0.1:8081",
        Name:           "demo-http",
    })
    _ = client.Start(ctx)
    defer client.Close()
    select {}
}
```

## Configuration Reference

Server flags (see `docker-compose.yml`):
- `--http` Unified admin UI + HTTP proxy listen address (default `:8080`)
- `--bootstrap` Repeatable multiaddr with `/p2p/`

Example client flags (see `make client-run`):
- `--server-url` Admin base URL to fetch `/health` (default `http://localhost:8080`)
- `--bootstrap` Repeatable multiaddr with `/p2p/`
- `--backend-http` Local backend HTTP listen address (default `:8081`)