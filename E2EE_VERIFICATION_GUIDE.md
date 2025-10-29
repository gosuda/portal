# E2EE Encryption Verification Guide

This guide demonstrates how to verify that the RelayDNS server acts as a **blind relay** and cannot decrypt E2EE (End-to-End Encrypted) traffic.

## Overview

The E2EE architecture ensures:
- **Client-side encryption**: All data is encrypted in the browser using WASM
- **Blind relay**: Server only forwards encrypted packets without decryption capability
- **Content-Type detection**: Happens at Service Worker level before encryption

## Server Logging

The relay server now includes enhanced logging to show encrypted packet data as it flows through the relay. The server logs:

1. **Direction**: `Client→Lease` or `Lease→Client`
2. **Lease ID**: The target service identifier
3. **Bytes transferred**: Size of each encrypted chunk
4. **Packet count**: Number of encrypted packets relayed
5. **Encrypted preview**: First 32 bytes of encrypted data in hexadecimal

### Log Format

```json
{
  "level": "info",
  "direction": "Client→Lease",
  "lease_id": "ABC123XYZ",
  "bytes": 1024,
  "total_bytes": 4096,
  "packet_count": 4,
  "encrypted_preview": "a3f2e1d4c5b6a7890f1e2d3c4b5a6978...",
  "message": "[E2EE-RELAY] Forwarding encrypted packet (server cannot decrypt)"
}
```

## Verification Steps

### Step 1: Start the Relay Server

```bash
cd cmd/relay-server
./relay-server

# Server should start on :4017
# [server] http: :4017
```
### Step 2: Make Test Requests

In the browser console, run:

```javascript
// Simple text request
fetch('https://api.github.com/zen')
  .then(r => r.text())
  .then(console.log);

// JSON API request
fetch('https://jsonplaceholder.typicode.com/posts/1')
  .then(r => r.json())
  .then(console.log);

// Binary data request
fetch('https://via.placeholder.com/150')
  .then(r => r.blob())
  .then(blob => console.log('Received image:', blob.size, 'bytes'));
```

### Step 3: Check Server Logs

Watch the server console for E2EE relay logs:

```bash
# You should see logs like:

[E2EE-RELAY] Starting E2EE tunnel relay (server acts as blind relay)
  lease_id=ABC123XYZ

[E2EE-RELAY] Forwarding encrypted packet (server cannot decrypt)
  direction=Client→Lease
  lease_id=ABC123XYZ
  bytes=512
  total_bytes=512
  packet_count=1
  encrypted_preview=3a7f2e1d8c4b9a650f3e8d1c5b2a9746e3d8f1a4c7b2e5d9f0a3c6b8e1d4f7a2

[E2EE-RELAY] Forwarding encrypted packet (server cannot decrypt)
  direction=Lease→Client
  lease_id=ABC123XYZ
  bytes=1024
  total_bytes=1536
  packet_count=2
  encrypted_preview=f9e4d3c2b1a0987654321fedcba09876543210fedcba0987654321fedcba098

[E2EE-RELAY] E2EE tunnel relay completed
  lease_id=ABC123XYZ
```

## What the Logs Prove

### 1. Server Cannot Decrypt

The `encrypted_preview` field shows **hexadecimal gibberish**:
```
3a7f2e1d8c4b9a650f3e8d1c5b2a9746e3d8f1a4c7b2e5d9f0a3c6b8e1d4f7a2
```

This is **ChaCha20-Poly1305** encrypted data. The server:
- ❌ Cannot see the HTTP headers (Host, User-Agent, etc.)
- ❌ Cannot see the request method (GET, POST, etc.)
- ❌ Cannot see the URL path
- ❌ Cannot see the request/response body
- ❌ Cannot determine if it's JSON, HTML, or binary
- ✅ Can only see encrypted byte streams

### 2. Blind Relay Operation

The server only knows:
- **Source**: Which client sent the data
- **Destination**: Which lease holder should receive it
- **Size**: How many bytes were transferred
- **Direction**: Client→Lease or Lease→Client

