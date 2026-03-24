# Portal Relay Deployment Guide

This guide covers production deployment of Portal Relay on a public domain.

## 1. Prerequisites

You need:

- A public domain (example: `example.com`)
- A public Linux server with a static public IP
- Open inbound ports: `443/tcp`, `4017/tcp`
- Optional UDP ports (if enabling UDP transport): `4017/udp`, `50000+/udp` (see section 3.2)
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

Portal derives public lease hostnames from the normalized `PORTAL_URL` host.
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

### 3.2 Enabling UDP Transport

UDP transport is disabled by default. To enable UDP for real-time workloads (game servers, VoIP), complete all steps:

**Step 1: Open UDP ports on your VM/host**

If running on a cloud VM (AWS EC2, GCP, OCI, etc.), open the required UDP ports in the security group / firewall rules:
- `4017/udp` — QUIC tunnel listener (relay ↔ tunnel)
- `50000-50009/udp` — Raw UDP lease ports (adjust count to match `UDP_PORT_COUNT`)

Example (UFW, 10 ports):
```bash
sudo ufw allow 4017/udp
sudo ufw allow 50000:50009/udp
```

**Step 2: Expose UDP ports in Docker**

If using Docker with `network_mode: host`, UDP ports are directly accessible on the host — no additional Docker config needed.

If using bridge networking, map the UDP ports explicitly in `docker-compose.yaml`:
```yaml
ports:
  - "4017:4017/udp"
  - "50000-50009:50000-50009/udp"
```

**Step 3: Configure UDP port count in `.env`**

Set `UDP_PORT_COUNT` to the number of concurrent UDP leases you want to support. Ports are allocated starting from port 50000:
```bash
UDP_PORT_COUNT=10   # allocates ports 50000-50009
```

| Variable | Default | Description |
|---|---|---|
| `UDP_PORT_COUNT` | `0` (disabled) | Number of UDP ports to allocate, starting at port 50000 |

**Step 4: Enable UDP in the admin panel**

Navigate to `/admin`, toggle UDP transport to "Enabled", and optionally set a max concurrent UDP lease limit.

> **Docker note:** Use `network_mode: host` for the portal container to avoid Docker iptables port-mapping overhead. Docker creates one iptables rule per mapped port, so large UDP ranges cause very slow container start/stop. Host networking bypasses this entirely and allows dynamic UDP port allocation. See the nginx-proxy examples for the recommended setup.

> **UDP buffer tuning (Linux):** Increase kernel UDP buffer limits for QUIC performance:
> ```bash
> sudo sysctl -w net.core.rmem_max=7500000
> sudo sysctl -w net.core.wmem_max=7500000
> ```
> To persist across reboots, add to `/etc/sysctl.conf` or a file in `/etc/sysctl.d/`.

### 3.3 Certificates and DNS Maintenance

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
BOOTSTRAPS=
DISCOVERY=true
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

For non-apex deployments, set `PORTAL_URL` to the non-apex host value (for example, `https://portal.example.com:8443`).
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

- `443/tcp` — SNI router (tenant TLS passthrough)
- `4017/tcp` — Admin/API listener
- `4017/udp` — QUIC tunnel listener (only if `UDP_PORT_COUNT > 0`)
- `50000+/udp` — Raw UDP lease ports (only if `UDP_PORT_COUNT > 0`, adjust range to match count)

UFW example (with 10 UDP ports):

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw allow 4017/udp
sudo ufw allow 50000:50009/udp
sudo ufw status
```

### 6.2 QUIC UDP buffer warnings

If relay logs show `failed to sufficiently increase receive buffer size`, the kernel UDP buffer limit is too low. Apply the sysctl settings from section 3.2.
