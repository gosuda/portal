# Portal Deploy Guide

How to run a public Portal relay with TLS passthrough.

## Overview

Portal uses SNI-based TLS passthrough: the relay routes TLS by SNI to tunnel backends.

TLS certificate mode:
- `self`: tunnel uses locally managed certificate and key files.
- `keyless`: tunnel uses a local certificate chain and delegates signing to an external signer API (relay does not hold private key).

```
Client ──TLS──► Relay (SNI Router :443) ──TLS──► Tunnel Backend (TLS mode)
                       │
                       └── Self or keyless mode at tunnel
```

## Prerequisites

- A public server (VPS / cloud VM / on-prem with port forwarding)
- A domain you can manage
- Ports 80, 443, and 4017 open to the Internet
- DNS A/AAAA records pointing to your server:
  - `example.com -> <server IP>`
  - `*.example.com -> <server IP>`
- Wildcard TLS certificate and private key for `*.example.com`

## Quick Start

### 1. DNS Configuration

```
# A records (IPv4)
example.com.             A      203.0.113.10
*.example.com.           A      203.0.113.10

# AAAA records (IPv6, if applicable)
example.com.             AAAA   2001:db8::1
*.example.com.           AAAA   2001:db8::1
```

### 2. Create .env File

```bash
# Core
PORTAL_URL=https://example.com
ADMIN_SECRET_KEY=your-secure-key-here
```

### 3. Run Portal

```bash
docker compose up -d
```

### 4. Run Tunnel

```bash
portal-tunnel --host localhost:3000 --name myapp --relay https://example.com --tls-mode keyless
```

### 5. Access

```
https://myapp.example.com
```

## Environment Variables

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `PORTAL_URL` | `http://localhost:4017` | Base URL (e.g., `https://example.com`) |
| `BOOTSTRAP_URIS` | (derived) | Relay API URLs |
| `ADMIN_SECRET_KEY` | (auto-generated) | Admin authentication key |
| `SNI_PORT` | `:443` | SNI router port |

## docker-compose.yml

```yaml
services:
  portal:
    image: ghcr.io/gosuda/portal:1
    environment:
      PORTAL_URL: ${PORTAL_URL}
      ADMIN_SECRET_KEY: ${ADMIN_SECRET_KEY}
    ports:
      - "4017:4017"
      - "443:443"
      - "80:80"
    restart: unless-stopped
```

## Tunnel Client

### TLS Mode (Production)

```bash
portal-tunnel --host localhost:3000 --name myapp --relay https://example.com --tls-mode keyless
```

- `--host`: Local service address
- `--name`: Subdomain name (becomes `myapp.example.com`)
- `--relay`: Portal relay URL
- `--tls-mode`: `no-tls`, `self`, or `keyless`

How it works:
1. Tunnel performs TLS handshake on reverse tunnel connections
2. In `self` mode, tunnel signs locally with certificate key
3. In `keyless` mode, tunnel requests signatures from external signer API
4. TLS connections go directly to tunnel on port 443

### TLS Self Mode (Tunnel-Managed Certificate)

```bash
portal-tunnel \
  --host localhost:3000 \
  --name myapp \
  --relay https://example.com \
  --tls-mode self \
  --tls-cert-file /etc/ssl/myapp/fullchain.pem \
  --tls-key-file /etc/ssl/myapp/privkey.pem
```

- Relay keyless endpoints are not used in self mode.
- Tunnel must have direct access to certificate and key files.

### TLS Keyless Mode (External Signer)

```bash
portal-tunnel \
  --host localhost:3000 \
  --name myapp \
  --relay https://example.com \
  --tls-mode keyless
```

- Keyless signer endpoint, key id, trust roots, and certificate chain are auto-configured by SDK defaults.
- Auto-discovery expects an HTTPS signer endpoint.
- External signer API must return TLS signature responses for the requested digest.

Signer API request/response example:

```json
{
  "key_id": "relay-cert",
  "algorithm": "RSA_PSS_SHA256",
  "digest": "<base64>",
  "timestamp_unix": 1735628400,
  "nonce": "c4d76ad40f5d8f95a1fe4b2f1c922f4a"
}
```

```json
{
  "key_id": "relay-cert",
  "algorithm": "RSA_PSS_SHA256",
  "signature": "<base64>"
}
```

### Non-TLS Mode (Development Only)

```bash
portal-tunnel --host localhost:3000 --name myapp --relay https://example.com
```

**Not recommended for production.** Use TLS mode instead.

- No TLS certificate issued
- HTTP requests proxied through relay on port 4017
- No end-to-end encryption between client and tunnel

## Port Summary

| Port | Service | Description |
|------|---------|-------------|
| 443 | SNI Router | TLS passthrough to tunnel backends |
| 4017 | HTTP Relay | Admin UI, API, HTTP proxy |
| 80 | HTTP | Redirect to HTTPS (optional) |

## Validate

```bash
# Health check
curl https://example.com/healthz
# Expected: {"status":"ok"}

# Domain API
curl https://example.com/sdk/domain
# Expected: {"success":true,"base_domain":"example.com"}

# Tunnel script
curl -fsSL https://example.com/tunnel | HOST=localhost:3000 NAME=test sh
```

## Architecture

```
                    ┌─────────────────────────────────────────────────────┐
                    │                    Portal Relay                      │
                    │                                                     │
    :443 TLS ──────►│  ┌─────────────┐    ┌─────────────────────────────┐ │
                    │  │ SNI Router  │───►│ Tunnel Backend              │ │
                    │  │             │    │ w/ keyless certificate     │ │
                    │  └─────────────┘    └─────────────────────────────┘ │
                    │                                                     │
                    ├─────────────────────────────────────────────────────┤
                    │                                                     │
    :4017 HTTP ────►│  ┌─────────────┐    ┌─────────────────────────────┐ │
                    │  │ HTTP Server │───►│ Tunnel (dev mode, no TLS)   │ │
                    │  │             │    │ or redirect to HTTPS        │ │
                    │  └─────────────┘    └─────────────────────────────┘ │
                    │         │                                           │
                    │         ▼                                           │
                    │  ┌─────────────┐    ┌─────────────┐                 │
                    │  │  Admin UI   │    │    API      │                 │
                    │  │  /admin     │    │   /sdk/*    │                 │
                    │  └─────────────┘    └─────────────┘                 │
                    │                                                     │
                    └─────────────────────────────────────────────────────┘
```

## Troubleshooting

### SNI Router Fails to Start

```bash
# Check if port 443 is in use
sudo netstat -tlnp | grep :443

# Grant capability to bind privileged port
sudo setcap 'cap_net_bind_service=+ep' ./bin/relay-server
```

### TLS Certificate Load Fails

```bash
# Verify self TLS certificate files
ls -l /etc/ssl/myapp/fullchain.pem /etc/ssl/myapp/privkey.pem

# Check logs
docker compose logs portal
```

### Tunnel Cannot Connect

```bash
# Check relay is running
curl https://example.com/healthz

# Run tunnel with verbose logging
portal-tunnel --host localhost:3000 --name test --relay https://example.com --tls-mode keyless
```
