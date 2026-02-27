# Portal Deploy Guide

How to run a public Portal relay with TLS passthrough and keyless TLS signing.

## Overview

Portal uses SNI-based TLS passthrough: the relay routes TLS connections by SNI to tunnel backends, tunnels keep public cert chains, and CertificateVerify signatures are delegated to the relay signer.

```
Client ──TLS──► Relay (SNI Router :443) ──TLS──► Tunnel Backend (TLS mode)
                       │
                       └── Keyless signer holds private key
```

## Prerequisites

- A public server (VPS / cloud VM / on-prem with port forwarding)
- A domain you can manage
- Ports 80, 443, and 4017 open to the Internet
- DNS A/AAAA records pointing to your server:
  - `example.com -> <server IP>`
  - `*.example.com -> <server IP>`
- Relay signer TLS cert/key, signing private key, and trust bundle (root CA)

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

# Keyless signer
SIGNER_TLS_CERT_FILE=/etc/portal/signer/server.crt
SIGNER_TLS_KEY_FILE=/etc/portal/signer/server.key
SIGNER_SIGNING_KEY_FILE=/etc/portal/signer/signing.key
SIGNER_CERT_CHAIN_FILE=/etc/portal/signer/public-chain.crt
SIGNER_ROOT_CA_FILE=/etc/portal/signer/root-ca.crt
SIGNER_ENDPOINT=example.com:9443
SIGNER_SERVER_NAME=example.com
SIGNER_KEY_ID=portal-wildcard
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

### Keyless Signer

| Variable | Description |
|----------|-------------|
| `SIGNER_TLS_CERT_FILE` | Signer HTTPS server certificate PEM path |
| `SIGNER_TLS_KEY_FILE` | Signer HTTPS server private key PEM path |
| `SIGNER_SIGNING_KEY_FILE` | Keyless signing private key PEM path |
| `SIGNER_CERT_CHAIN_FILE` | Public cert chain PEM delivered to tunnel SDK |
| `SIGNER_ROOT_CA_FILE` | Root CA PEM delivered to tunnel SDK |
| `SIGNER_ENDPOINT` | Public signer endpoint (host:port or URL) |
| `SIGNER_SERVER_NAME` | TLS server name used by tunnel signer client |
| `SIGNER_KEY_ID` | Key identifier resolved by signer key store |
| `SIGNER_ENABLE_MTLS` | Require tunnel client certificates for signer |
| `SIGNER_CLIENT_CA_FILE` | Client CA PEM for signer mTLS verification |
| `SIGNER_CLIENT_CERT_FILE` | Optional tunnel client cert PEM to distribute via API |
| `SIGNER_CLIENT_KEY_FILE` | Optional tunnel client key PEM to distribute via API |

## docker-compose.yml

```yaml
services:
  portal:
    image: ghcr.io/gosuda/portal:1
    environment:
      PORTAL_URL: ${PORTAL_URL}
      ADMIN_SECRET_KEY: ${ADMIN_SECRET_KEY}
      SIGNER_TLS_CERT_FILE: ${SIGNER_TLS_CERT_FILE}
      SIGNER_TLS_KEY_FILE: ${SIGNER_TLS_KEY_FILE}
      SIGNER_SIGNING_KEY_FILE: ${SIGNER_SIGNING_KEY_FILE}
      SIGNER_CERT_CHAIN_FILE: ${SIGNER_CERT_CHAIN_FILE}
      SIGNER_ROOT_CA_FILE: ${SIGNER_ROOT_CA_FILE}
      SIGNER_ENDPOINT: ${SIGNER_ENDPOINT}
      SIGNER_SERVER_NAME: ${SIGNER_SERVER_NAME}
      SIGNER_KEY_ID: ${SIGNER_KEY_ID}
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
1. Tunnel requests keyless config via `/sdk/keyless/config`
2. Tunnel builds TLS config from cert chain + remote signer settings
3. Relay signer performs CertificateVerify signatures with private key in signer store
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

# Keyless config API (requires lease credentials)
curl -X POST https://example.com/sdk/keyless/config

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
