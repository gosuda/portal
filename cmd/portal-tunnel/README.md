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
  --tls
```

TLS options:
- TLS disabled (default): plain TCP/HTTP proxying without TLS termination
- TLS enabled (`--tls`): keyless TLS mode with auto-discovered certificate chain and remote signing
- TLS is terminated at the tunnel, then proxied to your local service via TCP
- Access via `https://myapp.example.com` directly on port 443

**Requirements:**
- TLS enabled: no local cert/key required in default mode (SDK auto-discovers signer certificate chain)
- Keyless auto-discovery expects an HTTPS signer endpoint.

## Flags

```text
Usage:
        portal-tunnel [OPTIONS] [ARGUMENTS]

Options:
        --relay           Portal relay server API URLs (comma-separated, http/https) [default: http://localhost:4017] [env: RELAYS]
        --host            Target host to proxy to (host:port or URL)  [env: APP_HOST]
        --name            Service name  [env: APP_NAME]
        --tls             Enable keyless TLS mode [env: TLS]
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
curl -fsSL https://portal.example.com/tunnel | APP_HOST=localhost:3000 APP_NAME=myapp sh

# Windows PowerShell
$env:APP_HOST="localhost:3000"; $env:APP_NAME="myapp"; irm https://portal.example.com/tunnel | iex
```

Installer integrity policy:

- The installer downloads `BIN_URL` and `BIN_URL.sha256`.
- SHA256 verification is mandatory and fail-closed.
- Missing, malformed, or mismatched checksums abort startup with a remediation hint.

### Production (TLS)

```bash
export RELAYS=https://portal.example.com
export APP_HOST=localhost:3000
export APP_NAME=myapp
export TLS=1

./bin/portal-tunnel
```

### Production (Keyless TLS)

```bash
export RELAYS=https://portal.example.com
export APP_HOST=localhost:3000
export APP_NAME=myapp
export TLS=1

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
  --tls
```
