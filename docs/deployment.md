# Portal Relay Deployment Guide

This guide covers the production steps for running Portal Relay on a public domain.

## 1. Prerequisites

You need:

- A public domain, for example `example.com`
- A public Linux server with a static public IPv4
- Docker and Docker Compose
- A supported DNS provider account for ACME DNS-01 automation: `cloudflare` or `route53`
- Open inbound ports:
  - `443/tcp`
  - `4017/tcp`
  - optional for UDP transport:
    - `4017/udp`
    - `50000+/udp` (see section 4)

## 2. DNS Provider Setup

### 2.1 Choose ACME DNS provider

Set `ACME_DNS_PROVIDER` to one of:

- `cloudflare`
- `route53`

### 2.2 Cloudflare setup

#### Add domain to Cloudflare

1. Cloudflare Dashboard -> `Websites` -> `Add a Site`
2. Enter your domain, for example `example.com`
3. Complete onboarding and apply Cloudflare nameservers at your registrar
4. Wait until zone status is `Active`

#### Create DNS records

If `PORTAL_URL=https://example.com`, create:

- `example.com -> <server-ip>`
- `*.example.com -> <server-ip>`

If you deploy on a non-apex host such as `PORTAL_URL=https://portal.example.com:8443`, create:

- `portal.example.com -> <server-ip>`
- `*.portal.example.com -> <server-ip>`

Set both records as:

- Type: `A`
- Proxy status: `DNS only`

#### Create Cloudflare API token

Cloudflare Dashboard -> `My Profile` -> `API Tokens` -> `Create Token`

Required permissions:

- `Zone:Read`
- `DNS:Edit`

Scope:

- Limit the token to the target zone

Save the token for `CLOUDFLARE_TOKEN`.

### 2.3 Route53 setup

Create or select a public hosted zone that covers your relay host.

Provide Route53 write access through either:

- static AWS credentials, or
- ambient AWS credentials such as an instance role

Static credential environment variables:

- `AWS_ACCESS_KEY_ID`
- `AWS_SECRET_ACCESS_KEY`
- optional `AWS_SESSION_TOKEN`
- `AWS_REGION`, for example `us-east-1`

Optional:

- `AWS_HOSTED_ZONE_ID`

Equivalent relay flags:

- `--aws-access-key-id`
- `--aws-secret-access-key`
- `--aws-session-token`
- `--aws-region`
- `--aws-hosted-zone-id`

## 3. Run Relay Server

### 3.1 Create `.env` at repository root

Example:

```bash
PORTAL_URL=https://example.com
BOOTSTRAPS=
DISCOVERY=true
WIREGUARD_ENDPOINT=
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

Notes:

- For non-apex deployments, set `PORTAL_URL` to the non-apex host value, for example `https://portal.example.com:8443`
- Portal uses the `PORTAL_URL` host for public lease hostnames
- `WIREGUARD_ENDPOINT` is optional. When empty, Portal advertises `PORTAL_URL` host with `DISCOVERY_PORT`
- Set `WIREGUARD_ENDPOINT` explicitly only when relay-peer discovery UDP is exposed on a different address than `PORTAL_URL`
- `KEYLESS_DIR` stores relay certificate material

If the relay sits behind a reverse proxy or ingress and you want admin/auth and lease IP tracking to use the original client IP, set:

```bash
TRUST_PROXY_HEADERS=true
```

If your proxy source addresses are public or you want a stricter allowlist, also set `TRUSTED_PROXY_CIDRS`.

### 3.2 Start Relay

```bash
docker compose up -d
```

## 4. Optional UDP Setup

UDP transport is disabled by default.

### 4.1 Open UDP ports on your VM or host

Open these UDP ports in your cloud security group or firewall:

- `4017/udp`
- the lease port range starting at `50000`, for example `50000-50009/udp`

UFW example for 10 UDP ports:

```bash
sudo ufw allow 4017/udp
sudo ufw allow 50000:50009/udp
```

### 4.2 Expose UDP ports in Docker

If you use `network_mode: host`, the container uses host UDP ports directly.

If you use bridge networking, map the ports explicitly in `docker-compose.yaml`:

```yaml
ports:
  - "4017:4017/udp"
  - "50000-50009:50000-50009/udp"
```

### 4.3 Configure `UDP_PORT_COUNT`

Set `UDP_PORT_COUNT` in `.env` to the number of UDP leases you want to support.

Example:

```bash
UDP_PORT_COUNT=10
```

That allocates lease UDP ports `50000-50009`.

| Variable | Default | Description |
|---|---|---|
| `UDP_PORT_COUNT` | `0` | Number of UDP ports to allocate, starting at port 50000 |

### 4.4 Enable UDP in the admin panel

After the relay starts, open `/admin`, enable UDP transport, and optionally set a max concurrent UDP lease limit.

### 4.5 Optional Linux UDP buffer tuning

For better QUIC performance on Linux:

```bash
sudo sysctl -w net.core.rmem_max=7500000
sudo sysctl -w net.core.wmem_max=7500000
```

To persist this across reboots, add the values to `/etc/sysctl.conf` or a file in `/etc/sysctl.d/`.

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
sudo systemctl status portal-watcher
sudo journalctl -u portal-watcher -f
sudo journalctl -u portal-watcher --since today
```

## 6. Troubleshooting

### 6.1 Ports blocked

Required inbound ports:

- `443/tcp`
- `4017/tcp`
- optional for UDP:
  - `4017/udp`
  - `50000+/udp` matching `UDP_PORT_COUNT`

UFW example with 10 UDP ports:

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw allow 4017/udp
sudo ufw allow 50000:50009/udp
sudo ufw status
```

### 6.2 QUIC UDP buffer warnings

If relay logs show `failed to sufficiently increase receive buffer size`, apply the sysctl settings from section 4.5.

### 6.3 Docker DNS resolution fails

If logs show `discover bootstraps failed`, `sync dns records`, or `lookup <host> on 127.0.0.11:53: write: operation not permitted`, Docker is usually using the wrong host resolver config.

On Linux hosts with `systemd-resolved`, point `/etc/resolv.conf` at the upstream resolver list and restart Docker:

```bash
sudo ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
sudo systemctl restart docker
docker compose up -d
```

Verify from the container:

```bash
docker exec -it portal-1 nslookup api4.ipify.org
```
