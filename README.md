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

## Architecture Overview

```
┌────────────┐   pubsub (GossipSub)    ┌─────────────┐
│  relay dns │ <---------------------> │  client(s)  │
│  server    │                         │(in your app)│
└────────────┘                         └─────────────┘
      │                                      │
      │ TCP stream (e.g. SSH, HTTP, custom)  │
      ▼                                      ▼
   Your users                        Your local service
```

## Getting Started

### 1️⃣ Run the Server (Docker Compose)

```bash
docker compose up --build -d
```

Published ports:
- Admin/UI + HTTP proxy: `8080`
- libp2p TCP/QUIC: `4001/tcp`, `4001/udp`

### 2️⃣ (Optional) Run Example Clients

Clients are NOT required to run the server. They are provided for testing/demo.

- HTTP client (optional): exposes a tiny local HTTP backend and advertises it.
  ```bash
  make client-run
  # Optional overrides:
  # make client-run BACKEND_PORT=8081 SERVER_URL=http://localhost:8080
  ```

- Chat client (optional): WebSocket chat UI (local) + advertiser (libp2p). Uses coder/websocket.
  ```bash
  make chat-run
  # Optional overrides:
  # make chat-run CHAT_PORT=8091 CHAT_NAME=demo-chat SERVER_URL=http://localhost:8080
  ```

If you run the chat client:
- Open locally: `http://localhost:8091`
- Open via server proxy: Admin → your peer → Open (routes to `/peer/<peerID>/` then `/peer/<peerID>/ws`).

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

HTTP client flags (see `make client-run`):
- `--server-url` Admin base URL to fetch `/health` (default `http://localhost:8080`)
- `--port` Local backend HTTP port (default `8081`)

Chat client flags (see `make chat-run`):
- `--server-url` Admin base URL to fetch `/health` (default `http://localhost:8080`)
- `--port` Local chat HTTP port (default `8091`)
- `--name` Display name (shown on server UI)

## Deploying the Server (public)

- Expose ports:
  - `8080/tcp` Admin UI + HTTP/WS proxy (`/peer/<peerID>/`) — flag: `--http-port`
  - `4001/tcp` and `4001/udp` libp2p (GossipSub/streams) — flag: `--p2p-port`
- Cloudflare DNS:
  - Web UI/proxy (8080): can be proxied (orange cloud) if using HTTP/HTTPS
  - libp2p (4001 tcp/udp): must be DNS only (gray cloud). Cloudflare proxy doesn’t support arbitrary TCP/UDP ports.
- WebSocket: server proxies 101 and tunnels bytes; the chat backend allows any HTTP(S) Origin for demo. Restrict in production.
