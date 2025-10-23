# Go SDK

This SDK helps you advertise a local TCP service (e.g., HTTP) over libp2p so a RelayDNS server can discover and proxy to it.

## Minimal example (HTTP backend)

```go
package main

import (
    "context"
    "fmt"
    "log"

    sdk "github.com/gosuda/relaydns/sdk/go"
)

func main() {
    ctx := context.Background()

    // Assume your local HTTP server listens on 127.0.0.1:8080
    cli, err := sdk.NewClient(ctx, sdk.ClientConfig{
        Name:      "example-backend",
        TargetTCP: "127.0.0.1:8080",
        ServerURL: "http://relaydns.gosuda.org",
    })
    if err != nil { log.Fatal(err) }

    if err := cli.Start(ctx); err != nil { log.Fatal(err) }
    defer func() { _ = cli.Close() }()

    // Blocks in your real app; here you’d run until SIGINT, etc.
    fmt.Println("status:", cli.ServerStatus())
}
```

## Key concepts

- `TargetTCP`: If set, inbound libp2p streams are proxied directly to the local TCP service using a bidirectional byte pipe.
- `ServerURL`: If set, the client periodically fetches multiaddrs from `/hosts` and attempts to connect to the server’s peer; `ServerStatus()` reports `Connected` only when libp2p connectivity is established.
- Defaults: Reasonable defaults are applied (protocol/topic, advertise intervals, reasonable HTTP timeout, and address sorting that prefers QUIC and local addresses).

## Lifecycle

- `NewClient` creates the libp2p host and applies defaults.
- `Start` installs the stream handler, joins pubsub, begins advertising, and starts background refresh (if `ServerURL` is set).
- `Close` stops background goroutines, removes the handler, and closes the host.

See working examples under `sdk/go/examples/`:

- `http-client`: simple HTTP backend with a status page.
- `chat`: WebSocket chat demo; demonstrates HTTP upgrade tunneling.

