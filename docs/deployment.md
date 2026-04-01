# Portal Relay Deployment Guide

This guide covers the production steps for running Portal Relay on a public domain.

## 1. Prerequisites

You need:

- A public domain, for example `example.com`
- A public Linux server with a static public IPv4
- Docker and Docker Compose
- Optional for managed ACME DNS-01 automation or Portal-managed ENS TXT sync: a supported DNS provider account for `cloudflare` or `route53`
- Open inbound ports:
  - `443/tcp`
  - `4017/tcp`
  - optional for UDP transport:
    - `4017/udp`
    - `50000+/udp` (see section 5)

## 2. Certificate and DNS Mode

Choose one of these modes:

- Manual certificate mode
  - Leave `ACME_DNS_PROVIDER` empty.
  - Place `fullchain.pem` and `privatekey.pem` in `KEYLESS_DIR`.
  - Portal uses the files as-is and does not modify DNS or renew the certificate.
- Manual certificate + gasless mode
  - Place `fullchain.pem` and `privatekey.pem` in `KEYLESS_DIR`.
  - Set `ACME_DNS_PROVIDER`.
  - Portal keeps the manual certificate files, skips ACME certificate issuance, and still uses the provider for DNSSEC + ENS TXT automation.
- Managed ACME mode
  - Set `ACME_DNS_PROVIDER` to `cloudflare` or `route53`.
  - Portal manages root/wildcard A records and certificate renewal.
  - If ENS gasless is enabled, Portal also manages DNSSEC.

If you only need a relay and do not need Portal-managed DNS or automatic renewal, manual certificate mode is the simplest option.

## 3. Managed ACME Provider Setup

### 3.1 Choose ACME DNS provider

Set `ACME_DNS_PROVIDER` to one of:

- `cloudflare`
- `route53`

### 3.2 Cloudflare setup

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
- optional when `ENS_GASLESS_ENABLED=true` and `ACME_DNS_PROVIDER=cloudflare`:
  - `Zone Settings:Edit`

Scope:

- Limit the token to the target zone

Save the token for `CLOUDFLARE_TOKEN`.

### 3.3 Route53 setup

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

When `ENS_GASLESS_ENABLED=true` and `ACME_DNS_PROVIDER=route53` and the hosted zone does not already have an active Route53 key-signing key (KSK), also provide:

- `AWS_DNSSEC_KMS_KEY_ARN`
- optional `DNSSEC_KSK_NAME`

### 3.4 Optional ENS Gasless Automation

Portal can optionally enable ENS gasless DNS import for the base domain and lease hostnames.

- This is not required for normal Portal deployment.
- Enable it only when you specifically need ENS gasless DNS import.
- ENS gasless automation requires `ACME_DNS_PROVIDER`.
- Portal uses that provider for both DNSSEC automation and ENS TXT create/delete.
- If valid manual certificate files already exist in `KEYLESS_DIR`, Portal keeps using them and does not force ACME certificate issuance just because `ACME_DNS_PROVIDER` is set.
- Cloudflare can enable zone signing directly, but some registrars still require publishing the returned DS record.
- Route53 requires a compatible KMS key ARN when no active KSK already exists, and the registrar may still require the DS record.
- New lease hostnames such as `app.portal.example.com` are published automatically when they register and are cleaned up on unregister or expiry.
- ENS gasless import still depends on DNSSEC being valid for the domain.
- By default Portal writes `ENS1 0x238A8F792dFA6033814B18618aD4100654aeef01 <address>`.
- The address is derived automatically from the relay identity for the base domain and from each lease identity for lease hostnames.
- This enables offchain gasless DNSSEC usage in ENS-aware clients. It does not perform an onchain ENS claim transaction.
- Keep `ENS_GASLESS_ENABLED=false` unless you intend to use ENS gasless DNS import.

## 4. Run Relay Server

### 4.1 Create `.env` at repository root

Manual certificate example:

```bash
PORTAL_URL=https://example.com
BOOTSTRAPS=
DISCOVERY=true
IDENTITY_PATH=/portal-certs/identity.json
WIREGUARD_ENDPOINT=
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=/portal-certs
ACME_DNS_PROVIDER=
ENS_GASLESS_ENABLED=false
```

Place these files in `KEYLESS_DIR` before startup:

