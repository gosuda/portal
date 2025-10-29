# Portal WASM SDK

WebAssembly SDK for Portal with **mandatory End-to-End Encryption (E2EE) Proxy** functionality.

## Overview

This WASM SDK provides browser-native E2EE proxy capabilities through Service Worker interception. All network traffic is automatically encrypted client-side before being relayed through the server.

## Features

- 🔒 **E2EE Proxy (Mandatory)**: Service Worker intercepts all fetch() requests and encrypts them client-side
- 🔐 **Strong Encryption**: Ed25519 key exchange + ChaCha20-Poly1305 authenticated encryption
- 🌐 **WebSocket Transport**: Real-time bidirectional E2EE tunnels
- 📦 **Protocol Support**: HTTP, WebSocket, and TCP proxying through encrypted channels
- 🎯 **Browser Native**: Runs directly in browser using WebAssembly
- ⚡ **High Performance**: Compiled Rust code optimized for WASM
- 🔄 **Auto Type Detection**: Content-Type based routing (Text/File/Binary/API)

## Architecture

```
Browser Application
      │ fetch()
      ▼
Service Worker (sw-proxy.js)  ← Intercepts ALL requests
      │
      ▼
WASM ProxyEngine              ← E2EE encryption
      │ E2EE WebSocket
      ▼
Relay Server                  ← Relay only (cannot decrypt)
      │ E2EE Tunnel
      ▼
Target Peer                   ← Decrypts and processes
```

## Building

### Prerequisites

- Rust toolchain (1.70+)
- wasm-pack: `cargo install wasm-pack`
- make (for automated builds)

### Quick Build

**Using Makefile (Recommended):**
```bash
# From repository root
make build-wasm

# This will:
# 1. Build WASM module with wasm-pack
# 2. Copy artifacts to cmd/relay-server/wasm/ (for embed)
# 3. Copy Service Worker files (sw-proxy.js, sw.js)
```

**Manual Build:**
```bash
cd portal/wasm

# Build WASM module
wasm-pack build --target web --release

# Deploy to server (copies all files to embed directory)
./deploy-server.sh
```

**Build Server:**
```bash
cd ../../cmd/relay-server

# Build server with embedded WASM
go build -o relay-server

# Run
./relay-server
```

**Access:**
- Admin UI: `http://localhost:4017/`

### Docker Build

```bash
# From repository root
docker build -t portal-server .

# Run
docker run -p 4017:4017 portal-server
```

The Dockerfile uses multi-stage builds:
1. **Stage 1**: Build WASM with Rust + wasm-pack
2. **Stage 2**: Build Go server with embedded WASM
3. **Stage 3**: Minimal runtime image

## Output Files

After building, the following files are generated in `cmd/relay-server/wasm/`:

```
cmd/relay-server/wasm/
├── portal_wasm.js          # WASM JavaScript bindings
├── portal_wasm_bg.wasm     # WASM binary (465KB)
├── portal_wasm_sw.js       # Service Worker bindings
├── portal_wasm.d.ts        # TypeScript definitions
├── sw-proxy.js               # E2EE Proxy Service Worker (ESSENTIAL)
└── sw.js                     # Basic caching Service Worker
```

These files are embedded in the Go server binary via `//go:embed wasm` directive.

## Usage

### For End Users (Browser)

Simply open the page - E2EE Proxy activates automatically:

```html
<!-- Open: http://localhost:4017/ -->

<!-- Service Worker auto-registers -->
<script>
navigator.serviceWorker.register('/sw-proxy.js')
  .then(() => console.log('E2EE Proxy activated'));
</script>

<!-- Now ALL fetch() requests are E2EE encrypted! -->
<script>
fetch('https://api.github.com/zen')
  .then(r => r.text())
  .then(console.log);
// ↑ Automatically encrypted via E2EE tunnel!
</script>
```

### For Developers (JavaScript)

```javascript
import init, { RelayClient } from '/pkg/portal_wasm.js';

// Initialize WASM
await init();

// Connect to relay server
const client = await RelayClient.connect('ws://localhost:4017/relay');

// Register a service
await client.registerLease('my-service', ['http/1.1', 'h2']);

// Get server info
const info = await client.getRelayInfo();
console.log('Active leases:', info.leases);
```

### For Go Applications

See [Go SDK Documentation](../../sdk/)

## Documentation

- **[E2EE_PROXY_INTEGRATION.md](E2EE_PROXY_INTEGRATION.md)** - Comprehensive integration guide
- **[E2EE_PROXY_DEPLOYMENT.md](../../E2EE_PROXY_DEPLOYMENT.md)** - Korean deployment guide
- **[SERVICE_WORKER.md](SERVICE_WORKER.md)** - Service Worker implementation details
- **[BUILDING.md](BUILDING.md)** - Detailed build instructions
- **[INTEGRATION_TEST_GUIDE.md](INTEGRATION_TEST_GUIDE.md)** - Testing procedures
- **[USAGE.md](USAGE.md)** - API usage examples

## Testing

```bash
# Unit tests
cd portal/wasm
cargo test

# Integration tests
./integration-test.sh

# Browser test
# 1. Start server: cd ../../cmd/relay-server && ./relay-server
# 2. Open: http://localhost:4017
# 3. Check DevTools Console for "ProxyEngine ready"
```

## Security

### End-to-End Encryption

- **Algorithm**: Ed25519 key exchange + X25519 ECDH + ChaCha20-Poly1305
- **Key Management**: Ephemeral keys per connection
- **Server Role**: Relay only (cannot decrypt)

### Content-Type Based Type Detection

Service Worker automatically determines message type:

| Content-Type | Type | Handling |
|-------------|------|----------|
| `application/json` | Text/API | JSON serialization |
| `multipart/form-data` | File | Chunked streaming |
| `application/octet-stream` | Binary | Raw bytes |
| `text/*` | Text | UTF-8 encoding |

## Troubleshooting

### Service Worker 404 Error

```bash
# Rebuild and deploy
cd portal/wasm
./deploy-server.sh
cd ../../cmd/relay-server
go build -o relay-server
```

### WASM Initialization Failed

```bash
# Check files are served correctly
curl http://localhost:4017/pkg/portal_wasm.js
curl http://localhost:4017/pkg/portal_wasm_bg.wasm
curl http://localhost:4017/sw-proxy.js
```

### WebSocket Connection Refused

```bash
# Verify server is running and URL is correct
# Correct:   ws://localhost:4017/relay
# Incorrect: ws://localhost:4017/
```

## Performance

| Metric | Standard | E2EE Proxy | Overhead |
|--------|----------|------------|----------|
| First Load | 2-3s | 2.5-3.5s | +500ms (WASM init) |
| Cached Load | 2s | 100ms | -95% (Service Worker) |
| Request Latency | 50ms | 80ms | +30ms (encryption) |
| Throughput | 100MB/s | 90MB/s | -10% (crypto) |

## License

MIT OR Apache-2.0
