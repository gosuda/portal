# Portal-tunnel

Portal-tunnel connects a local service to a Portal relay with the legacy CLI shape restored on top of the new core.

## Usage

```bash
./portal-tunnel --host localhost:8080 \
  --relay https://portal.example.com \
  --name myapp \
  --description "Service description" \
  --tags tag1,tag2 \
  --thumbnail https://example.com/thumb.png \
  --owner "Portal Operator"
```

## Flags

```text
--relay         Portal relay server API URLs (comma-separated, https only) [env: RELAYS]
--host          Target host to proxy to (host:port or URL) [env: APP_HOST]
--name          Service name [env: APP_NAME]
--description   Service description metadata [env: APP_DESCRIPTION]
--tags          Service tags metadata (comma-separated) [env: APP_TAGS]
--thumbnail     Service thumbnail URL metadata [env: APP_THUMBNAIL]
--owner         Service owner metadata [env: APP_OWNER]
--hide          Hide service from discovery [env: APP_HIDE]
```

## Notes

- The current runtime accepts multiple relay URLs but uses the first one.
- Tenant TLS is provisioned automatically through the relay keyless signer. The SDK fetches the relay certificate chain and uses `/v1/sign` for remote signing.
- When the local service is unreachable, the tunnel returns an HTTP 503 page.
