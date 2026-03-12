# Portal Relay Deployment Guide

This guide covers production deployment of Portal Relay on a public domain.

## 1. Prerequisites

You need:

- A public domain (example: `example.com`)
- A public Linux server with a static public IP
- Open inbound ports: `443/tcp`, `4017/tcp`
- Docker and Docker Compose
- A DNS provider account for ACME DNS-01 automation with a supported provider (`cloudflare` or `route53`)

## 2. DNS Provider Setup

### 2.1 Choose ACME DNS provider

Set `ACME_DNS_PROVIDER` to one of the currently supported values:

- `ACME_DNS_PROVIDER=cloudflare`, or
- `ACME_DNS_PROVIDER=route53`

Both providers keep root and wildcard A records synchronized to the relay public IPv4 and use DNS-01 for certificate issuance.

### 2.2 Cloudflare setup (`ACME_DNS_PROVIDER=cloudflare`)

#### Add domain to Cloudflare

1. Cloudflare Dashboard -> `Websites` -> `Add a Site`
2. Enter your domain (`example.com`)
3. Complete onboarding and apply Cloudflare nameservers at your registrar
4. Wait until zone status is `Active`

#### Create DNS records

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

Portal's relay DNS and certificates cover the normalized `PORTAL_URL` host and its wildcard, and public lease hostnames are published as `<name>.<root host>`.
Requests to the exact root host are not served by the wildcard route; they fall back to the admin/API listener.

#### Create Cloudflare API token

Cloudflare Dashboard -> `My Profile` -> `API Tokens` -> `Create Token`.

Grant:

- `Zone:Read`
- `DNS:Edit`

Scope:

- Zone resources limited to your target zone (for example, `example.com`)

Save this token for `CLOUDFLARE_TOKEN`.

### 2.3 Route53 setup (`ACME_DNS_PROVIDER=route53`)

Create or select a public hosted zone that covers your `PORTAL_URL` root host and provide Route53 write permissions through either static AWS credentials or ambient AWS credentials (for example, an instance role).

Static credential environment variables:

- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`
- Optional `AWS_SESSION_TOKEN` for temporary credentials
- `AWS_REGION` (for example, `us-east-1`)

Optional:

- `AWS_HOSTED_ZONE_ID` (when omitted, relay selects a matching public hosted zone by domain suffix)

Equivalent relay flags:

- `--aws-access-key-id`
- `--aws-secret-access-key`
- `--aws-session-token`
- `--aws-region`
- `--aws-hosted-zone-id`

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
- On non-localhost deployments, ACME DNS-01 uses the configured supported DNS provider to:
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
KEYLESS_DIR=./.portal-certs
ACME_DNS_PROVIDER=cloudflare
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
```

Route53 example:

```bash
KEYLESS_DIR=./.portal-certs
ACME_DNS_PROVIDER=route53
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
AWS_SESSION_TOKEN=...
AWS_REGION=us-east-1
# Optional override
AWS_HOSTED_ZONE_ID=Z1234567890ABC
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

## 5. Auto-Update

Automatically redeploy when a new `ghcr.io/gosuda/portal:latest` image is pushed.

### 5.1 Deploy script

Create `deploy_portal.sh` in your project directory:

```bash
#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

docker compose pull
docker compose up -d
docker image prune -f
```

### 5.2 Watcher script

The repository includes `watch_and_deploy.sh`, which polls the remote image digest and runs the deploy script on change.

Environment variables:

| Variable | Default | Description |
|---|---|---|
| `INTERVAL` | `60` | Poll interval in seconds |
| `DEPLOY_SCRIPT` | `deploy_portal.sh` | Path to deploy script |
| `DIGEST_FILE` | `.portal_image_digest` | File storing the last known digest |

### 5.3 Register as systemd service

Set `WorkingDirectory` and `ExecStart` to the directory where `watch_and_deploy.sh` and `deploy_portal.sh` are located:

```bash
sudo tee /etc/systemd/system/portal-watcher.service << 'EOF'
[Unit]
Description=Portal Docker Image Watcher
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service

[Service]
Type=simple
User=opc
# Set to the directory containing watch_and_deploy.sh and deploy_portal.sh
WorkingDirectory=<path-to-project>
ExecStart=/bin/bash <path-to-project>/watch_and_deploy.sh
Restart=always
RestartSec=10
Environment=INTERVAL=60
Environment=DEPLOY_SCRIPT=deploy_portal.sh

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now portal-watcher
```

Adjust `User` to match your environment. Ensure the user belongs to the `docker` group:

```bash
sudo usermod -aG docker opc
```

### 5.4 Verify and monitor

```bash
# Service status
sudo systemctl status portal-watcher

# Live logs
sudo journalctl -u portal-watcher -f

# Today's logs only
sudo journalctl -u portal-watcher --since today
```

## 6. Troubleshooting

### 6.1 Ports blocked

Required inbound ports:

- `443/tcp`
- `4017/tcp`

UFW example:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw status
```
