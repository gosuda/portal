# Portal WASM Usage Guide

## 📦 Installation and Build

### 1. Prerequisites

```bash
# Install Rust (if not already installed)
# Installed via rustup

# Install wasm-pack (if not already installed)
curl https://rustwasm.github.io/wasm-pack/installer/init.sh -sSf | sh

# Add WASM target
rustup target add wasm32-unknown-unknown
```

### 2. Build

```bash
cd e:/git/portal/portal/wasm

# Development build (fast, includes debug info)
wasm-pack build --target web --dev

# Production build (optimized, smaller size)
wasm-pack build --target web --release

# Build output is generated in pkg/ folder
```

## 🚀 Usage

### Using in Browser

#### 1. Prepare HTML File

```html
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Portal WASM Client</title>
</head>
<body>
    <h1>Portal WASM Client</h1>
    <div id="status">Initializing...</div>
    <pre id="output"></pre>

    <script type="module">
        // Import WASM module
        import init, { RelayClient } from './pkg/portal_wasm.js';

        async function main() {
            try {
                // 1. Initialize WASM
                await init();
                console.log('✓ WASM initialized');

                // 2. Connect to Portal server
                const client = await new RelayClient('wss://your-relay-server.com/ws');
                document.getElementById('status').textContent = 'Connected!';

                // 3. Get server info
                const info = await client.getRelayInfo();
                console.log('Server info:', info);
                document.getElementById('output').textContent =
                    JSON.stringify(info, null, 2);

                // 4. Check my credential ID
                const myId = client.getCredentialId();
                console.log('My ID:', myId);

                // 5. Register lease
                await client.registerLease('my-service', ['http/1.1', 'h2']);
                console.log('✓ Lease registered');

            } catch (error) {
                console.error('Error:', error);
                document.getElementById('status').textContent = 'Error: ' + error;
            }
        }

        main();
    </script>
</body>
</html>
```

#### 2. Run Local Server

```bash
# Python HTTP server
cd e:/git/portal/portal/wasm
python -m http.server 8000

# Or Node.js
npx serve .

# Open in browser
# http://localhost:8000/example.html
```

### API Reference

#### RelayClient

##### Constructor

```typescript
new RelayClient(serverUrl: string): Promise<RelayClient>
```

Connect to Portal server.

**Parameters:**
- `serverUrl`: WebSocket server URL (e.g., `wss://relay.example.com/ws`)

**Example:**
```javascript
const client = await new RelayClient('wss://relay.example.com/ws');
```

##### getRelayInfo()

```typescript
getRelayInfo(): Promise<RelayInfo>
```

Get server information and list of active leases.

**Returns:**
```typescript
interface RelayInfo {
    identity: {
        id: string;
        public_key: string;
    };
    address: string[];
    leases: string[];
}
```

**Example:**
```javascript
const info = await client.getRelayInfo();
console.log('Server ID:', info.identity.id);
console.log('Active leases:', info.leases);
```

##### getCredentialId()

```typescript
getCredentialId(): string
```

Returns the client's credential ID (Ed25519 public key hash).

**Example:**
```javascript
const myId = client.getCredentialId();
console.log('My credential ID:', myId);
```

##### registerLease()

```typescript
registerLease(name: string, alpns: string[]): Promise<void>
```

Register a service.

**Parameters:**
- `name`: Service name
- `alpns`: List of ALPN protocols (e.g., `['http/1.1', 'h2']`)

**Example:**
```javascript
await client.registerLease('my-api-service', ['http/1.1', 'h2']);
```

##### requestConnection()

```typescript
requestConnection(leaseId: string, alpn: string): Promise<string>
```

Request a connection to another peer.

**Parameters:**
- `leaseId`: Lease ID to connect to
- `alpn`: ALPN protocol

**Example:**
```javascript
const result = await client.requestConnection('target-lease-id', 'http/1.1');
console.log('Connection result:', result);
```

## 🔧 Advanced Usage

### Using with TypeScript

```typescript
import init, { RelayClient } from './pkg/portal_wasm';

interface RelayInfo {
    identity: {
        id: string;
        public_key: string;
    };
    address: string[];
    leases: string[];
}

async function connectToRelay(url: string): Promise<RelayClient> {
    await init();
    return await new RelayClient(url);
}

async function main() {
    const client = await connectToRelay('wss://relay.example.com/ws');
    const info: RelayInfo = await client.getRelayInfo();
    console.log(info);
}
```

### Using with React

