# Portal Deploy Guide

How to run a public Portal relay with DNS, TLS, and wildcard subdomains.

Portal does NOT manage TLS, certificates, or DNS. You must place an HTTPS reverse proxy or TLS terminator in front of Portal.

## Prerequisites
- A public server (VPS / cloud VM / on-prem with port forwarding)
- A domain you can manage
- Ports 80 and 443 open to the Internet
- DNS A/AAAA records pointing to your server:
  - `yourdomain.com -> <server IP>`
  - `*.yourdomain.com -> <server IP>`

## TLS & Wildcard Certificates
Portal requires a single wildcard TLS certificate for all app subdomains (`*.yourdomain.com`).

- Wildcard certificates require DNS-01.
  - HTTP-01/TLS-ALPN-01 will not work for `*.` names.
- Use any ACME client that supports DNS-01 (reverse proxy or standalone).
- You must provide DNS credentials so the ACME client can create TXT records at
  - `_acme-challenge.yourdomain.com`.

## Environment (docker compose)
- Set these for public deployment (via `environment:`):
  ```
  PORTAL_PORT=4017
  PORTAL_URL=https://yourdomain.com
  BOOTSTRAP_URIS=https://yourdomain.com
  ```

## Deploy
- Run Portal (e.g., `docker compose up -d`) exposing 4017 internally.
- Place an HTTPS reverse proxy in front, terminate TLS with your wildcard cert, and route `yourdomain.com` / `*.yourdomain.com` to Portal on 4017.
- Supply your DNS API credentials to the ACME client so DNS-01 can obtain/renew the wildcard cert.

## Validate
- Health: `curl -vk https://yourdomain.com/healthz` → `{"status":"ok"}`.
- Tunnel script fetch: `curl -fsSL https://yourdomain.com/tunnel | head`.
