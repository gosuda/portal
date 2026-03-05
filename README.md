# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.png" alt="Portal logo" width="540" />
</p>

<p align="center">Expose your local application to the public internet — no port forwarding, no NAT, no DNS setup.<br />Portal is a self-hosted relay network. You can connect to any relay or run your own.</p>

<p align="center">将你的本地应用暴露到公网 —— 无需端口映射、无需 NAT、无需 DNS 配置。<br />Portal 是一个可自托管的中继网络，你可以运行自己的中继节点，也可以连接到已有的节点。</p>

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
- **End-to-end encryption**: Supports TLS passthrough with relay keyless certificates
- **Permissionless Hosting**: Anyone can run their own Portal — no approval needed
- **One-Command Setup**: Expose any local app with a single command

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

1. Fork the repository
2. Create a feature branch (git checkout -b feature/amazing-feature)
3. Commit your changes (git commit -m 'Add amazing feature')
4. Push to the branch (git push origin feature/amazing-feature)
5. Open a Pull Request

## License

MIT License — see [LICENSE](LICENSE)
