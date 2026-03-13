# Portal Relay Deployment Guide

This guide covers deploying Portal Relay on a public domain with Docker Compose.

## 1. Prerequisites

You need:

- A public domain
- A Linux server with a public IPv4 address (inbound 443, 4017 port)
- Docker and Docker Compose
- `cloudflare` or `route53` credentials for ACME DNS-01

## 2. DNS Provider and `.env` Setup

`PORTAL_URL` sets the relay root host. Portal automatically manages the `A` records and certificates for that host and its wildcard.

### 2.1 Cloudflare

1. Add your domain to Cloudflare and wait until the zone is `Active`.
2. Create an API token with:
   - `Zone:Read`
   - `DNS:Edit`
   - scope limited to the target zone
3. Create `.env`:

```bash
PORTAL_URL=https://example.com
BOOTSTRAP_URIS=https://example.com
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=./.portal-certs
ACME_DNS_PROVIDER=cloudflare
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
```

### 2.2 Route53

1. Create or select a public hosted zone for the `PORTAL_URL` host.
2. Prepare AWS credentials with Route53 access.
3. Optional: set `AWS_HOSTED_ZONE_ID` to pin Portal to one hosted zone explicitly.
4. Create `.env`:

```bash
PORTAL_URL=https://example.com
BOOTSTRAP_URIS=https://example.com
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=./.portal-certs
ACME_DNS_PROVIDER=route53
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
AWS_SESSION_TOKEN=...
AWS_REGION=us-east-1
AWS_HOSTED_ZONE_ID=Z1234567890ABC
```

## 3. Start the Relay

```bash
docker compose up -d
```

Then open the relay root host in a browser or check `/healthz` once DNS and certificate provisioning are ready.

### 3.1 Advanced

Use the example folders under `examples/` as the source of truth for deployment layouts and automation:

- Single-service nginx reverse proxy and deployment automation: [examples/nginx-proxy](examples/nginx-proxy)
- Multi-service nginx reverse proxy: [examples/nginx-proxy-multi-service](examples/nginx-proxy-multi-service)

## 4. Troubleshooting

### 4.1 Firewall

Required inbound ports:

- `443/tcp`
- `4017/tcp`

UFW example:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw status
```

### 4.2 DNS and Certificate Checks

- Make sure `PORTAL_URL` matches the host and hosted zone you expect Portal to manage.
- Make sure the configured Cloudflare token or AWS credentials can update DNS for that host.
- For Cloudflare, make sure the target zone is `Active`.
- If certificate issuance is still in progress, watch `docker compose logs -f` until ACME completes.

### 4.3 Non-Apex and Proxy Setups

- For non-apex deployments, set `PORTAL_URL` and `BOOTSTRAP_URIS` to the same non-apex host value such as `https://portal.example.com:8443`.
- `KEYLESS_DIR` stores the relay certificate material as `fullchain.pem` and `privatekey.pem`.
- If the relay sits behind a reverse proxy or ingress and you want admin/auth and lease IP tracking to use forwarded client addresses, set:

```bash
TRUST_PROXY_HEADERS=true
```

- If your trusted proxy source addresses are public or you want a stricter allowlist, also set `TRUSTED_PROXY_CIDRS`.
