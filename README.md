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
- [WASM Client Tests](#wasm-client-tests)
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

```

For a public deployment guide (DNS, TLS, reverse proxy), see [docs/portal-deploy-guide.md](docs/portal-deploy-guide.md).

### Running a Portal App using Tunnel

```bash
# 1. Start your local service

# 2. Run the tunnel client to expose
curl -fsSL http://localhost:4017/tunnel | HOST=localhost:3000 NAME=myapp sh
```

### Running a Portal App using the SDK
See [portal-toys](https://github.com/gosuda/portal-toys)

## Architecture

For a detailed overview of system components and data flow, see the [architecture documentation](docs/architecture.md).

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

## WASM Client Tests

Run the JavaScript asset tests:

```bash
node --test cmd/webclient/*.test.js
```

Run the Go WASM client tests with the Node test runner:

```bash
GOCACHE=/tmp/portal-go-cache GOOS=js GOARCH=wasm \
go test -exec "node $(go env GOROOT)/lib/wasm/wasm_exec_node.js" \
./cmd/webclient
```

Run the TinyGo WASM tests:

```bash
GOCACHE=/tmp/portal-go-cache GOMODCACHE=/tmp/portal-gomodcache XDG_CACHE_HOME=/tmp/portal-cache \
tinygo test -target=wasm -tags debug ./cmd/webclient
```

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
