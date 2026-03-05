# Portal Relay Deployment Guide

This guide covers production deployment of Portal Relay on a public domain.

## 1. Prerequisites

You need:

- A public domain (example: `example.com`)
- A public Linux server with a static public IP
- Open inbound ports: `443/tcp`, `4017/tcp`
- Docker and Docker Compose
- A Cloudflare-managed DNS zone (required for ACME DNS-01 automation)

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

Expected records:

- `example.com -> <server-ip>`
- `*.example.com -> <server-ip>`

If you deploy on a non-apex host (for example, `PORTAL_URL=https://portal.example.com:8443`), create host-scoped records:

- `portal.example.com -> <server-ip>`
- `*.portal.example.com -> <server-ip>`

Portal normalizes `PORTAL_URL` to its host for routing, so public service hosts become `<lease>.portal.example.com`.
Requests to the exact `PORTAL_URL` host (for example, `portal.example.com`) are not wildcard-matched; the router uses no-route fallback and forwards them to the admin/API listener.
Relay/tunnel traffic for reverse admission stays raw TCP on `/sdk/connect`.

### 2.4 Control-Plane Admission (Token-Only)

- `/sdk/register`, `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` use token-based admission.
- Control-plane admission order is fixed: `IP -> Lease -> Token`.
- Clients without valid lease token are rejected.

### 2.3 Create Cloudflare API Token

Cloudflare Dashboard -> `My Profile` -> `API Tokens` -> `Create Token`.

Grant:

- `Zone:Read`
- `DNS:Edit`

Scope:

- Zone resources limited to your target zone (for example, `example.com`)

Save this token for `CLOUDFLARE_TOKEN`.

## 3. Run Relay Server

### 3.1 Create `.env` at repository root

```bash
PORTAL_URL=https://example.com
BOOTSTRAP_URIS=https://example.com
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=/etc/portal/keyless
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
```

For non-apex deployments, set `PORTAL_URL` and `BOOTSTRAP_URIS` to the same non-apex host value (for example, `https://portal.example.com:8443`). Keep any path segments only for dashboard use, not for routing.
`PORTAL_URL` path/query segments are ignored for route derivation; only the host component is used.

### 3.2 Start Relay

```bash
docker compose up
```

## 4. Troubleshooting

### 4.1 Ports blocked

Required inbound ports:

- `443/tcp`
- `4017/tcp`

UFW example:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw status
```
