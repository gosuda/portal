# Portal Deploy Guide

How to run a public Portal relay with TLS Passthrough and ACME DNS-01.

## Overview

Portal uses SNI-based TLS passthrough: the relay routes TLS connections by SNI to tunnel backends, and each tunnel gets its own certificate via ACME DNS-01.

```
Client ──TLS──► Relay (SNI Router :443) ──TLS──► Tunnel Backend (TLS mode)
                       │
                       └── ACME DNS-01 issues cert for each tunnel
```

## Prerequisites

- A public server (VPS / cloud VM / on-prem with port forwarding)
- A domain you can manage
- Ports 80, 443, and 4017 open to the Internet
- DNS A/AAAA records pointing to your server:
  - `example.com -> <server IP>`
  - `*.example.com -> <server IP>`
- DNS provider API credentials (Cloudflare or Route53) for ACME DNS-01

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

# ACME (Cloudflare example)
ACME_DNS_PROVIDER=cloudflare
ACME_EMAIL=admin@example.com
CLOUDFLARE_API_TOKEN=your-cloudflare-api-token
```

### 3. Run Portal

```bash
docker compose up -d
```

### 4. Run Tunnel

```bash
portal-tunnel --host localhost:3000 --name myapp --relay https://example.com --tls
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

### ACME Certificate Management

| Variable | Description |
|----------|-------------|
| `ACME_DNS_PROVIDER` | `cloudflare` or `route53` |
| `ACME_EMAIL` | Email for ACME registration |
| `ACME_DIRECTORY` | ACME directory URL (default: Let's Encrypt) |

### DNS Provider Credentials

**Cloudflare:**
```bash
CLOUDFLARE_API_TOKEN=your-api-token
```

**Route53:**
```bash
AWS_ACCESS_KEY_ID=your-access-key
AWS_SECRET_ACCESS_KEY=your-secret-key
AWS_REGION=us-east-1
```

## docker-compose.yml

```yaml
services:
  portal:
    image: ghcr.io/gosuda/portal:1
    environment:
      PORTAL_URL: ${PORTAL_URL}
      ADMIN_SECRET_KEY: ${ADMIN_SECRET_KEY}
      ACME_DNS_PROVIDER: ${ACME_DNS_PROVIDER}
      ACME_EMAIL: ${ACME_EMAIL}
      CLOUDFLARE_API_TOKEN: ${CLOUDFLARE_API_TOKEN}
    ports:
      - "4017:4017"
      - "443:443"
      - "80:80"
    restart: unless-stopped
```

## Tunnel Client

### TLS Mode (Production)

```bash
portal-tunnel --host localhost:3000 --name myapp --relay https://example.com --tls
```

- `--host`: Local service address
- `--name`: Subdomain name (becomes `myapp.example.com`)
- `--relay`: Portal relay URL
- `--tls`: Enable TLS

How it works:
1. Tunnel generates private key and CSR locally
2. Sends CSR to relay via `/api/csr`
3. Relay issues certificate via ACME DNS-01
4. TLS connections go directly to tunnel on port 443

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
curl https://example.com/api/domain
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
                    │  │             │    │ w/ ACME certificate         │ │
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
                    │  │  /admin     │    │   /api/*    │                 │
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

### ACME Certificate Issuance Fails

```bash
# Verify credentials
echo $CLOUDFLARE_API_TOKEN
echo $ACME_EMAIL

# Check logs
docker compose logs portal | grep -i acme
```

### Tunnel Cannot Connect

```bash
# Check relay is running
curl https://example.com/healthz

# Run tunnel with verbose logging
portal-tunnel --host localhost:3000 --name test --relay https://example.com --tls
```
