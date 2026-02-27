# Portal-tunnel

Portal-tunnel is a tunneling tool that connects your local services to a [portal network](github.com/gosuda/portal), without any additional configuration.

## Usage

You can run the tunnel using command-line flags or a configuration file.

### Run binary

```bash
./bin/portal-tunnel --host localhost:8080 \
  --relay https://portal.gosuda.org,https://portal.thumbgo.kr,https://portal.iwanhae.kr \
  --name <service> \
  --description "Service description" \
  --tags tag1,tag2 \
  --thumbnail https://example.com/thumb.png
```

### TLS Mode (Keyless End-to-End Encryption)

Enable TLS for end-to-end encryption from client to your local service:

```bash
./bin/portal-tunnel --host localhost:8080 \
  --relay https://portal.example.com \
  --name myapp \
  --tls
```

When `--tls` is enabled:
- Tunnel stores only the public certificate chain
- Relay signer stores the private key and performs remote CertificateVerify signatures
- TLS is terminated at the tunnel, then proxied to your local service via TCP
- Access via `https://myapp.example.com` directly on port 443

**Requirements:**
- Relay must expose keyless signer configuration via `/sdk/keyless/config`
- Relay signer TLS/signing key materials must be configured

## Flags

```text
Usage:
        portal-tunnel [OPTIONS] [ARGUMENTS]

Options:
        --relay           Portal relay server API URLs (comma-separated, http/https) [default: http://localhost:4017] [env: RELAYS]
        --host            Target host to proxy to (host:port or URL)  [env: APP_HOST]
        --name            Service name  [env: APP_NAME]
		--tls             Enable keyless TLS termination on tunnel client (private key stays on relay signer) [env: TLS_ENABLE]
        --protocols       ALPN protocols (comma-separated) [default: http/1.1,h2] [env: APP_PROTOCOLS]
        --description     Service description metadata  [env: APP_DESCRIPTION]
        --tags            Service tags metadata (comma-separated)  [env: APP_TAGS]
        --thumbnail       Service thumbnail URL metadata  [env: APP_THUMBNAIL]
        --owner           Service owner metadata  [env: APP_OWNER]
        --hide            Hide service from discovery (metadata)  [env: APP_HIDE]
        -h, --help        Print this help message and exit
```

## Examples

### Quick Start (HTTP)

```bash
# macOS/Linux
curl -fsSL https://portal.example.com/tunnel | HOST=localhost:3000 NAME=myapp sh

# Windows PowerShell
$env:HOST="localhost:3000"; $env:NAME="myapp"; irm https://portal.example.com/tunnel | iex
```

### Production (TLS)

```bash
export RELAYS=https://portal.example.com
export APP_HOST=localhost:3000
export APP_NAME=myapp
export TLS_ENABLE=true

./bin/portal-tunnel
```

### Multiple Relays (High Availability)

```bash
./bin/portal-tunnel \
  --host localhost:3000 \
  --name myapp \
  --relay https://portal1.example.com,https://portal2.example.com \
  --tls
```
