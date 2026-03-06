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

- Record 1 (root host)
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

If you deploy on a non-apex host (for example, `PORTAL_URL=https://portal.example.com:8443`), create host-scoped records instead:

- `portal.example.com -> <server-ip>`
- `*.portal.example.com -> <server-ip>`

Portal derives public lease hostnames from the normalized `PORTAL_URL` host.
Requests to the exact root host are not served by the wildcard route; they fall back to the admin/API listener.

### 2.3 Create Cloudflare API Token

Cloudflare Dashboard -> `My Profile` -> `API Tokens` -> `Create Token`.

Grant:

- `Zone:Read`
- `DNS:Edit`

Scope:

- Zone resources limited to your target zone (for example, `example.com`)

Save this token for `CLOUDFLARE_TOKEN`.

## 3. Relay Runtime Behavior

### 3.1 Control Plane and Reverse Sessions

- `/sdk/register` creates a lease and stores the caller-provided reverse token.
- `/sdk/connect` requires:
  - `lease_id` query parameter
  - `X-Portal-Token` header
  - HTTP/1.1
- `/sdk/renew` and `/sdk/unregister` require `lease_id` + `reverse_token`.
- `/sdk/connect` is hijacked into a long-lived reverse TCP session after validation.

### 3.2 Certificates and DNS Maintenance

- Relay certificates live in `KEYLESS_DIR`:
  - `fullchain.pem`
  - `privatekey.pem`
- On non-localhost deployments, ACME DNS-01 uses the Cloudflare token to:
  - ensure root and wildcard A records point to the current public IP
  - provision the relay certificate
  - keep DNS and certificate state refreshed over time

## 4. Run Relay Server

### 4.1 Create `.env` at repository root

```bash
PORTAL_URL=https://example.com
BOOTSTRAP_URIS=https://example.com
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=.portal-certs
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
```

For non-apex deployments, set `PORTAL_URL` and `BOOTSTRAP_URIS` to the same non-apex host value (for example, `https://portal.example.com:8443`).
`PORTAL_URL` path/query segments are ignored for route derivation; only the host component is used.

If the relay sits behind a reverse proxy or ingress and you want admin/auth and lease IP tracking to use the original client IP, set:

```bash
TRUST_PROXY_HEADERS=true
```

By default, forwarded headers are accepted from private, loopback, and link-local proxy source ranges.
If your proxy source addresses are public or you want a stricter allowlist, also set `TRUSTED_PROXY_CIDRS`.

### 4.2 Start Relay

```bash
docker compose up
```

## 5. Troubleshooting

### 5.1 Ports blocked

Required inbound ports:

- `443/tcp`
- `4017/tcp`

UFW example:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw status
```
