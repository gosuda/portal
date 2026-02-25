# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.jpg" alt="Portal logo" width="540" />
</p>

Portal is a permissionless, open hosting network that transforms your local project into a public web endpoint. [Learn more.](https://gosuda.org/portal/)

## Overview

Portal connects local applications to web users through a secure relay layer.
Each application is assigned a subdomain within Portal, and all traffic between endpoints is end-to-end encrypted.
This enables developers to publish local services globally without managing servers or cloud infrastructure.

## Features

- 🔄 **Connection Relay**: Connects clients behind NAT or firewalls through the Portal network
- 🔐 **End-to-End Encryption**: TLS passthrough with ACME DNS-01 certificates
- 🕊️ **Permissionless Hosting**: Anyone can run their own Portal — no approval needed
- ⚙️ **Simple Setup**: Quick start with Tunnel client or Go SDK

## Quick Start

### Run Portal Relay

```bash
# Start with Docker Compose
docker compose up

# Access at http://localhost:4017
# Admin panel at http://localhost:4017/admin
# Auto-generated admin key shown in logs, or set your own:
ADMIN_SECRET_KEY=your-secret-key docker compose up
```

For production deployment (DNS, TLS, reverse proxy), see [docs/portal-deploy-guide.md](docs/portal-deploy-guide.md).

### Expose Local Service via Tunnel

```bash
# Windows PowerShell
$env:HOST="localhost:3000"; $env:NAME="myapp"; irm http://localhost:4017/tunnel | iex

# macOS/Linux
curl -fsSL http://localhost:4017/tunnel | HOST=localhost:3000 NAME=myapp sh
```

### Use Go SDK

```go
import "gosuda.org/portal/sdk"

client, _ := sdk.NewClient(sdk.WithBootstrapServers([]string{"http://localhost:4017"}))
listener, _ := client.Listen("myapp")
http.Serve(listener, handler)
```

See [portal-toys](https://github.com/gosuda/portal-toys) for more examples.

## Architecture

- **Relay Server**: TLS passthrough relay with SNI routing, admin UI, lease management
- **SDK**: Go library for native app integration
- **Tunnel**: CLI client for exposing local services without code changes

For details, see [docs/architecture.md](docs/architecture.md).

## Contributing

We welcome contributions from the community!
Before getting started, please check the [development guide](docs/development.md)
 for setup instructions and best practices.

### Steps to Contribute
1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request
## License

MIT License — see [LICENSE](LICENSE)
