# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.jpg" alt="Portal logo" width="540" />
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

Security policy hardening in this refactor does not require operator setup changes.

## Components

- **Relay**: A server that routes public requests to the right connected app.
- **Tunnel**: A CLI agent that proxies your local app through the relay.

For details, see [docs/glossary.md](docs/glossary.md).

## Protocol Scope

- Raw TCP reverse-connect is the only supported relay/tunnel transport.
- No websocket compatibility path is provided for transport control or data-plane flow.

## Runtime Contracts

- Lease IDs in admin and SDK payloads are plain string IDs.
- Base64URL lease-ID encoding is used only for admin action route path segments (`/admin/leases/{encodedLeaseID}/{action}`).
- `/sdk/connect` accepts secure transport when either:
  - direct TLS is present, or
  - request comes from an allowlisted trusted proxy and forwarded HTTPS headers indicate HTTPS.
- Tunnel installer scripts always fetch `${BIN_URL}.sha256` and fail closed on missing, malformed, or mismatched checksum.

### Routing Notes

- SNI routing preserves an exact-match fallback for the portal root host. Requests that target the exact `PORTAL_URL` host (for example, `portal.example.com`) are handled by the admin/API listener via the no-route path.

## Quick Start

### Run Portal Relay

```bash
git clone https://github.com/gosuda/portal
cd portal
docker compose up
```

For deployment to a public domain, see [docs/deployment.md](docs/deployment.md).

### Expose Local Service via Tunnel

1. Run your local service.
2. Open the Portal relay site.
3. Click `Add your server` button.
4. Use the generated command to connect your local service.

### Use the Go SDK (Advanced)

See [portal-toys](https://github.com/gosuda/portal-toys) for more examples.

## Architecture

See [docs/architecture.md](docs/architecture.md).
For architecture decisions, see [docs/adr/README.md](docs/adr/README.md).

## Contributing

We welcome contributions from the community!

### Steps to Contribute
1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

MIT License — see [LICENSE](LICENSE)
