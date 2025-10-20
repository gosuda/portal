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

### 1️⃣ Run the RelayDNS Server

The **server** acts as a public entrypoint that accepts incoming TCP connections  
and forwards them over libp2p to available clients.

```bash
go build -o relaydns ./cmd/relaydns

./relaydns \
  --listen-tcp :22 \
  --listen-http :8080 \
  --protocol /relaydns/ssh/1.0 \
  --topic relaydns.backends
```

### 2️⃣ Embed the RelayDNS Client in Your App

The client is a small Go library that you can embed in any Go program.
It automatically advertises itself and tunnels incoming streams to your local TCP service.

Install the module:
```bash
go get github.com/gosuda/relaydns
```

Example usage:
```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/libp2p/go-libp2p"
    "github.com/gosuda/relaydns/relaydns"
)

func main() {
    ctx := context.Background()
    h, err := libp2p.New(
        libp2p.EnableHolePunching(),
        libp2p.EnableNATService(),
    )
    if err != nil {
        log.Fatal(err)
    }

    client, err := relaydns.NewClient(ctx, h, relaydns.ClientConfig{
        Protocol:       "/relaydns/ssh/1.0",
        Topic:          "relaydns.backends",
        AdvertiseEvery: 5 * time.Second,
        TargetTCP:      "127.0.0.1:22", // your local SSH or app port
        Name:           "seoul-node",
    })
    if err != nil {
        log.Fatal(err)
    }
    defer client.Close()

    select {} // keep running
}
```