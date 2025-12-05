# Portal-tunnel

Portal-tunnel is a tunneling tool that connects your local services to a [portal network](github.com/gosuda/portal), without any additional configuration.

## Usage

You can run the tunnel using command-line flags or a configuration file.

### Run binary

```bash
./bin/portal-tunnel --host localhost --port 8080 \
  --relay portal.gosuda.org,portal.thumbgo.kr,portal.iwanhae.kr \
  --name <service> \
  --description "Service description" \
  --tags tag1,tag2 \
  --thumbnail https://example.com/thumb.png
```

## Flags

```text
Usage:
  portal-tunnel [flags]

Flags:
      --config string        Path to portal-tunnel config file
      --description string   Service description metadata
  -h, --help                 help for portal-tunnel
      --hide                 Hide service from discovery (metadata)
      --host string          Local host to proxy to when config is not provided (default "localhost")
      --name string          Service name when config is not provided (auto-generated if empty)
      --owner string         Service owner metadata
      --port string          Local port to proxy to when config is not provided (default "4018")
      --relay string         Portal relay server URLs when config is not provided (comma-separated) (default "ws://localhost:4017/relay")
      --tags string          Service tags metadata (comma-separated)
      --thumbnail string     Service thumbnail URL metadata
```
