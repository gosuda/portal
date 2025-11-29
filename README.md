# PORTAL — Public Open Relay To Access Localhost

What if you could take an app that only runs on your laptop and **safely expose it to the Internet** without touching firewalls, domains, or TLS certificates? **Portal** is an E2EE-based public relay/tunneling solution built exactly for that.

For **app developers**, Portal turns any local service into a shareable, internet‑reachable multiple endpoint in seconds with **end‑to‑end encrypted** protection for your app server's and users data. For **relay operators**, Portal offers an open‑source relay server with a built‑in web UI, sensible defaults, and tunable policies so you can easily run your own secure public or private portal.

## Quick Start — Three Ways In

### Running Your Own Relay Server

From the repository root, you can build and start it like this:

```bash
make build
./bin/relay-server  # 4017 port by default
```

With just those two commands, you become the operator of an **E2EE-based privacy-preserving relay service and public portal server**.

### Exposing Your Local App

Now flip the perspective. Suppose you are an **app developer**, not a portal operator. You can expose your local app to the Internet without touching firewalls, NAT port forwarding, or TLS.

`portal-tunnel` is available in its own repository: [github.com/gosuda/portal-tunnel](https://github.com/gosuda/portal-tunnel). Clone it and build it similarly:

```bash
git clone https://github.com/gosuda/portal-tunnel.git
cd portal-tunnel
make build
./bin/portal-tunnel expose --port 8080  # if your app is running on 8080
```

This connects to the **local relay server** you started in step 1-1 and exposes your app running on port `8080`.

You can attach to any Portal instance you know. Don't you have any portal instances? Choose the one you like from the [Portal List](https://portal-list.portal.gosuda.org/):

```bash
./bin/portal-tunnel expose \
	--relay "wss://portal.gosuda.org/relay" \
	--port 8080 \
	--name "my-app" \
	--description "My cool local app" \
```

Once running, your local app becomes **publicly reachable from anywhere in the world** with **end-to-end encryption**.

### Checking Out What's Running

The Relay Server is more than just a raw tunneling gateway. It ships with a **web frontend** out of the box:

- A **card-style list of apps** currently being relayed.
- App developers can **optionally register their apps** in this list.

Visit any Portal instance (like the public one at [portal.gosuda.org](https://portal.gosuda.org)) to see what apps are currently being relayed. Click a card to access the app with **end-to-end encrypted traffic** automatically handled in the background.


## Why Portal?

There are already many tunneling, proxy, and ingress services available. Portal stands out for several key reasons:

### 3-1. Open-source tunnel and relay both

Most commercial tunneling services only ship a **client binary**, while the server infrastructure that actually relays traffic is completely opaque.

Portal ships **both the tunnel client and the Relay Server as open source**:

- Anyone can **operate their own public portal**.
- You can run private portals for internal networks, personal use, or community-driven portals.

### True End-to-End Encryption

In many tunneling services, the server can see **all request and response traffic in plaintext**:

- App users, and
- App providers (service operators)

effectively share their data with a third-party tunnel vendor.

Portal was designed from day one to avoid this. In the Portal network:

- **App ↔ Client traffic is encrypted via X25519 + ChaCha20-Poly1305 E2EE**.
- The Relay Server only sees minimal header-level information and **never decrypts the payload**.
- The security model is almost equivalent to serving directly, without any tunnel at all.

You also don’t need to worry about issuing/renewing TLS certificates or maintaining complex load balancers. **Publishing your app via Portal automatically gives you an encrypted channel**.

If your Relay Server itself is behind TLS, your effective stack becomes **E2EE over TLS**—a double protection layer with no extra setup, installs, or service bills.

### Easy Relay Operation with Built-in UI, Admin, and Utilities

The Portal Relay Server does much more than simply "open a port and forward bytes." As soon as it comes up, you get:

- A **default homepage** on `80` or `443` that lists apps currently being relayed.
- A React/Tailwind-based frontend you can fully customize (it’s open source).
- Operator-friendly utilities such as bandwidth control, DDoS protection, and admin pages (some WIP).

Portal is therefore a **unified solution that bundles relay, homepage, admin, and utilities**. Unlike minimal proxy-only projects, Portal is designed so that even beginners can run a relay safely and comfortably.


## Overview

Portal consists of three main components:

- **Relay Server (`relay-server`)**: the public portal. It manages Leases (app advertisement slots) and relays traffic between clients and apps while preserving E2EE end to end. By default it listens on `:4017` for web UI serving, app server registration (via SDK/WebSocket), and client tunneling traffic; it can be put behind your own TLS / reverse proxy if you like.
- **Tunnel / SDK ([`portal-tunnel`](https://github.com/gosuda/portal-tunnel) , `sdk`)**: client-side components that let app developers register their services with Portal and tunnel local services into the portal. You can either run the standalone `portal-tunnel` CLI or embed the Go SDK directly into your app or automation scripts.
- **Portal Web (served by Relay Server)**: a React/Tailwind-based single-page app that shows a card-style list of currently relayed services, with search, tag-based filtering, pagination, and server-side rendering (SSR) powered by the relay's Lease Manager.

Portal supports **two installation methods** to fit different deployment scenarios:

- **`make` build**: For local development, testing, and custom builds. Build from source with full control over the compilation process.
- **Docker Compose**: For quick deployment and production use. Pre-built container images with minimal setup required.


For a deeper look into system components and data flow, see [docs/architecture.md](docs/architecture.md).


## Installation & Running

You can run Portal using a **Makefile** or **Docker Compose**.*.

### Using `make` (Local Development/Testing)

From the repository root:

```bash
make build          # Build WASM, frontend, and server
./bin/relay-server  # Run the relay server (default port: 4017)
```

> NOTE: You only need to run `make build` once to build the binaries, including `portal-tunnel`.

To just spin up a local server quickly:

```bash
make run  # Internally runs ./bin/relay-server
```

### Using Docker Compose

#### Prerequisites

Ensure you have Docker and Docker Compose installed:

- **Docker**: [Install Docker](https://docs.docker.com/get-docker/)
- **Docker Compose**: Included with Docker Desktop, or install separately via [Docker Compose installation guide](https://docs.docker.com/compose/install/)

Verify installation:

```bash
docker --version
docker-compose --version
```

#### Quick Start

If you have Docker installed, you can use `docker-compose.yml` to get started quickly:

```bash
docker-compose build
docker-compose up -d
```

This will:

1. Build the Portal Docker image (or pull from `ghcr.io/gosuda/portal:1`)
2. Start the relay server in detached mode on port `4017`
3. Use default configuration suitable for local testing


#### Environment Variable Configuration


**Available Environment Variables:**

- `PORTAL_PORT`: Port the server listens on (default: `4017`)
- `PORTAL_URL`: Base URL for the portal frontend (default: `http://localhost:4017`)
- `PORTAL_APP_URL`: Wildcard URL pattern for app access with `%s` placeholder (default: `http://*.localhost:4017`)
- `BOOTSTRAP_URIS`: Comma-separated relay server addresses for tunnel clients (default: `ws://localhost:4017/relay`)

The Docker Compose setup supports extensive customization via environment variables. You can configure them in two ways:

**Option 1: Create a `.env` file** (recommended)

Create a `.env` file in the same directory as `docker-compose.yml`:

```env
# Server port
PORTAL_PORT=4017

# Portal base URL (adjust for your domain)
PORTAL_URL=https://portal.example.com

# App subdomain pattern (use %s as placeholder for app names)
PORTAL_APP_URL=https://%s.portal.example.com

# Bootstrap URIs for tunnel clients (comma-separated)
BOOTSTRAP_URIS=wss://portal.example.com/relay,wss://backup.example.com/relay
```

Then run:

```bash
docker-compose up -d
```

**Option 2: Inline environment variables**

Set variables directly in the command line:

```bash
PORTAL_PORT=8080 \
PORTAL_URL=https://myportal.io \
PORTAL_APP_URL=https://%s.myportal.io \
BOOTSTRAP_URIS=wss://myportal.io/relay \
docker-compose up -d
```

**Example: Production Configuration with TLS**

Assuming you have a reverse proxy (nginx, Caddy, Traefik) handling TLS:

`.env` file:

```env
PORTAL_PORT=4017
PORTAL_URL=https://portal.mydomain.com
PORTAL_APP_URL=https://%s.portal.mydomain.com
BOOTSTRAP_URIS=wss://portal.mydomain.com/relay
```

`docker-compose.yml` (modify ports as needed):

```yaml
services:
  portal:
    image: ghcr.io/gosuda/portal:1
    ports:
      - "127.0.0.1:4017:4017"  # Only expose to localhost, reverse proxy forwards
    environment:
      PORTAL_URL: ${PORTAL_URL}
      PORTAL_APP_URL: ${PORTAL_APP_URL}
      BOOTSTRAP_URIS: ${BOOTSTRAP_URIS}
    restart: unless-stopped
```

**Example: Custom Port Configuration**

To run Portal on a different port (e.g., `8080`):

```bash
PORTAL_PORT=8080 \
PORTAL_URL=http://localhost:8080 \
PORTAL_APP_URL=http://*.localhost:8080 \
BOOTSTRAP_URIS=ws://localhost:8080/relay \
docker-compose up -d
```

#### Persisting Configuration

For production deployments, consider:

1. **Use a `.env` file** for version-controlled configuration (add sensitive values via secrets)
2. **Adjust `docker-compose.yml`** for your infrastructure (ports, networks, volumes)
3. **Set up a reverse proxy** (nginx, Caddy, Traefik) for TLS termination and domain routing
4. **Configure DNS** to point your domain and wildcard subdomain to your server:
   - A record: `portal.example.com` → your server IP
   - A record (wildcard): `*.portal.example.com` → your server IP
   
   Then update the environment variables in `docker-compose.yml` or `.env`:
   ```yaml
   PORTAL_URL: https://portal.example.com
   PORTAL_APP_URL: https://%s.portal.example.com
   BOOTSTRAP_URIS: wss://portal.example.com/relay
   ```


## Configuration

Both the Relay Server and Tunnel Client expose a variety of flags so you can customize ports, bandwidth, policies, and more. Here are a few common scenarios.

> For a complete list of flags and defaults, run each binary with `--help` or check the Go sources under `cmd/relay-server` and `cmd/portal-tunnel`.

### Relay Server (`relay-server`) Behavior and Flags

**Default Behavior:**

By default, `relay-server` listens on `:4017` for three types of traffic:

1. **Web UI**: HTTP requests for the portal homepage and app listing
2. **App Server Registration & Tunneling**: WebSocket connections from app servers (via SDK or portal-tunnel) to register leases and receive client traffic
3. **Client Tunneling**: WebSocket connections from browsers/clients to tunnel E2EE traffic to apps

Additionally:

- Uses `PORTAL_URL` and `PORTAL_APP_URL` if set; otherwise falls back to `http://localhost:4017` with a wildcard app URL
- Uses `BOOTSTRAP_URIS` if provided; otherwise auto-derives sensible bootstrap URIs from `PORTAL_URL`
- Routes requests based on Host header: subdomain requests go to the E2EE proxy, others to the portal app UI

**Configuration Flags:**

- `--port <PORT>`: Server listening port. Default: `4017`
  ```bash
  ./bin/relay-server --port 5000
  ```

- `--portal-url <URL>`: Base URL for the portal frontend. Used for generating app links and configuration. (env: `PORTAL_URL`)
  ```bash
  ./bin/relay-server --portal-url "https://portal.example.com"
  ```

- `--portal-app-url <PATTERN>`: Subdomain wildcard URL pattern for app access. (env: `PORTAL_APP_URL`)
  ```bash
  ./bin/relay-server --portal-app-url "https://%s.portal.example.com"
  ```

- `--bootstraps <URL1,URL2,...>`: Comma-separated list of bootstrap server addresses for clients to connect to. (env: `BOOTSTRAP_URIS`)
  ```bash
  ./bin/relay-server --bootstraps "portal.example.com:4017,portal-backup.example.com:4017"
  ```

- `--alpn <PROTOCOL>`: ALPN identifier advertising the service protocol. Default: `http/1.1`
  ```bash
  ./bin/relay-server --alpn "h2"
  ```

- `--max-lease <N>`: Maximum active relayed connections per lease. `0` means unlimited. Default: `0`
  ```bash
  ./bin/relay-server --max-lease 100
  ```

- `--lease-bps <BPS>`: Default bandwidth rate limit (bytes-per-second) per lease. `0` means unlimited. Default: `0`
  ```bash
  ./bin/relay-server --lease-bps 1000000    # 1 MB/s per lease
  ```

- `--noindex <true|false>`: Disallow all web crawlers via `robots.txt`. Default: `false` (env: `NOINDEX`)
  ```bash
  ./bin/relay-server --noindex true
  ```

**Complete Example with Multiple Options:**

```bash
./bin/relay-server \
	--port 8080 \
	--portal-url "https://myportal.io" \
	--portal-app-url "https://%s.myportal.io" \
	--bootstraps "myportal.io:8080,backup.myportal.io:8080" \
	--max-lease 50 \
	--lease-bps 5000000 \
	--noindex false
```

**Using Environment Variables:**

```bash
export PORTAL_URL="https://portal.example.com"
export PORTAL_APP_URL="https://%s.portal.example.com"
export BOOTSTRAP_URIS="portal.example.com:4017,backup.example.com:4017"
export NOINDEX="true"

./bin/relay-server --port 4017
```

Run `./bin/relay-server --help` for the complete list of available flags.

### Portal Tunnel (`portal-tunnel`) Usage and Flags

> **Note:** `portal-tunnel` is a separate repository. Clone it from [github.com/gosuda/portal-tunnel](https://github.com/gosuda/portal-tunnel) and build with `make build`.

**Command Structure:**

```bash
./bin/portal-tunnel expose [OPTIONS]
```

The `expose` command registers your local service with a Portal relay server and tunnels incoming client traffic to your local app.

**Basic Usage:**

```bash
./bin/portal-tunnel expose \
	--relay "ws://localhost:4017/relay" \
	--host "127.0.0.1" \
	--port "8080" \
	--name "my-app" \
	--description "my-cool-local-app" \
	--tags "demo,example" \
	--owner "your-name"
```

**Commonly Used Flags:**

- `--config <file>`: Load service configuration from a YAML/TOML file instead of command-line flags. When provided, other flags except `--relay` are ignored.
- `--relay <URL[,URL...]>`: Portal relay server address(es) (comma-separated). Default: `ws://localhost:4017/relay`. Use WebSocket URLs (`ws://` or `wss://`).
- `--host <HOST>`: Local host to proxy traffic to. Default: `localhost`
- `--port <PORT>`: Local port to proxy traffic to. Default: `4018`
- `--name <NAME>`: Human-readable service name for discovery. Auto-generated if empty.
- `--description <TEXT>`: Service description displayed in the portal UI.
- `--tags <tag1,tag2,...>`: Comma-separated tags for filtering (e.g., `demo,example,frontend`).
- `--thumbnail <URL>`: URL to a thumbnail/icon for your service in the portal UI.
- `--owner <NAME>`: Service owner name or contact information.
- `--hide`: If set to `true`, hide this service from the portal's public discovery list (metadata only; traffic still relayed).

**Complete Example:**

```bash
./bin/portal-tunnel expose \
	--relay "wss://portal.gosuda.org/relay" \
	--host "127.0.0.1" \
	--port "3000" \
	--name "demo-app" \
	--description "A demo React application" \
	--tags "web,frontend,demo" \
	--owner "alice" \
	--thumbnail "https://example.com/thumbnail.png"
```

**Using a Configuration File:**

```bash
./bin/portal-tunnel expose --config ./services.yaml
```

Create `services.yaml`:

```yaml
relays:
  - name: "primary"
    urls:
      - "wss://portal.gosuda.org/relay"
      - "ws://localhost:4017/relay"  # Fallback
services:
  - name: "web-app"
    target: "127.0.0.1:3000"
    description: "My web app"
    tags: [web, frontend]
    owner: "alice"
  - name: "api-server"
    target: "127.0.0.1:8080"
    description: "REST API backend"
    tags: [api, backend]
    owner: "alice"
```

**Running Multiple Services:**

Use a configuration file to expose multiple local services to the same or different relays:

```bash
./bin/portal-tunnel expose --config ./all-services.yaml
```

This will spawn tunnels for all configured services, each maintaining its own connection to its configured relay(s).

**Graceful Shutdown:**

Press `Ctrl+C` to shut down the tunnel(s) cleanly. The tunnel logs the number of connections proxied before exiting.


## Features

Highlights of what Portal provides:

- **Expose local apps to the world**: publish a local service to the Internet without firewall tweaks, port-forwarding, domains, nameservers, or TLS setup.
- **End-to-End Encryption**: X25519 key exchange plus ChaCha20-Poly1305 keeps all traffic encrypted from client to app. Browser sessions are protected by a WASM-based Service Worker proxy.
- **High Performance**: `yamux`-based multiplexing lets you run many streams over a single connection for high throughput and low overhead.
- **Simple Setup**: with the Portal SDK or Tunnel client, you can bootstrap apps quickly without deep infra knowledge.
- **Integrated operator experience**: home UI, admin pages, bandwidth and security utilities are bundled for relay operators.
- **App directory & discoverability**: app developers can register their services in the portal list to get organic exposure and an easy, UI-driven health check.
- **Fun factor — Portal animation**: on a user's very first visit via Portal, while the E2EE WASM bundle is downloading, they'll see a **cute gopher animation**:

	![Portal Animation](portal.jpg)

	- The animation appears **only once across all apps** relayed by a given Portal: once the WASM is cached, it's reused.


## Architecture

For a detailed overview of Portal's architecture, components, and connection/crypto flows, see:

- System components and data flow: [docs/architecture.md](docs/architecture.md)

That document includes Mermaid diagrams for the system layout, component relationships, and connection sequences to help you understand how Portal works internally.


## Glossary

Portal uses terminology that differs slightly from traditional server–client architectures (Portal, App, Client, Lease, etc.). If you're unsure what a term means, see:

- Portal glossary: [docs/glossary.md](docs/glossary.md)

It defines key concepts such as Portal (Relay Server), App (service publisher), Client (service consumer), and Lease (advertising/routing slot).


## Contributing

We warmly welcome community contributions to Portal.

Before opening a PR, please read:

- Development guide and project principles: [docs/development.md](docs/development.md)

This document describes the project philosophy, agreement process for changes that affect usability, and testing/quality requirements so we can evolve Portal without regressing the user experience.


## Steps to Contribute

A typical contribution workflow looks like this:

1. Fork the repository.
2. Create a feature branch:  
	 `git checkout -b feature/amazing-feature`
3. Commit your changes:  
	 `git commit -m "Add amazing feature"`
4. Push to your fork:  
	 `git push origin feature/amazing-feature`
5. Open a Pull Request on GitHub and describe your changes and motivation.

If your change could affect existing usability, please **call out the rationale and impact** clearly in the PR description and be ready to discuss it with reviewers.


## License

This project is licensed under the MIT License. See the `LICENSE` file at the repository root for details.

