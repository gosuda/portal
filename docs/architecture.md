# Architecture

## Overview

Portal connects local applications to web users through a secure relay layer with end-to-end encryption.

```
┌─────────────┐      ┌──────────────┐      ┌─────────────┐
│   Browser   │◄────►│ Relay Server │◄────►│  App/Tunnel │
│             │  TLS  │  (:4017/443) │  TCP  │  (SDK/CLI)  │
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
- **Reverse Hub**: Connection management for tunnel backends
- **Lease Manager**: Registration, TTL, banning
- **SNI Router**: TLS passthrough routing

### SDK (`sdk/`)

- **Client**: Bootstrap, health checks, reconnection
- **Listener**: `net.Listener` implementation for tunnel connections
- **Types**: Shared API types (`RegisterRequest`, `Metadata`, etc.)

### Tunnel (`cmd/portal-tunnel`)

- TCP proxy between relay and local service
- No code changes required to expose existing services

## Connection Flow

1. **Register**: App/Tunnel → Relay (`POST /api/register`)
2. **Reverse Connect**: App/Tunnel ← Relay (`TCP reverse tunnel`)
3. **Client Request**: Browser → Relay (`GET *.localhost:4017`)
4. **Proxy**: Relay ↔ App/Tunnel ↔ Local Service

## Security

- **E2EE**: TLS passthrough with ACME certificates - relay routes TLS by SNI without termination
- **Tokens**: Per-lease reverse connection tokens
- **SNI Routing**: TLS passthrough without decryption

## Protocol Stack

```
TLS (E2EE via ACME certificate)
    └── TCP
        └── Application Data
```

Or for non-TLS (development only):

```
HTTP
    └── TCP
        └── Application Data
```
