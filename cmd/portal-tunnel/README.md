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

### Transport Model

Portal tunnel always runs in TLS reverse-connect mode:

- Reverse admission requires HTTPS relay endpoints.
- Tunnel-side TLS uses keyless signing with auto-discovered signer materials.
- Traffic is proxied from tunnel to local `--host` over TCP.
- Public access is `https://<service>.<portal-root-host>/`.

### Lifecycle Identity

The tunnel automatically acquires a per-lease mTLS identity via the relay's control plane. Identity materials are managed by `keyless_tls/keyless/lifecycle` and stored encrypted on disk under `KEYLESS_DIR/lifecycle-identities/`.

- `KEYLESS_DIR` defaults to `/etc/portal/keyless`. The tunnel must have read/write access to this directory.
- If the relay's issuer certificate or key is unavailable, the tunnel fails at startup.

## Flags

```text
Usage:
        portal-tunnel [OPTIONS] [ARGUMENTS]

Options:
        --relay           Portal relay server API URLs (comma-separated, https only) [default: https://localhost:4017] [env: RELAYS]
        --host            Target host to proxy to (host:port or URL)  [env: APP_HOST]
        --name            Service name  [env: APP_NAME]
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

### Production

```bash
export RELAYS=https://portal.example.com
export APP_HOST=localhost:3000
export APP_NAME=myapp

./bin/portal-tunnel
```

When the local service is unreachable, the tunnel returns an HTTP 503 "Service Unavailable" page to the browser.

### Multiple Relays (High Availability)

```bash
./bin/portal-tunnel \
  --host localhost:3000 \
  --name myapp \
  --relay https://portal1.example.com,https://portal2.example.com
```