```tsx
import { useEffect, useState } from 'react';
import init, { RelayClient } from './pkg/portal_wasm';

function App() {
    const [client, setClient] = useState<RelayClient | null>(null);
    const [info, setInfo] = useState<any>(null);

    useEffect(() => {
        async function connect() {
            await init();
            const c = await new RelayClient('wss://relay.example.com/ws');
            setClient(c);

            const i = await c.getRelayInfo();
            setInfo(i);
        }
        connect();
    }, []);

    if (!info) return <div>Loading...</div>;

    return (
        <div>
            <h1>Server ID: {info.identity.id}</h1>
            <h2>Active Leases: {info.leases.length}</h2>
        </div>
    );
}
```

### Using with Vue

```vue
<template>
    <div>
        <h1>Portal Client</h1>
        <div v-if="loading">Connecting...</div>
        <div v-else-if="error">Error: {{ error }}</div>
        <div v-else>
            <p>Server ID: {{ info?.identity.id }}</p>
            <p>Leases: {{ info?.leases.length }}</p>
        </div>
    </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue';
import init, { RelayClient } from './pkg/portal_wasm';

const client = ref<RelayClient | null>(null);
const info = ref<any>(null);
const loading = ref(true);
const error = ref('');

onMounted(async () => {
    try {
        await init();
        client.value = await new RelayClient('wss://relay.example.com/ws');
        info.value = await client.value.getRelayInfo();
    } catch (e: any) {
        error.value = e.message;
    } finally {
        loading.value = false;
    }
});
</script>
```

## 📝 Error Handling

```javascript
try {
    const client = await new RelayClient('wss://relay.example.com/ws');
    await client.registerLease('my-service', ['http/1.1']);
} catch (error) {
    if (error.toString().includes('WebSocket connection failed')) {
        console.error('Cannot connect to server');
    } else if (error.toString().includes('lease registration rejected')) {
        console.error('Lease registration rejected');
    } else {
        console.error('Unknown error:', error);
    }
}
```

## 🔍 Debugging

### Browser DevTools

```javascript
// Check WASM loading
console.log('WASM memory:', WebAssembly.Memory);

// Error details
window.addEventListener('error', (e) => {
    console.error('Global error:', e);
});

// WASM initialization failure
init().catch(err => {
    console.error('WASM init failed:', err);
});
```

### Network Monitoring

Browser DevTools → Network tab:
- Check WebSocket connection status
- Inspect message payloads
- Analyze connection timing

## 🚦 Real-world Example

### Simple Chat Application

```javascript
import init, { RelayClient } from './pkg/portal_wasm.js';

class ChatApp {
    constructor() {
        this.client = null;
    }

    async connect(serverUrl, username) {
        await init();
        this.client = await new RelayClient(serverUrl);

        // Register chat room as lease
        await this.client.registerLease(`chat-${username}`, ['chat-protocol']);

        console.log(`✓ Connected as ${username}`);
        console.log(`✓ Credential ID: ${this.client.getCredentialId()}`);
    }

    async listOnlineUsers() {
        const info = await this.client.getRelayInfo();
        return info.leases.filter(id => id.startsWith('chat-'));
    }

    async sendMessage(targetUser, message) {
        await this.client.requestConnection(
            `chat-${targetUser}`,
            'chat-protocol'
        );
        // Message sending logic...
    }
}

// Usage
const chat = new ChatApp();
await chat.connect('wss://relay.example.com/ws', 'Alice');
const users = await chat.listOnlineUsers();
console.log('Online users:', users);
```

## 📚 More Examples

- [example.html](./example.html) - Basic example
- [README.md](./README.md) - Project overview
- [BUILDING.md](./BUILDING.md) - Detailed build guide

## 🐛 Known Issues

1. **WebSocket Connection Delay in Safari**: Safari may experience slight delays when establishing WebSocket connections.
2. **File Size**: WASM file is approximately ~500KB on first build. gzip compression recommended.
3. **Cross-Origin**: CORS configuration may be required.

## 💡 Tips

- **Reduce Build Size**: Use `wasm-opt` for additional optimization
  ```bash
  wasm-opt -Oz -o optimized.wasm pkg/portal_wasm_bg.wasm
  ```

- **Improve Loading Speed**:
  ```javascript
  // Use streaming initialization
  const response = await fetch('./pkg/portal_wasm_bg.wasm');
  await init(response);
  ```

- **Better Error Handling**:
  ```javascript
  window.addEventListener('unhandledrejection', event => {
      console.error('Promise rejection:', event.reason);
  });
  ```
