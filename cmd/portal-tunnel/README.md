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

## Flags

```text
Usage:
        portal-tunnel [OPTIONS] [ARGUMENTS]

Options:
        --relay           Portal relay server API URLs (comma-separated, http/https) [default: http://localhost:4017] [env: RELAYS]
        --host            Target host to proxy to (host:port or URL)  [env: APP_HOST]
        --name            Service name  [env: APP_NAME]
        --protocols       ALPN protocols (comma-separated) [default: http/1.1,h2] [env: APP_PROTOCOLS]
        --description     Service description metadata  [env: APP_DESCRIPTION]
        --tags            Service tags metadata (comma-separated)  [env: APP_TAGS]
        --thumbnail       Service thumbnail URL metadata  [env: APP_THUMBNAIL]
        --owner           Service owner metadata  [env: APP_OWNER]
        --hide            Hide service from discovery (metadata)  [env: APP_HIDE]
        -h, --help        Print this help message and exit
```
