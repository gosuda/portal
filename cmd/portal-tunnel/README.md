# Portal-tunnel

Portal-tunnel connects a local service to a Portal relay with the legacy CLI shape restored on top of the new core.

## Usage

```bash
./portal-tunnel --host localhost:8080 \
  --relays https://portal.example.com \
  --name myapp \
  --description "Service description" \
  --tags tag1,tag2 \
  --thumbnail https://example.com/thumb.png \
  --owner "Portal Operator"
```

## Flags

```text
--relays        Additional Portal relay server API URLs (comma-separated, https only; appended to registry.json defaults unless --default-relays=false is set) [env: RELAYS]
--default-relays
                Include repository registry.json default relays [env: DEFAULT_RELAYS]
--host          Target host to proxy to (host:port or URL) [env: APP_HOST]
--name          Public hostname prefix (single DNS label) [env: APP_NAME]
--description   Service description metadata [env: APP_DESCRIPTION]
--tags          Service tags metadata (comma-separated) [env: APP_TAGS]
--thumbnail     Service thumbnail URL metadata [env: APP_THUMBNAIL]
--owner         Service owner metadata [env: APP_OWNER]
--hide          Hide service from discovery [env: APP_HIDE]
```

## Notes

- Multiple relay URLs are registered independently. Each relay gets its own lease ID and public URLs.
- The tunnel always starts from the repository-root `registry.json` relay list. `--relays` and `RELAYS` append extra relay URLs on top of those defaults.
- `--default-relays=false` disables the registry defaults and uses only explicit `--relays` or `RELAYS` input.
- Relay publishes each service at `<name>.<portal root host>`.
- Portal-tunnel now consumes one aggregate SDK listener, so the CLI no longer manages per-relay listener loops itself.
- Relay startup and reconnect failures are retried independently in the background. A relay that is down does not stop healthy relays from continuing to serve traffic.
- The tunnel starts once relay URLs pass local validation. Remote compatibility checks, lease registration, and reconnects continue in the background until each relay becomes ready.
- The configured relay list is either `registry.json + explicit relay URLs` or, with `--default-relays=false`, just the explicit relay URLs. Published public URLs appear only for relays that have registered successfully.
- SDK callers that do not set `ListenerConfig.RetryCount` use infinite retry semantics for each relay.
- Tenant TLS is provisioned automatically through the relay keyless signer. The SDK fetches the relay certificate chain and uses `/v1/sign` for remote signing.
- When the local service is unreachable, the tunnel returns an HTTP 503 page.
