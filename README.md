# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.jpg" alt="Portal logo" width="540" />
</p>

Expose your local application to the public internet —
no ports, no NAT, no DNS setup.

Portal is an open, self-hosted, permissionless relay network — not a SaaS tunnel.

Connect to any relay to publish your service — or run your own relay.

## Why Portal?

Publishing a local service typically requires:

- Opening inbound ports
- Configuring NAT or firewall rules
- Managing DNS records
- Terminating TLS at a gateway

Portal removes these steps by inverting the connection model.
Applications establish outbound connections to a relay.
The relay runs on a public base domain, assigns each service a subdomain,
and routes incoming traffic while preserving end-to-end TLS.

## Features

- 🔄 **Connection Behind NAT**: Works behind NAT or firewalls without opening inbound ports
- 🌐 **Automatic Subdomain Routing**: Give each app its own subdomain ( your-app.<base-domain> )
- 🔐 **End-to-End Encryption**: TLS passthrough with relay keyless certificates
- 🕊️ **Permissionless Hosting**: Anyone can run their own Portal — no approval needed
- ⚙️ **One-Command Setup**: Expose any local app with a single command

## Components

- **Relay**: A server that routes public requests to the right connected app.
- **Tunnel**: A CLI agent that proxies your local app through the relay.

For details, see [docs/glossary.md](docs/glossary.md).

## Quick Start

### Run Portal Relay

```bash
git clone https://github.com/gosuda/portal
cd portal
docker compose up
```

For deployment to a public domain, see [docs/deployment.md](docs/deployment.md).

### Expose Local Service via Tunnel

1. Open the relay page in your browser.
2. Click `Add your server`.
3. Use the generated command to connect your local service.

With Tunnel, you can expose almost any local app in one step, without changing your app code.
Run your app locally, execute the generated command, and Portal publishes it on a public subdomain.

### Use Go SDK (Advanced)

See [portal-toys](https://github.com/gosuda/portal-toys) for more examples.

## Architecture

See [docs/architecture.md](docs/architecture.md).

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
