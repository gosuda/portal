# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.png" alt="Portal logo" width="540" />
</p>

<p align="center">Expose your local application to the public internet — no port forwarding, no NAT, no DNS setup.<br />Portal is a self-hosted relay network. You can connect to any relay or run your own.</p><br />

<p align="center"><img width="800" alt="Portal Demo" src="./portal.gif" /></p>

## Why Portal?

Publishing a local service to the internet is often complicated.
It usually requires opening inbound ports, configuring NAT or firewalls, managing DNS, and terminating TLS.

Portal removes this complexity by inverting the connection model.
Applications establish outbound connections to a relay, which exposes the service to the public internet and routes incoming traffic back to the application while preserving end-to-end TLS.

Unlike other tunneling services, Portal is self-hosted and permissionless. You can run your own relay on your domain or connect to any relay.

## Features

- **NAT-friendly connectivity**: Works behind NAT or firewalls without opening inbound ports
- **Automatic subdomain routing**: Gives each app its own subdomain (`your-app.<base-domain>`)
- **End-to-end encryption**: Supports TLS passthrough with relay keyless certificates
- **Permissionless Hosting**: Anyone can run their own Portal — no approval needed
- **One-Command Setup**: Expose any local app with a single command
- **UDP Relay (Experimental)**: Supports raw UDP relay use cases, but the transport model and operational behavior may still change

## Components

- **Relay**: A server that routes public requests to the right connected app.
- **Tunnel**: A CLI agent that proxies your local app through the relay.

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

MIT License — see [LICENSE](LICENSE)
