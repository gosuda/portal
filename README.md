# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.jpg" alt="Portal logo" width="540" />
</p>

Portal is an open hosting network that transforms your local project into a public web endpoint. [See more](https://gosuda.org/portal/)

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

- 🔐 **End-to-End Encryption**: Client-to-client communication is fully encrypted
- 🔑 **Cryptographic Identity**: Ed25519-based identity system with verifiable signatures
- 🔄 **Connection Relay**: Secure connection forwarding through central server
- ⏰ **Lease Management**: Time-based lease system with automatic cleanup
- 🌐 **Protocol Support**: Application-Layer Protocol Negotiation (ALPN)
- 🚀 **High Performance**: Multiplexed connections using yamux
- 🐳 **Docker Support**: Containerized deployment ready
- 🌍 **Browser E2EE Proxy**: WASM-based Service Worker for automatic browser encryption
- 📱 **Multi-Platform**: Go SDK for servers, WASM SDK for browsers

## Quick Start

### Portal Hosting
You can publish Portal for relay apps.

### App Publishing
You can Pubishing own app through portal.
See [portal-toys](https://github.com/gosuda/portal-toys)

## Architecture

For a detailed overview of system components and data flow, see the [architecture documentation](docs/architecture.md).

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
