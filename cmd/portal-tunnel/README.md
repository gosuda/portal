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

### TLS Mode (End-to-End Encryption)

Enable TLS for end-to-end encryption from client to your local service:

```bash
./bin/portal-tunnel --host localhost:8080 \
  --relay https://portal.example.com \
  --name myapp \
  --tls-mode keyless
```

TLS modes:
- No TLS mode (`--tls-mode no-tls`): plain TCP/HTTP proxying without TLS termination
- Self mode (`--tls-mode self`): tunnel uses local certificate and key files
- Keyless mode (`--tls-mode keyless`): tunnel auto-discovers certificate chain and delegates signing to external signer API
- TLS is terminated at the tunnel, then proxied to your local service via TCP
- Access via `https://myapp.example.com` directly on port 443

**Requirements:**
- Self mode: set `TLS_CERT_FILE` and `TLS_KEY_FILE` (or use `--tls-cert-file`, `--tls-key-file`)
- Keyless mode: no local cert/key required in default mode (SDK auto-discovers signer certificate chain)
- Keyless auto-discovery expects an HTTPS signer endpoint.

## Flags

```text
Usage:
        portal-tunnel [OPTIONS] [ARGUMENTS]

Options:
        --relay           Portal relay server API URLs (comma-separated, http/https) [default: http://localhost:4017] [env: RELAYS]
        --host            Target host to proxy to (host:port or URL)  [env: APP_HOST]
        --name            Service name  [env: APP_NAME]
        --tls-mode        TLS mode: no-tls, self, or keyless [default: no-tls] [env: TLS_MODE]
        --tls-cert-file   PEM certificate chain for --tls-mode self [env: TLS_CERT_FILE]
        --tls-key-file    PEM private key for --tls-mode self [env: TLS_KEY_FILE]
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
export TLS_MODE=keyless

./bin/portal-tunnel
```

### Production (Self TLS)

```bash
export RELAYS=https://portal.example.com
export APP_HOST=localhost:3000
export APP_NAME=myapp
export TLS_MODE=self
export TLS_CERT_FILE=/etc/ssl/myapp/fullchain.pem
export TLS_KEY_FILE=/etc/ssl/myapp/privkey.pem

./bin/portal-tunnel
```

### Production (Keyless TLS)

```bash
export RELAYS=https://portal.example.com
export APP_HOST=localhost:3000
export APP_NAME=myapp
export TLS_MODE=keyless

./bin/portal-tunnel
```

Expected signer API contract (`/v1/sign`):

```json
{
  "key_id": "relay-cert",
  "algorithm": "RSA_PSS_SHA256",
  "digest": "<base64>",
  "timestamp_unix": 1735628400,
  "nonce": "c4d76ad40f5d8f95a1fe4b2f1c922f4a"
}
```

```json
{
  "key_id": "relay-cert",
  "algorithm": "RSA_PSS_SHA256",
  "signature": "<base64>"
}
```

### Multiple Relays (High Availability)

```bash
./bin/portal-tunnel \
  --host localhost:3000 \
  --name myapp \
  --relay https://portal1.example.com,https://portal2.example.com \
  --tls-mode keyless
```
