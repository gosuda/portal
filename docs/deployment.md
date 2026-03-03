# Portal Relay Deployment Guide

This guide covers production-style deployment of Portal Relay on a public domain.
It includes DNS setup, environment configuration, startup, validation, and basic operations.

## 1. Prerequisites

You need:

- A public domain (example: `example.com`)
- A public Linux server with a static public IP
- Open inbound ports: `443/tcp`, `4017/tcp`
- Docker and Docker Compose
- Cloudflare-managed DNS zone for your domain (required for automatic ACME DNS-01 flow)

## 2. DNS and Cloudflare Setup

### 2.1 Add Domain to Cloudflare

1. Cloudflare Dashboard -> `Websites` -> `Add a Site`
2. Enter your domain (`example.com`)
3. Complete onboarding and apply Cloudflare nameservers at your registrar
4. Wait until zone status is `Active`

### 2.2 Create DNS Records

Cloudflare Dashboard -> `DNS` -> `Records`:

- Record 1 (apex)
  - Type: `A`
  - Name: `@`
  - Content: `<server-ip>`
  - Proxy status: `DNS only`
- Record 2 (wildcard)
  - Type: `A`
  - Name: `*`
  - Content: `<server-ip>`
  - Proxy status: `DNS only`

Expected:

- `example.com -> <server-ip>`
- `*.example.com -> <server-ip>`

### 2.3 Create Cloudflare API Token

Cloudflare Dashboard -> `My Profile` -> `API Tokens` -> `Create Token`.

Grant:

- `Zone:Read`
- `DNS:Edit`

Scope:

- Zone resources limited to your target zone (for example, `example.com`)

Save this token for `CLOUDFLARE_TOKEN`.

## 3. Run Relay Server

### 3-1. Create `.env` at repository root:

```bash
PORTAL_URL=https://example.com
BOOTSTRAP_URIS=https://example.com
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=/etc/portal/keyless
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
```

### 3-2. Start Relay

```bash
docker compose up
```

## 4. Troubleshooting

### 4.1 Ports blocked

Required inbound:

- `443/tcp`
- `4017/tcp`

UFW example:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw status
```