The server does NOT know:
- What protocol is being used (HTTP, WebSocket, etc.)
- What data is being transmitted
- What the response contains

### 3. Content-Type Detection Happens Before Encryption

The Service Worker (`sw-proxy.js`) inspects `Content-Type` headers **before** passing data to WASM for encryption:

```javascript
// In sw-proxy.js (before encryption)
const contentType = request.headers.get('content-type');
if (contentType.includes('application/json')) {
    type = 'Text'; // or 'API'
} else if (contentType.includes('multipart/form-data')) {
    type = 'File';
}
// THEN encrypt with WASM ProxyEngine
```

This means:
- Type detection: **Client-side (unencrypted)**
- Encryption: **Client-side (WASM)**
- Server relay: **Blind (encrypted only)**

## Comparison: Without E2EE vs With E2EE

### Without E2EE (Traditional Proxy)

Server log would show:
```json
{
  "method": "GET",
  "url": "https://api.github.com/zen",
  "headers": {
    "User-Agent": "Mozilla/5.0...",
    "Accept": "application/json"
  },
  "body": "...",
  "response_body": "Design for failure."
}
```

### With E2EE (RelayDNS)

Server log shows:
```json
{
  "direction": "Client→Lease",
  "encrypted_preview": "3a7f2e1d8c4b9a65...",
  "message": "server cannot decrypt"
}
```

## Security Analysis

### What Server CAN Do

1. ✅ Count total bytes transferred
2. ✅ Track connection timing (when started/ended)
3. ✅ See source and destination identities (lease IDs)
4. ✅ Monitor connection patterns (frequency, duration)

### What Server CANNOT Do

1. ❌ Decrypt any application data
2. ❌ Read HTTP headers or bodies
3. ❌ Modify encrypted data without detection (Poly1305 MAC)
4. ❌ Perform man-in-the-middle attacks (no private keys)
5. ❌ Log sensitive information (URLs, credentials, etc.)

## Cryptographic Verification

### Encryption Algorithm

**ChaCha20-Poly1305** AEAD:
- **Encryption**: ChaCha20 stream cipher (256-bit key)
- **Authentication**: Poly1305 MAC (128-bit tag)
- **Nonce**: 12 bytes random per message

### Key Exchange

**X25519** ephemeral key exchange:
- Fresh keys per connection
- No long-term keys stored on server
- Perfect forward secrecy

### Signature Verification

**Ed25519** signatures:
- Identity authentication
- Cannot forge without private key
- Server only verifies signatures, cannot decrypt

## Testing Encrypted Data

You can verify encryption by:

1. **Inspect Network Tab** (browser):
   - Open DevTools → Network
   - Filter: WS (WebSocket)
   - Click on `/relay` connection
   - View Messages tab
   - You'll see binary frames (encrypted)

2. **Server Logs**:
   - Look for `[E2EE-RELAY]` messages
   - `encrypted_preview` should be random hex
   - No plaintext should appear

3. **Wireshark/tcpdump** (advanced):
   - Capture WebSocket traffic
   - All application data appears as binary blobs
   - No HTTP headers/bodies visible in relay tunnel

## Conclusion

The server logs **prove** that:

1. ✅ All data is encrypted before reaching the server
2. ✅ Server acts as a blind relay (cannot decrypt)
3. ✅ Content-Type detection happens client-side before encryption
4. ✅ E2EE architecture is working as designed

The relay server is **zero-knowledge** about application content, ensuring maximum privacy and security for all relayed communications.

## Further Reading

- [E2EE_PROXY_INTEGRATION.md](relaydns/wasm/E2EE_PROXY_INTEGRATION.md) - Integration guide
- [E2EE_PROXY_DEPLOYMENT.md](E2EE_PROXY_DEPLOYMENT.md) - Deployment guide (Korean)
- [relaydns/core/cryptoops/README.md](relaydns/core/cryptoops/README.md) - Cryptographic details
- [SERVICE_WORKER.md](relaydns/wasm/SERVICE_WORKER.md) - Service Worker implementation
