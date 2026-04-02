# PORTAL - Public Open Relay To Access Localhost

[English](./README.md) | [简体中文](./README.zh-CN.md)

<p align="center"><img width="800" alt="Portal Demo" src="./portal.gif" /></p>

<p align="center">Expose your local application to the public internet - no port forwarding, no NAT, no DNS setup.<br />Portal is a self-hosted relay network with end-to-end encryption (E2EE). You can connect to any relay or run your own.</p><br />

## Why Portal?

Publishing a local service to the internet is often complicated.
It usually requires opening inbound ports, configuring NAT or firewalls, managing DNS, and terminating TLS.

Portal removes this complexity by inverting the connection model.
Applications establish outbound connections to a relay, which exposes the service to the public internet and routes incoming traffic back to the application while preserving end-to-end TLS.

Unlike other tunneling services, Portal is self-hosted and permissionless. You can run your own relay on your domain or connect to any relay.

## Features

- **NAT-friendly connectivity**: Works behind NAT or firewalls without opening inbound ports
- **Automatic subdomain routing**: Gives each app its own subdomain (`your-app.<base-domain>`)
- **End-to-end tenant TLS**: Relay routes by SNI, while tenant TLS terminates on your side with relay-backed keyless signing
- **Permissionless hosting**: Anyone can run their own Portal, no approval needed
- **One-command setup**: Expose any local app with a single command
- **UDP relay (experimental)**: Supports raw UDP relay

## How Portal Provides End-to-End Encryption

Portal is designed so that tenant TLS terminates on your side rather than at the relay. In the normal data path, the relay forwards encrypted traffic without access to tenant TLS plaintext.

1. The relay accepts the public connection and reads only the TLS ClientHello required for SNI-based routing.
2. It forwards the tenant connection as raw encrypted bytes over the reverse session without terminating tenant TLS.
3. The Portal client on your side acts as the TLS server and completes the tenant handshake locally.
4. For relay-hosted domains, the Portal client obtains certificate signatures via `/v1/sign`, using the relay only as a keyless signing oracle.
5. Session keys are derived entirely on your side. The relay provides certificate signatures only and does not receive tenant traffic secrets.
6. After the handshake, the relay continues forwarding ciphertext without needing tenant TLS plaintext to keep routing traffic.

Portal also checks that the relay is preserving TLS passthrough. The Portal client connects to its own public endpoint and compares TLS exporter values observed on both client-controlled ends. If they differ, `portal expose` rejects the relay by default.

## Components

- **Relay**: A server that routes public requests to the right connected app.
- **Tunnel**: A CLI agent that proxies your local app through the relay.

## Quick Start

### Run Portal Relay

```bash
git clone https://github.com/gosuda/portal
cd portal && cp .env.example .env
docker compose up
```

The Docker setup persists both the relay identity JSON and relay certificates under `./.portal-certs`. Keep that directory on persistent storage if you want a stable relay address and certificate state across restarts.

For public domains, you can either:

- place `fullchain.pem` and `privatekey.pem` in `./.portal-certs` and leave `ACME_DNS_PROVIDER` empty, or
- set `ACME_DNS_PROVIDER=cloudflare|gcloud|route53` and let Portal manage DNS-01 + renewal

If you want Portal-managed ENS TXT/DNSSEC while keeping manual certificate files, place the certs in `./.portal-certs`, set `ACME_DNS_PROVIDER`, and enable `ENS_GASLESS_ENABLED=true`.
For deployment to a public domain, see [docs/deployment.md](docs/deployment.md).

### Expose Local Service via Tunnel

Install the tunnel from the official GitHub release assets:

```bash
curl -fsSL https://github.com/gosuda/portal/releases/latest/download/install.sh | bash
portal expose 3000
```

```powershell
$ProgressPreference = 'SilentlyContinue'
irm https://github.com/gosuda/portal/releases/latest/download/install.ps1 | iex
portal expose 3000
```
For CLI usage and install details, see [cmd/portal-tunnel/README.md](cmd/portal-tunnel/README.md).

### Use the Go SDK (Advanced)

See [portal-toys](https://github.com/gosuda/portal-toys) for more examples.

## Architecture

See [docs/architecture.md](docs/architecture.md).
For architecture decisions, see [docs/adr/README.md](docs/adr/README.md).

## Examples

| Example | Description |
|---------|-------------|
| [nginx reverse proxy](docs/examples/nginx-proxy/) | Deploy Portal behind nginx with L4 SNI routing and TLS termination |
| [nginx + multi-service](docs/examples/nginx-proxy-multi-service/) | Run Portal alongside other web services behind a single nginx instance |

## Public Relay Registry

Portal's official public relay registry is:

`https://raw.githubusercontent.com/gosuda/portal/main/registry.json`

Portal tunnel clients can include this registry by default, and the relay UI also reads from the same path to show the official relay list.

If you operate a public Portal relay, open a Pull Request to add your relay URL to `registry.json`. Keeping the registry updated makes public relays easier for the community to discover.

## Contributing

We welcome contributions from the community!

1. Fork the repository
2. Create a feature branch (git checkout -b feature/amazing-feature)
3. Commit your changes (git commit -m 'Add amazing feature')
4. Push to the branch (git push origin feature/amazing-feature)
5. Open a Pull Request

## License

MIT License - see [LICENSE](LICENSE)