```text
/portal-certs/fullchain.pem
/portal-certs/privatekey.pem
```

Manual certificate + gasless example:

```bash
PORTAL_URL=https://example.com
BOOTSTRAPS=
DISCOVERY=true
IDENTITY_PATH=/portal-certs/identity.json
WIREGUARD_ENDPOINT=
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=/portal-certs
ACME_DNS_PROVIDER=cloudflare
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
ENS_GASLESS_ENABLED=true
```

In this mode, Portal keeps the manual certificate files but still manages DNSSEC and `ENS1 ...` TXT records through Cloudflare.

Managed Cloudflare example:

```bash
PORTAL_URL=https://example.com
BOOTSTRAPS=
DISCOVERY=true
IDENTITY_PATH=/portal-certs/identity.json
WIREGUARD_ENDPOINT=
SNI_PORT=443
ADMIN_SECRET_KEY=your-admin-secret
KEYLESS_DIR=/portal-certs
ACME_DNS_PROVIDER=cloudflare
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
ENS_GASLESS_ENABLED=false
```

Route53 example:

```bash
IDENTITY_PATH=/portal-certs/identity.json
KEYLESS_DIR=/portal-certs
ACME_DNS_PROVIDER=route53
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
AWS_SESSION_TOKEN=...
AWS_REGION=us-east-1
# Optional override
AWS_HOSTED_ZONE_ID=Z1234567890ABC
# Required only for ENS gasless automation when no ACTIVE KSK already exists.
AWS_DNSSEC_KMS_KEY_ARN=arn:aws:kms:...
# Optional override
DNSSEC_KSK_NAME=portal_ksk
ENS_GASLESS_ENABLED=false
```

Notes:

- For non-apex deployments, set `PORTAL_URL` to the non-apex host value, for example `https://portal.example.com:8443`
- Portal uses the `PORTAL_URL` host for public lease hostnames
- `WIREGUARD_ENDPOINT` is optional. When empty, Portal advertises `PORTAL_URL` host with `DISCOVERY_PORT`
- Set `WIREGUARD_ENDPOINT` explicitly only when relay-peer discovery UDP is exposed on a different address than `PORTAL_URL`
- `IDENTITY_PATH` stores the relay identity JSON inside the container
- `KEYLESS_DIR` stores relay certificate material inside the container
- The Docker Compose stack stores relay identity JSON and certificate state under `./.portal-certs` on the host

If the relay sits behind a reverse proxy or ingress and you want admin/auth and lease IP tracking to use the original client IP, set:

```bash
TRUST_PROXY_HEADERS=true
```

If your proxy source addresses are public or you want a stricter allowlist, also set `TRUSTED_PROXY_CIDRS`.

### 4.2 Start Relay

When using the published Docker image, create the bind-mount directory first and make it writable by UID `65532` (`nonroot` in the distroless image):

```bash
mkdir -p ./.portal-certs
sudo chown 65532:65532 ./.portal-certs
chmod 755 ./.portal-certs
```

If you use manual certificate mode, make sure `fullchain.pem` and `privatekey.pem` already exist in `./.portal-certs` before startup.

Then start the stack:

```bash
docker compose up -d
```

## 5. Optional UDP Setup

UDP transport is disabled by default.

### 5.1 Open UDP ports on your VM or host

Open these UDP ports in your cloud security group or firewall:

- `4017/udp`
- the lease port range starting at `50000`, for example `50000-50009/udp`

UFW example for 10 UDP ports:

```bash
sudo ufw allow 4017/udp
sudo ufw allow 50000:50009/udp
```

### 5.2 Expose UDP ports in Docker

If you use `network_mode: host`, the container uses host UDP ports directly.

If you use bridge networking, map the ports explicitly in `docker-compose.yaml`:

```yaml
ports:
  - "4017:4017/udp"
  - "50000-50009:50000-50009/udp"
```

### 5.3 Configure `UDP_PORT_COUNT`

Set `UDP_PORT_COUNT` in `.env` to the number of UDP leases you want to support.

Example:

```bash
UDP_PORT_COUNT=10
```

That allocates lease UDP ports `50000-50009`.

| Variable | Default | Description |
|---|---|---|
| `UDP_PORT_COUNT` | `0` | Number of UDP ports to allocate, starting at port 50000 |

### 5.4 Enable UDP in the admin panel

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
