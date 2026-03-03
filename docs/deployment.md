# Portal Relay Deploy Guide

## Prerequisites

- Public domain (example: `example.com`)
- Public server with public IP
- Docker, Docker Compose

## Quick Start

### 1. Register Domain in Cloudflare

In Cloudflare Dashboard:
1. Go to `Websites`
2. Click `Add a Site`
3. Enter your domain (example: `example.com`)
4. Select a plan and complete setup
5. Update nameservers at your registrar to the nameservers provided by Cloudflare
6. Confirm the zone status is `Active`

### 2. Register DNS Records

In Cloudflare Dashboard:
1. Open your domain dashboard (`example.com`)
2. Go to `DNS` -> `Records`
3. Click `Add record`

Create record 1 (root domain):
- Type: `A`
- Name: `@`
- IPv4 address: `<server-ip>`
- Proxy status: `DNS only`
- Save

Create record 2 (wildcard subdomain):
- Type: `A`
- Name: `*`
- IPv4 address: `<server-ip>`
- Proxy status: `DNS only`
- Save

Example result:
- `example.com -> <server-ip>`
- `*.example.com -> <server-ip>`

### 3. Create Cloudflare API Token

Create token in Cloudflare Dashboard:
1. `My Profile` -> `API Tokens` -> `Create Token`
2. Custom permissions:
   - `Zone:Read`
   - `DNS:Edit`
3. Zone resources: include your target zone (`example.com`)
4. Copy and save the token value (you will use it as `CLOUDFLARE_TOKEN`)

### 4. Run the Relay Server

Create `.env` in repository root:

```bash
PORTAL_URL=https://example.com
ADMIN_SECRET_KEY=your-admin-key
SNI_PORT=443
KEYLESS_DIR=/etc/portal/keyless
CLOUDFLARE_TOKEN=cf_xxxxxxxxxxxxxxxxx
```

Run Docker Compose

```bash
docker compose up -d
docker compose logs -f
```

## Troubleshooting

### Inbound ports are blocked

Required inbound ports:
- `443/tcp`
- `4017/tcp`

Linux example (UFW):

```bash
sudo ufw allow 443/tcp
sudo ufw allow 4017/tcp
sudo ufw status
```
