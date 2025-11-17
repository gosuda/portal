# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.jpg" alt="Portal logo" width="540" />
</p>

Portal is a permissionless, open hosting network that transforms your local project into a public web endpoint. [See more.](https://gosuda.org/portal/)

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Quick Start](#quick-start)
- [Architecture](#architecture)
- [Contributing](#contributing)
- [License](#license)

## Overview

Portal connects local applications to web users through a secure relay layer.
Each application is assigned a subdomain within Portal, and all traffic between endpoints is end-to-end encrypted.
This enables developers to publish local services globally without managing servers or cloud infrastructure.

## Features

- 🔄 **Connection Relay**: Connects clients behind NAT or firewalls through the Portal network.
- 🔐 **End-to-End Encryption**: Fully encrypted client-to-client communication, including browser sessions via a WASM-based Service Worker proxy.
- 🕊️ **Permissionless Hosting**: Anyone can open or choose their own Portal — no approval, no central authority.
- 🚀 **High Performance**: Multiplexed connections using yamux
- ⚙️ **Simple Setup**: Build and bootstrap apps quickly using the Portal SDK or Tunnel client.

## Quick Start
You can run **Portal** to host relay services, or run **App** to publish your own application through portal.

### Running the Portal Network
Run Portal with Docker Compose:

```bash
# 1. Start services
docker compose up

# 2. Open in browser
http://localhost:4017

# 3. Domain setup (optional)
# Point DNS to this server:
#   A record for portal.example.com → server IP
#   A (wildcard) for *.example.com (or *.portal.example.com) → server IP
#
# Then edit docker-compose.yml environment for your domain:
PORTAL_UI_URL: https://yourservice.com
PORTAL_FRONTEND_URL: https://*.yourservice.com
BOOTSTRAP_URIS: wss://yourservice.com/relay

```

### Running a Portal App
See [portal-toys](https://github.com/gosuda/portal-toys)

## Architecture

For a detailed overview of system components and data flow, see the [architecture documentation](docs/architecture.md).

## Portal-tunnel

portal-tunnel is a tunneling tool that connects a locally running service to a relay server, allowing external access.

1. **Run with a config file** (Check the [example](cmd/portal-tunnel/config.yaml.example) for configuration details)

```bash
bin/portal-tunnel expose --config config.yaml
```

2. **Expose a single service directly**

```bash
bin/portal-tunnel expose --relay <url> [--relay <url> ...] --host localhost --port 8080 --name <service>
```

## Glossary

If you need Portal-specific terminology, check the [Portal glossary](docs/glossary.md)
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

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
