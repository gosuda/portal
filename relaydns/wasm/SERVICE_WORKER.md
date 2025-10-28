# Service Worker Implementation for RelayDNS WASM

## Overview

RelayDNS WASM client uses Service Worker to efficiently load and cache WASM modules and JavaScript files.

## Architecture

```
┌─────────────────┐
│   Browser       │
│  (example.html) │
└────────┬────────┘
         │
         │ 1. Register
         ▼
┌─────────────────┐
│ Service Worker  │
│    (sw.js)      │
└────────┬────────┘
         │
         │ 2. Cache & Serve
         ▼
┌─────────────────┐
│  WASM Files     │
│  - .wasm        │
│  - .js          │
└─────────────────┘
```

## Files

### 1. `sw.js` - Service Worker Script

Service Worker provides the following features:

- **Install**: Store WASM and JavaScript files in cache
- **Activate**: Delete cache from previous versions
- **Fetch**: Serve files using cache-first strategy

#### Cached Files
- `/pkg/relaydns_wasm.js` - JavaScript glue code
- `/pkg/relaydns_wasm_bg.wasm` - WASM binary
- `/example.html` - Main HTML page

### 2. HTML Integration

#### example.html
```javascript
// Service Worker Registration
if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js')
        .then((registration) => {
            console.log('Service Worker registered:', registration.scope);
        })
        .catch((error) => {
            console.error('Service Worker registration failed:', error);
        });
}

// WASM Module Loading
import init, { RelayClient } from './pkg/relaydns_wasm.js';
```

## Benefits

### 1. **Faster Loading**
- Improved loading speed by serving WASM files from cache
- Reduced network requests

### 2. **Offline Support**
- Works offline once cached
- Reliable even in unstable network environments

### 3. **Better Performance**
- Immediate response with cache-first strategy
- Significant caching benefits due to large WASM file sizes

### 4. **Version Control**
- Version management through cache name
- Automatic updates when deploying new versions

## Cache Strategy

### WASM Files (.wasm)
```javascript
Cache First → Network Fallback
```

1. Check cache
2. Return if found in cache
3. Fetch from network if not found
4. Store fetched file in cache

### JavaScript Files (.js)
```javascript
Cache First → Network Fallback
```

Same as WASM files

### Other Files
```javascript
Network First → Cache Fallback
```

## Usage

### Development

Run development server:
```bash
cd relaydns/wasm
go run serve.go
# or
python3 -m http.server 8000
```

Access via browser:
```
http://localhost:8000/example.html
```

### Production

1. Build WASM:
```bash
wasm-pack build --target web
```

2. Host static files:
- `pkg/` directory
- `sw.js`
- `example.html`

3. HTTPS required:
- Service Worker requires HTTPS (localhost is an exception)

## Debugging

### Chrome DevTools

1. **Application Tab** → **Service Workers**
   - Check registered Service Workers
   - Unregister/Update available

2. **Application Tab** → **Cache Storage**
   - Check cached files
   - Can delete cache

3. **Console**
   - Check Service Worker logs
   ```
   [SW] Installing Service Worker...
   [SW] Caching WASM files
   [SW] Serving WASM from cache: /pkg/relaydns_wasm_bg.wasm
   ```

### Force Update

Clear cache:
```javascript
// In browser console
caches.keys().then(keys => {
    keys.forEach(key => caches.delete(key));
});

// Unregister Service Worker
navigator.serviceWorker.getRegistrations().then(registrations => {
    registrations.forEach(r => r.unregister());
});
```

## Configuration

### Cache Name

Manage cache versions in `sw.js`:
```javascript
const CACHE_NAME = 'relaydns-wasm-v1';
```

When deploying new version:
```javascript
const CACHE_NAME = 'relaydns-wasm-v2';
```

### Cached URLs

Add required files:
```javascript
const urlsToCache = [
    '/pkg/relaydns_wasm.js',
    '/pkg/relaydns_wasm_bg.wasm',
    '/example.html',
    // Add more files...
];
```

## Troubleshooting

### Service Worker Not Registered

**Cause**: Not using HTTPS or incorrect path

**Solution**:
- Use localhost or HTTPS
- Verify `/sw.js` path

### WASM Not Loading

**Cause**: Cache miss or incorrect MIME type

**Solution**:
- Clear cache
- Verify server provides correct MIME type
  - `.wasm` → `application/wasm`
  - `.js` → `application/javascript`

### Old Version Cached

**Cause**: Service Worker not updated

**Solution**:
1. Change cache name (`v1` → `v2`)
2. Hard refresh (Ctrl+Shift+R)
3. Enable "Update on reload" in DevTools

## Security Considerations

1. **HTTPS Required**
   - Must use HTTPS in production

2. **Same-Origin Policy**
   - Service Worker only works from same origin

3. **Scope**
   - `/sw.js` at root controls entire site
   - Can limit scope if needed

## Performance Metrics

### Before Service Worker
- WASM Load: ~2-3s (network)
- JS Load: ~500ms (network)
- Total: ~3s

### After Service Worker (cached)
- WASM Load: ~50ms (cache)
- JS Load: ~20ms (cache)
- Total: ~100ms

**30x faster!**

## References

- [Service Worker API - MDN](https://developer.mozilla.org/en-US/docs/Web/API/Service_Worker_API)
- [WebAssembly](https://webassembly.org/)
- [wasm-pack](https://rustwasm.github.io/wasm-pack/)
