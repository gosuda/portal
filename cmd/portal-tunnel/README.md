# Portal CLI

`cmd/portal-tunnel` builds the `portal` tunnel CLI. It connects a local service to one or more Portal relays.

## Usage

```bash
curl -sSL https://portal.example.com/install.sh | bash
portal expose 3000
portal list
```

```powershell
irm https://portal.example.com/install.ps1 | iex
portal expose 3000
portal list
```

Custom relay and metadata example:

```text
portal expose --name myapp \
  --relays https://portal.example.com \
  --description "Service description" \
  --tags tag1,tag2 \
  --thumbnail https://example.com/thumb.png \
  --owner "Portal Operator" \
  localhost:8080
```

## Commands

### `portal expose [flags] <target>`

- `<target>` accepts a bare port like `3000`, a `host:port`, or an `http(s)://host:port` URL.
- Bare ports resolve to `127.0.0.1:<port>`.
- `--name` is optional. When omitted, the CLI generates a name derived from its local seed and target port.
- `--relays` overrides installed default relays for that run.
- `--default-relays=false` disables the public registry list for that run.

Flags:

```text
--relays          Portal relay API URLs (comma-separated, https only)
--default-relays  Include public registry relays
--name            Public hostname prefix (single DNS label); auto-generated when omitted
--description     Service description metadata
--tags            Service tags metadata (comma-separated)
--thumbnail       Service thumbnail URL metadata
--owner           Service owner metadata
--hide            Hide service from discovery
```

### `portal list [flags]`

- Prints the relay URLs that the CLI will use with the current installed config plus any runtime overrides.
- `--relays` and `--default-relays=false` follow the same semantics as `portal expose`.

Legacy execution compatibility has been removed:

- Use `portal expose ...` explicitly; bare `portal [flags]` is no longer accepted.
- Runtime `APP_*`, `RELAYS`, and `DEFAULT_RELAYS` environment variable fallbacks are no longer used.
- Pass the local target as the required positional `<target>` argument.

## Install Behavior

- `install.sh` installs the downloaded binary as `portal`.
- `install.ps1` installs `portal.exe` for the current Windows user and updates the user `PATH`.
- The installer writes relay defaults to the user config file:
  - Linux/macOS: `${XDG_CONFIG_HOME:-$HOME/.config}/portal/config.json`
  - Windows: `%APPDATA%\portal\config.json`
- Installed defaults currently include the relay that served the installer plus the public registry list.

## Notes

- Multiple relay URLs are registered independently. Each relay gets its own lease ID and public URLs.
- Relay publishes each service at `<name>.<portal root host>`.
- The tunnel consumes one aggregate SDK listener, so the CLI no longer manages per-relay listener loops itself.
- Relay startup and reconnect failures are retried independently in the background. A relay that is down does not stop healthy relays from continuing to serve traffic.
- The tunnel starts once relay URLs pass local validation. Remote compatibility checks, lease registration, and reconnects continue in the background until each relay becomes ready.
- The configured relay list is either `public registry + installed/configured relay URLs` or, with `--default-relays=false`, just the explicit relay URLs. Published public URLs appear only for relays that have registered successfully.
- SDK callers that do not set `ListenerConfig.RetryCount` use infinite retry semantics for each relay.
- Tenant TLS is provisioned automatically through the relay keyless signer. The SDK fetches the relay certificate chain and uses `/v1/sign` for remote signing.
- When the local service is unreachable, the tunnel returns an HTTP 503 page.
