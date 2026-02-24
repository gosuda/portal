# Architecture

## Overview

Portal connects local applications to web users through a secure relay layer with end-to-end encryption.

```
┌─────────────┐      ┌──────────────┐      ┌─────────────┐
│   Browser   │◄────►│ Relay Server │◄────►│  App/Tunnel │
│  (WASM SW)  │  WS  │  (:4017)     │  WS  │  (SDK/CLI)  │
└─────────────┘      └──────────────┘      └─────────────┘
                            │
                            ▼
                     ┌──────────────┐
                     │  Local HTTP  │
                     │  (:3000 etc) │
                     └──────────────┘
```

## Components

### Relay Server (`cmd/relay-server`)

- **HTTP Server**: Static files, admin UI, API endpoints
- **WebSocket Relay**: `/api/connect` for reverse tunnel connections
- **Lease Manager**: Registration, TTL, banning
- **SNI Router**: TLS passthrough routing

### SDK (`sdk/`)

- **Client**: Bootstrap, health checks, reconnection
- **Listener**: `net.Listener` implementation over WebSocket
- **Types**: Shared API types (`RegisterRequest`, `Metadata`, etc.)

### Tunnel (`cmd/portal-tunnel`)

- TCP proxy between relay and local service
- No code changes required to expose existing services

### WebClient (`cmd/webclient`)

- WASM-based Service Worker proxy
- Runs in browser for E2EE communication

## Connection Flow

1. **Register**: App/Tunnel → Relay (`POST /api/register`)
2. **Reverse Connect**: App/Tunnel ← Relay (`WS /api/connect`)
3. **Client Request**: Browser → Relay (`GET *.localhost:4017`)
4. **Proxy**: Relay ↔ App/Tunnel ↔ Local Service

## Security

- **RDSEC**: X25519 key exchange + ChaCha20-Poly1305 encryption
- **Tokens**: Per-lease reverse connection tokens
- **SNI Routing**: TLS passthrough without termination

## Protocol Stack

```
HTTP/WebSocket
    └── yamux (multiplexing)
        └── RDSEC (E2EE)
            └── Application Data
```
