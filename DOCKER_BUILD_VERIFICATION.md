# Docker Build Verification

## Build Process Flow

```
┌─────────────────────────────────────────────────────────┐
│  Stage 1: WASM Builder (rust:1-bullseye)                │
│                                                         │
│  1. Install wasm-pack                                   │
│  2. Run: make build-wasm                                │
│     ├─ wasm-pack build --target web                     │
│     ├─ cp pkg/* → cmd/relay-server/wasm/                │
│     ├─ cp sw-proxy.js → cmd/relay-server/wasm/          │
│     └─ cp sw.js → cmd/relay-server/wasm/                │
│                                                         │
│  Output: cmd/relay-server/wasm/                         │
│    ├── relaydns_wasm.js                                 │
│    ├── relaydns_wasm_bg.wasm                            │
│    ├── relaydns_wasm_sw.js                              │
│    ├── sw-proxy.js          ← E2EE Proxy                │
│    └── sw.js                ← Caching                   │
└─────────────────────────────────────────────────────────┘
         │ COPY --from=wasm-builder
         ▼
┌─────────────────────────────────────────────────────────┐
│  Stage 2: Go Builder (golang:1)                         │
│                                                         │
│  1. Copy go.mod, go.sum                                 │
│  2. go mod download                                     │
│  3. Copy source code                                    │
│  4. COPY WASM files from Stage 1                        │
│  5. Run: make build-server                              │
│     └─ go build (embeds wasm/ via //go:embed)           │
│                                                         │
│  Output: bin/relayserver (18MB with embedded WASM)      │
└─────────────────────────────────────────────────────────┘
         │ COPY --from=builder
         ▼
┌─────────────────────────────────────────────────────────┐
│  Stage 3: Runtime (distroless/static-debian12)          │
│                                                         │
│  Final binary: /usr/bin/relayserver                     │
│    ├─ Contains all WASM files                           │
│    ├─ Contains service workers                          │
│    └─ Ready to serve E2EE proxy                         │
└─────────────────────────────────────────────────────────┘
```

## Files Embedded in Binary

```go
// view.go line 28-29
//go:embed wasm
var wasmFS embed.FS
```

**Embedded files:**
- `/pkg/relaydns_wasm.js` (47KB)
- `/pkg/relaydns_wasm_bg.wasm` (465KB)
- `/pkg/relaydns_wasm_sw.js` (52KB)
- `/sw-proxy.js` (5KB) ← **E2EE Proxy Service Worker**
- `/sw.js` (4KB) ← **Caching Service Worker**

## Verification Commands

### Local Build Test

```bash
# Test Makefile
make clean
make build-wasm

# Verify files copied
ls -lh cmd/relay-server/wasm/
# Should show: sw-proxy.js, sw.js

# Build server
make build-server

# Run
./bin/relayserver
```

### Docker Build Test

```bash
# Build Docker image
docker build -t relaydns-server .

# Run container
docker run -p 4017:4017 relaydns-server

# Test endpoints
curl http://localhost:4017/            # Admin UI
curl http://localhost:4017/sw-proxy.js # Service Worker
curl http://localhost:4017/pkg/relaydns_wasm.js # WASM
```

### Verify Embedded Files

```bash
# Check if files are embedded
docker run relaydns-server strings /usr/bin/relayserver | grep -E "sw-proxy"

# Should output:
# sw-proxy.js
```

## Expected HTTP Endpoints

| Endpoint | File | Description |
|----------|------|-------------|
| `/` | Admin template | Server admin UI |
| `/sw-proxy.js` | `sw-proxy.js` | E2EE proxy service worker |
| `/sw.js` | `sw.js` | WASM caching service worker |
| `/pkg/relaydns_wasm.js` | `relaydns_wasm.js` | WASM binding |
| `/pkg/relaydns_wasm_bg.wasm` | `relaydns_wasm_bg.wasm` | WASM binary |
| `/pkg/relaydns_wasm_sw.js` | `relaydns_wasm_sw.js` | SW WASM binding |
| `/relay` | WebSocket handler | E2EE tunnel |
| `/peer/{id}/*` | Reverse proxy | Server-side proxy |

## Troubleshooting

### Issue: Service Worker 404

**Symptom:**
```bash
curl http://localhost:4017/sw-proxy.js
# 404 Not Found
```

**Cause:** Files not copied in Makefile

**Fix:**
```bash
# Check Makefile has these lines:
make build-wasm
# [wasm] copying service workers and E2EE proxy files...
# cp relaydns/wasm/sw-proxy.js cmd/relay-server/wasm/
# cp relaydns/wasm/sw.js cmd/relay-server/wasm/
```

### Issue: Files Not Embedded

**Cause:** `//go:embed wasm` not working

**Fix:**
1. Check `view.go` has `//go:embed wasm`
2. Verify files exist in `cmd/relay-server/wasm/`
3. Rebuild: `go build`

### Issue: Docker Build Fails

**Symptom:**
```
Step X: cp: cannot stat 'relaydns/wasm/sw-proxy.js': No such file or directory
```

**Cause:** Files not committed to git

**Fix:**
```bash
# These files MUST be committed:
git add -f relaydns/wasm/sw-proxy.js
git add -f relaydns/wasm/sw.js
git commit -m "feat: add E2EE proxy service workers"
```

## Success Criteria

✅ **All checks must pass:**

1. **Local Build**
   ```bash
   make build
   ./bin/relayserver
   curl http://localhost:4017/sw-proxy.js | head -5
   # Output: // Service Worker for RelayDNS Network Proxy
   ```

2. **Docker Build**
   ```bash
   docker build -t test .
   # No errors
   ```

3. **File Size**
   ```bash
   ls -lh bin/relayserver
   # Should be ~18MB (includes embedded WASM)
   ```

4. **Service Worker Registration**
   - Open browser: `http://localhost:4017/`
   - DevTools → Application → Service Workers
   - Should show: `sw-proxy.js` registered

## Deployment Checklist

- [x] Makefile updated with SW file copying
- [x] `sw-proxy.js` exists in `relaydns/wasm/`
- [x] `sw.js` exists in `relaydns/wasm/`
- [x] `.gitignore` allows SW files to be committed
- [x] `view.go` serves SW files from embed
- [x] Dockerfile uses `make build-wasm`
- [ ] All files committed to git
- [ ] Docker image built and tested
- [ ] E2EE proxy verified working

## Next Steps

1. **Commit Changes**
   ```bash
   git add Makefile
   git add relaydns/wasm/{sw-proxy.js,sw.js}
   git commit -m "feat: add E2EE proxy with Docker support"
   ```

2. **Test Docker Build**
   ```bash
   docker build -t relaydns-server:latest .
   docker run -p 4017:4017 relaydns-server:latest
   ```

3. **Push Image**
   ```bash
   docker tag relaydns-server:latest ghcr.io/gosuda/relaydns:latest
   docker push ghcr.io/gosuda/relaydns:latest
   ```
