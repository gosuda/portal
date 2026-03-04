# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.png" alt="Portal logo" width="540" />
</p>

Expose your local application to the public internet — no ports, no NAT, no DNS setup.

Portal is a self-hosted relay network. You can run your own relay or connect to one that is already running.

## Why Portal?

Publishing a local service usually requires:

- Opening inbound ports
- Configuring NAT or firewall rules
- Managing DNS records
- Terminating TLS at a gateway

Portal removes most of this setup by inverting the connection model.
Applications establish outbound connections to a relay.
The relay runs on a public base domain, assigns each service a subdomain,
and routes incoming traffic while preserving end-to-end TLS.

## Features

- **NAT-friendly connectivity**: Works behind NAT or firewalls without opening inbound ports
- **Automatic subdomain routing**: Gives each app its own subdomain (`your-app.<base-domain>`)
- **Non-apex `PORTAL_URL` friendly**: Route hosts are derived from the full portal host (e.g., `https://portal.example.com:8443` -> `portal.example.com`), so services become `<name>.portal.example.com`
- **End-to-end encryption**: Supports TLS passthrough with relay keyless certificates
- **Self-hosted by design**: You can run your own Portal relay
- **Fast setup**: Expose a local app with a short command flow
- **Central anti-abuse enforcement**: `/sdk/register` and `/sdk/connect` use the same admin-managed policy controls (IP bans, lease authorization) before accepting a tunnel

## Components

- **Relay**: A server that routes public requests to the right connected app.
- **Tunnel**: A CLI agent that proxies your local app through the relay.

For details, see [docs/glossary.md](docs/glossary.md).

## Protocol Scope

- Raw TCP reverse-connect is the only supported relay/tunnel transport.
- Websocket transport is unsupported for relay/tunnel traffic.

## Connection Model

- Conn #1 (`browser -> app`) is the data plane and keeps existing tenant-facing TLS behavior.
- Conn #2 (`relay -> tunnel`) is the control plane and requires lease-bound client mTLS identity on `/sdk/register`, `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister`.

## Runtime Contracts

- Lease IDs in admin and SDK payloads are plain string IDs.
- Base64URL lease-ID encoding is used only for admin action route path segments (`/admin/leases/{encodedLeaseID}/{action}`).
- Control-plane admission order is strict: `IP -> Lease -> CertBind -> Token`.
- Tunnel installer scripts always fetch `${BIN_URL}.sha256` and fail closed on missing, malformed, or mismatched checksum.

## Control-Plane Upgrade Requirement

- This release wave is a hard-break for control-plane identity.
- Clients without valid lease-bound mTLS identity fail deterministically at control-plane admission.
- There is no token-only fallback mode after cutover.

### Routing Notes

- SNI routing preserves an exact-match fallback for the portal root host. Requests that target the exact `PORTAL_URL` host (for example, `portal.example.com`) are handled by the admin/API listener via the no-route path.

## Quick Start

### Run Portal Relay

```bash
git clone https://github.com/gosuda/portal
cd portal
docker compose up
```

Set `PORTAL_URL` to your public domain. If `ADMIN_SECRET_KEY` is not set, one is auto-generated and logged at startup. Set `CLOUDFLARE_TOKEN` to enable automatic ACME DNS-01 certificate provisioning (required only when using Cloudflare for DNS).

The compose file exposes port 443 (SNI routing) and 4017 (admin/API). The SNI port requires a wildcard DNS record (`*.<base-domain>`) pointing to the relay host.

For deployment to a public domain, see [docs/deployment.md](docs/deployment.md).

### Expose Local Service via Tunnel

1. Run your local service.
2. Open the Portal relay site.
3. Click `Add your server` button.
4. Use the generated command to connect your local service.

### Use the Go SDK (Advanced)

See [portal-toys](https://github.com/gosuda/portal-toys) for more examples.

## Relay Server Configuration

| Flag | Env Var | Default | Description |
| --- | --- | --- | --- |
| `--adminport` | — | `4017` | Admin/API HTTP(S) port |
| `--admin-secret-key` | `ADMIN_SECRET_KEY` | auto-generated | Admin auth secret (auto-generated and logged if not set) |
| `--portal-url` | `PORTAL_URL` | `https://localhost:4017` | Portal base URL |
| `--bootstraps` | `BOOTSTRAP_URIS` | derived from `PORTAL_URL` | Comma-separated relay API URLs |
| `--sni-port` | `SNI_PORT` | `443` | SNI TCP listener port |
| `--keyless-dir` | `KEYLESS_DIR` | `/etc/portal/keyless` | TLS cert and keyless materials directory |
| `--cloudflare-token` | `CLOUDFLARE_TOKEN` | `""` | Cloudflare DNS API token (Zone:Read + DNS:Edit) |
| `--lease-bps` | — | `0` (unlimited) | Per-lease bandwidth cap (bytes/sec) |
| `--trust-proxy-headers` | `TRUST_PROXY_HEADERS` | `false` | Trust X-Forwarded-For / X-Real-IP headers |
| `--trusted-proxy-cidrs` | `TRUSTED_PROXY_CIDRS` | `""` | CIDR allowlist for trusted proxies |

### Deployment Notes

- `admin_settings.json` persists runtime state (ban lists, BPS limits, approval mode) in the process working directory. Mount CWD as a volume to preserve state across container restarts.

## Architecture

See [docs/architecture.md](docs/architecture.md).
For architecture decisions, see [docs/adr/README.md](docs/adr/README.md).

## Contributing

1. Fork the repository.
2. Create a feature branch and make your changes.
3. Open a Pull Request.

## License

MIT License — see [LICENSE](LICENSE)
