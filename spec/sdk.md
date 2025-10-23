# RelayDNS SDK Development Specification

> **Version**: 1.0.0
> **Last Updated**: 2025-10-23
> **Target Languages**: Go, TypeScript, Python, Rust

---

## 📋 Table of Contents

1. [Overview](#overview)
2. [Core Concepts](#core-concepts)
3. [Architecture](#architecture)
4. [API Specification](#api-specification)
5. [Implementation Guide](#implementation-guide)
6. [Language-Specific Guides](#language-specific-guides)
7. [Testing](#testing)
8. [Example Code](#example-code)

---

## Overview

### What is RelayDNS?

RelayDNS is a **libp2p-based lightweight P2P proxy layer**. It makes local services (SSH, HTTP, WebSocket, etc.) behind NAT externally accessible and provides DNS-style simple service discovery.

### Why Do We Need an SDK?

By **embedding** a RelayDNS client in your application:
- Automatically advertise local services to the P2P network
- Enable external access even behind NAT/firewalls
- Operate without centralized reverse proxies

### The Role of the SDK

```
┌─────────────┐
│  Your App   │
├─────────────┤
│  SDK Client │ ← This is what we'll implement
├─────────────┤
│   libp2p    │
└─────────────┘
      ↕ P2P
┌─────────────┐
│  RelayDNS   │
│   Server    │
└─────────────┘
```

---

## Core Concepts

### 1. libp2p

**A peer-to-peer networking framework**
- Each node has a unique **Peer ID**
- Supports multiple **Transports** (TCP, QUIC, WebSocket)
- NAT traversal capabilities (Relay, Hole Punching)

### 2. GossipSub

**A Pub/Sub messaging protocol**
- Subscribe to topics to receive messages
- Broadcast peer advertisements
- Provides distributed service discovery

### 3. Stream Handler

**Proxies libp2p streams to local TCP**
- Automatically forwards connections from other peers to local services
- Bidirectional byte copying
- Error handling and cleanup

### 4. Bootstrap Peers

**Network entry points**
- Known peers for initial connection
- Dynamically fetched from the server's `/health` endpoint

---

## Architecture

### Overall Structure

```
┌──────────────────────────────────────────────────────────┐
│                      Your Application                     │
│  (HTTP Server, SSH Daemon, WebSocket Service, etc.)      │
└───────────────────────────┬──────────────────────────────┘
                            │ TCP (127.0.0.1:8081)
┌───────────────────────────▼──────────────────────────────┐
│                      SDK Client                           │
│  ┌────────────────────────────────────────────────────┐  │
│  │ Health Client   : Fetch bootstrap peers           │  │
│  ├────────────────────────────────────────────────────┤  │
│  │ libp2p Node     : P2P networking (TCP/QUIC/WS)     │  │
│  ├────────────────────────────────────────────────────┤  │
│  │ GossipSub       : Peer discovery (Pub/Sub)         │  │
│  ├────────────────────────────────────────────────────┤  │
│  │ Stream Handler  : Stream → TCP proxy               │  │
│  ├────────────────────────────────────────────────────┤  │
│  │ Advertisement   : Advertise service every 30s      │  │
│  └────────────────────────────────────────────────────┘  │
└───────────────────────────┬──────────────────────────────┘
                            │ libp2p streams
┌───────────────────────────▼──────────────────────────────┐
│                    RelayDNS Server                        │
│  • Bootstrap coordinator                                  │
│  • GossipSub topic relay                                  │
│  • Admin UI (peer list)                                   │
│  • HTTP/WS proxy endpoints                                │
└──────────────────────────────────────────────────────────┘
```

### Data Flow

#### 1. Startup Sequence
```
1. [SDK] GET /health → [Server]
   ← Returns bootstrap peer addresses

2. [SDK] Create libp2p node
   • Generate Ed25519 key
   • Enable TCP/QUIC transports
   • Configure Noise security

3. [SDK] Connect to bootstrap peers
   • Dial each peer
   • Join the network

4. [SDK] Join GossipSub topic
   • Subscribe to "/relaydns/peers/1.0.0"
   • Start receiving advertisements

5. [SDK] Register stream handler
   • "/relaydns/1.0.0" protocol
   • Wait for inbound connections

6. [SDK] Publish own advertisement
   • Broadcast peer information
   • Repeat every 30 seconds
```

#### 2. Connection Flow
```
[User] → [Server] → [SDK] → [Your App]

1. User selects peer in server UI
2. Server opens libp2p stream
3. SDK receives stream
4. SDK opens local TCP connection (127.0.0.1:8081)
5. Start bidirectional byte copying
```

---

## API Specification

### Configuration

All SDKs must provide the same configuration interface.

#### Required Fields (3)

| Field | Type | Description | Example |
|-------|------|-------------|---------|
| `ServerURL` | string | RelayDNS server base URL | `"http://localhost:8080"` |
| `TargetTCP` | string | Local service address | `"127.0.0.1:8081"` |
| `Name` | string | Display name for UI | `"my-http-service"` |

#### Optional Fields (3)

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `Protocol` | string | `"/relaydns/1.0.0"` | libp2p protocol string |
| `Topic` | string | `"/relaydns/peers/1.0.0"` | GossipSub topic |
| `Bootstrap` | []string | `[]` (fetched from server) | Custom bootstrap peers |

#### Validation Rules

- `ServerURL`: Must include `http://` or `https://`
- `TargetTCP`: Must be in `host:port` format
- `Name`: Non-empty string
- `Bootstrap`: Valid multiaddr format (e.g., `/ip4/1.2.3.4/tcp/4001/p2p/QmXxX...`)

### Client Interface

#### Methods

```
NewClient(config: ClientConfig) -> (Client, Error)
```
- Create a new client instance
- Validate configuration
- Initialize libp2p host
- Configure GossipSub
- **Does not start yet** (requires Start call)

```
Start() -> Error
```
- Connect to bootstrap peers
- Join GossipSub topic
- Register stream handler
- Start advertising
- Runs in background without blocking

```
Close() -> Error
```
- Close all connections
- Unsubscribe from GossipSub
- Clean up resources
- Graceful shutdown

```
PeerID() -> string
```
- Returns the client's libp2p Peer ID
- Read-only
- For debugging/logging

### Health Endpoint

#### Request
```http
GET {ServerURL}/health
```

#### Response
```json
{
  "status": "healthy",
  "peers": [
    "/ip4/1.2.3.4/tcp/4001/p2p/QmServerPeerID",
    "/ip4/5.6.7.8/tcp/4001/p2p/QmOtherPeerID"
  ],
  "version": "1.0.0"
}
```

#### Error Handling
- Server unreachable: Clear error message
- JSON parsing failure: Configuration validation failure
- Empty peers array: Warning log, continue (can use fallback bootstrap)

### Advertisement Message

#### Format (JSON)
```json
{
  "peer_id": "QmXxXxXxXxXxXxXxXxXxXxXxXxXxX",
  "name": "my-http-service",
  "protocol": "/relaydns/1.0.0",
  "timestamp": 1729728000
}
```

#### Publishing Cycle
- Initial: Immediately after Start()
- Thereafter: Every 30 seconds
- On shutdown: Stop advertising (automatic)

---

## Implementation Guide

### Step-by-Step Implementation

#### Step 1: Health Endpoint Client

**Goal**: Fetch bootstrap peer information from server

**Implementation**:
```
function fetchHealth(serverURL):
    url = serverURL + "/health"
    response = HTTP_GET(url)
    if response.status != 200:
        throw Error("Health check failed")

    data = JSON_PARSE(response.body)
    return data.peers
```

**Testing**:
```bash
curl http://localhost:8080/health
# Response should contain "peers" array
```

#### Step 2: libp2p Node Initialization

**Goal**: Set up P2P networking foundation

**Required Components**:
1. **Identity**: Generate Ed25519 keypair
2. **Transports**: Enable TCP, QUIC
3. **Security**: Noise protocol
4. **Multiplexing**: Yamux
5. **NAT**: Enable AutoNAT, Relay, Hole Punching

**Pseudocode**:
```
function createLibp2pHost():
    privateKey = generateEd25519Key()

    host = newLibp2pHost({
        identity: privateKey,
        transports: [TCP, QUIC],
        security: Noise,
        muxer: Yamux,
        enableNAT: true,
        enableRelay: true,
        enableHolePunching: true
    })

    return host
```

#### Step 3: GossipSub Configuration

**Goal**: Enable Pub/Sub messaging

**Implementation**:
```
function setupGossipSub(host):
    pubsub = newGossipSub(host)
    topic = pubsub.join("/relaydns/peers/1.0.0")
    subscription = topic.subscribe()

    return (pubsub, topic, subscription)
```

**Message Receiving**:
```
function handleMessages(subscription):
    while message = subscription.next():
        data = JSON_PARSE(message.data)
        log("Received peer: " + data.name + " (" + data.peer_id + ")")
```

#### Step 4: Bootstrap Connection

**Goal**: Join the network

**Implementation**:
```
function connectBootstrap(host, bootstrapAddrs):
    for addr in bootstrapAddrs:
        try:
            multiaddr = parseMultiaddr(addr)
            peerInfo = extractPeerInfo(multiaddr)
            host.connect(peerInfo)
            log("Connected to bootstrap: " + peerInfo.id)
        catch error:
            log("Failed to connect: " + addr)
            continue  // Try next peer
```

**Note**: Don't terminate the program if all bootstrap connections fail, just log warnings

#### Step 5: Stream Handler

**Goal**: Proxy inbound connections to local service

**Implementation**:
```
function registerStreamHandler(host, protocol, targetTCP):
    host.setStreamHandler(protocol, function(stream):
        handleStream(stream, targetTCP)
    )

function handleStream(stream, targetTCP):
    try:
        // Connect to local service
        tcpConn = connectTCP(targetTCP, timeout=5s)

        // Bidirectional copy
        go copyAsync(stream -> tcpConn)
        go copyAsync(tcpConn -> stream)

        // Clean up when either side closes
        waitForEither()

    finally:
        stream.close()
        tcpConn.close()
```

**Error Handling**:
- Local service down: Close stream immediately, log error
- Copy error: Clean up both connections
- Timeout: Give up after 5 seconds

#### Step 6: Advertisement Loop

**Goal**: Periodically advertise service

**Implementation**:
```
function startAdvertisement(pubsub, topic, peerID, name, protocol):
    // Advertise immediately
    publishAdvertisement(topic, peerID, name, protocol)

    // Repeat every 30 seconds
    ticker = newTicker(30 seconds)
    while true:
        wait(ticker.tick)
        publishAdvertisement(topic, peerID, name, protocol)

function publishAdvertisement(topic, peerID, name, protocol):
    message = {
        "peer_id": peerID,
        "name": name,
        "protocol": protocol,
        "timestamp": currentUnixTime()
    }

    data = JSON_STRINGIFY(message)
    topic.publish(data)
```

### Error Handling Strategy

#### 1. Startup Errors

| Error | Cause | Handling |
|-------|-------|----------|
| Health endpoint failure | Server down or network issue | Fail fast, clear error |
| Bootstrap connection failure | Bad address or firewall | Warning log, continue |
| GossipSub subscription failure | libp2p configuration issue | Fail fast, return error |

#### 2. Runtime Errors

| Error | Cause | Handling |
|-------|-------|----------|
| Local service down | Target TCP connection failure | Close stream, log error |
| Peer disconnection | Network instability | Retry with backoff |
| Stream error | Protocol mismatch | Clean up stream, log error |

#### 3. Reconnection Logic

```
function reconnectWithBackoff():
    delay = 1 second
    maxDelay = 60 seconds

    while true:
        try:
            fetchHealth()
            connectBootstrap()
            rejoinGossipSub()
            return SUCCESS
        catch error:
            log("Reconnect failed, retry in " + delay)
            sleep(delay)
            delay = min(delay * 2, maxDelay)
```

---

## Language-Specific Guides

### Go

#### Dependencies
```go
require (
    github.com/libp2p/go-libp2p v0.32.0
    github.com/libp2p/go-libp2p-pubsub v0.10.0
    github.com/multiformats/go-multiaddr v0.12.0
)
```

#### Core Types
```go
type ClientConfig struct {
    ServerURL string
    TargetTCP string
    Name      string
    Protocol  string
    Topic     string
    Bootstrap []string
}

type Client struct {
    config ClientConfig
    host   host.Host
    ps     *pubsub.PubSub
    topic  *pubsub.Topic
    sub    *pubsub.Subscription
}
```

#### Patterns
- Use Context: `ctx context.Context`
- Cleanup: `defer client.Close()`
- Concurrency: goroutines + channels

#### Example
```go
ctx := context.Background()
client, err := sdk.NewClient(ctx, sdk.ClientConfig{
    ServerURL: "http://localhost:8080",
    TargetTCP: "127.0.0.1:8081",
    Name:      "demo",
})
if err != nil {
    log.Fatal(err)
}
defer client.Close()

if err := client.Start(ctx); err != nil {
    log.Fatal(err)
}

select {} // Keep running
```

### TypeScript

#### Dependencies
```json
{
  "dependencies": {
    "libp2p": "^1.0.0",
    "@chainsafe/libp2p-gossipsub": "^12.0.0",
    "@libp2p/tcp": "^9.0.0",
    "@libp2p/noise": "^14.0.0"
  }
}
```

#### Core Types
```typescript
interface ClientConfig {
  serverURL: string;
  targetTCP: string;
  name: string;
  protocol?: string;
  topic?: string;
  bootstrap?: string[];
}

class Client {
  constructor(config: ClientConfig);
  async start(): Promise<void>;
  async close(): Promise<void>;
  getPeerID(): string;
}
```

#### Patterns
- All I/O uses `async/await`
- Cleanup: `try/finally` or `using`
- Browser: WebRTC only (requires signaling)

#### Example
```typescript
const client = new Client({
  serverURL: 'http://localhost:8080',
  targetTCP: '127.0.0.1:8081',
  name: 'demo'
});

await client.start();
console.log(`Peer ID: ${client.getPeerID()}`);

process.on('SIGINT', async () => {
  await client.close();
  process.exit(0);
});
```

### Python

#### Dependencies
```toml
[project]
dependencies = [
    "libp2p>=0.1.0",
    "aiohttp>=3.9.0",
]
```

#### Core Types
```python
@dataclass
class ClientConfig:
    server_url: str
    target_tcp: str
    name: str
    protocol: str = "/relaydns/1.0.0"
    topic: str = "/relaydns/peers/1.0.0"
    bootstrap: List[str] = field(default_factory=list)

class Client:
    def __init__(self, config: ClientConfig): ...
    async def start(self) -> None: ...
    async def close(self) -> None: ...
    def peer_id(self) -> str: ...
```

#### Patterns
- `async/await` (asyncio or trio)
- Context manager: `async with Client(...)`
- Use type hints

#### Example
```python
async def main():
    config = ClientConfig(
        server_url="http://localhost:8080",
        target_tcp="127.0.0.1:8081",
        name="demo"
    )

    async with Client(config) as client:
        await client.start()
        print(f"Peer ID: {client.peer_id()}")
        await asyncio.Event().wait()

asyncio.run(main())
```

### Rust

#### Dependencies
```toml
[dependencies]
libp2p = { version = "0.53", features = ["tcp", "quic", "noise", "yamux", "gossipsub"] }
tokio = { version = "1.0", features = ["full"] }
reqwest = { version = "0.11", features = ["json"] }
```

#### Core Types
```rust
pub struct ClientConfig {
    pub server_url: String,
    pub target_tcp: String,
    pub name: String,
    pub protocol: Option<String>,
    pub topic: Option<String>,
    pub bootstrap: Option<Vec<String>>,
}

pub struct Client {
    config: ClientConfig,
    swarm: Swarm<Behaviour>,
}

impl Client {
    pub async fn new(config: ClientConfig) -> Result<Self, Error>;
    pub async fn start(&mut self) -> Result<(), Error>;
    pub async fn close(self) -> Result<(), Error>;
    pub fn peer_id(&self) -> PeerId;
}
```

#### Patterns
- Swarm-based architecture
- `Result<T, E>` error handling
- Builder pattern (optional)

#### Example
```rust
#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let config = ClientConfig {
        server_url: "http://localhost:8080".to_string(),
        target_tcp: "127.0.0.1:8081".to_string(),
        name: "demo".to_string(),
        ..Default::default()
    };

    let mut client = Client::new(config).await?;
    client.start().await?;

    println!("Peer ID: {}", client.peer_id());

    tokio::signal::ctrl_c().await?;
    client.close().await?;

    Ok(())
}
```

---

## Testing

### Test Environment Setup

#### 1. Start RelayDNS Server
```bash
# Using Docker Compose
docker compose up -d

# Verify server
curl http://localhost:8080/health
```

#### 2. Start Local Service
```bash
# Simple HTTP server
python -m http.server 8081

# Or
echo "Hello RelayDNS" > index.html
python -m http.server 8081
```

### Unit Tests

#### Config Validation
```
TEST: Error on missing required field
  config = ClientConfig{Name: "test"}
  client = NewClient(config)
  EXPECT: error "ServerURL is required"

TEST: Success with valid config
  config = ClientConfig{
    ServerURL: "http://localhost:8080",
    TargetTCP: "127.0.0.1:8081",
    Name: "test"
  }
  client = NewClient(config)
  EXPECT: no error
```

#### Health Endpoint
```
TEST: Parse valid response
  response = '{"status":"healthy","peers":["addr1"],"version":"1.0"}'
  peers = parseHealth(response)
  EXPECT: peers = ["addr1"]

TEST: Error when server unavailable
  serverURL = "http://nonexistent:9999"
  EXPECT: error "connection refused"
```

### Integration Tests

#### 1. Basic Connection Test
```
GIVEN: RelayDNS server is running
  AND: Local HTTP server is running on port 8081

WHEN: SDK client starts
  config = ClientConfig{
    ServerURL: "http://localhost:8080",
    TargetTCP: "127.0.0.1:8081",
    Name: "test-client"
  }
  client.Start()

THEN:
  - Health endpoint call succeeds
  - Bootstrap peers connected successfully
  - GossipSub topic joined successfully
  - "test-client" appears in server Admin UI
```

#### 2. Proxy Test
```
GIVEN: Client is started
  AND: Local server responds with "Hello"

WHEN: Click peer in server UI → Click "Open"

THEN:
  - Page opens in new tab
  - "Hello" text is displayed
  - Stream is proxied to local port 8081
```

#### 3. Reconnection Test
```
GIVEN: Client is running

WHEN: Server restarts
  docker compose restart

THEN:
  - Client attempts to reconnect
  - Backoff logs are printed
  - Reconnection succeeds after server restart
  - Reappears in UI
```

#### 4. Error Handling Test
```
TEST: When local service is down
GIVEN: Client is running
WHEN: Local server is stopped (kill python)
  AND: Connection attempt from server
THEN:
  - Stream closes immediately
  - Error log: "connection refused"
  - Client continues running
```

### Test Checklist

Verify the following in a real environment:

- [ ] Health endpoint call succeeds
- [ ] Connected to bootstrap peers
- [ ] Joined GossipSub topic
- [ ] Published own advertisement
- [ ] Received other peer advertisements
- [ ] Displayed in server Admin UI
- [ ] Proxy connection works
- [ ] Handles local service down gracefully
- [ ] Graceful shutdown with Ctrl+C
- [ ] Normal operation after restart

---

## Example Code

### Minimal Example (Go)

```go
package main

import (
    "context"
    "log"

    sdk "github.com/your-org/relaydns-sdk-go"
)

func main() {
    ctx := context.Background()

    // Create client
    client, err := sdk.NewClient(ctx, sdk.ClientConfig{
        ServerURL: "http://localhost:8080",
        TargetTCP: "127.0.0.1:8081",
        Name:      "my-service",
    })
    if err != nil {
        log.Fatalf("Failed to create client: %v", err)
    }
    defer client.Close()

    // Start
    if err := client.Start(ctx); err != nil {
        log.Fatalf("Failed to start client: %v", err)
    }

    log.Printf("✓ Started. Peer ID: %s", client.PeerID())

    // Keep running
    select {}
}
```

### Custom Bootstrap (TypeScript)

```typescript
import { Client, ClientConfig } from 'relaydns-sdk';

const config: ClientConfig = {
  serverURL: 'http://localhost:8080',
  targetTCP: '127.0.0.1:8081',
  name: 'custom-service',
  bootstrap: [
    '/ip4/1.2.3.4/tcp/4001/p2p/QmPeerID1',
    '/ip4/5.6.7.8/tcp/4001/p2p/QmPeerID2'
  ]
};

const client = new Client(config);

try {
  await client.start();
  console.log(`✓ Started. Peer ID: ${client.getPeerID()}`);

  await new Promise(() => {}); // Keep running
} catch (error) {
  console.error('Failed:', error);
  process.exit(1);
}
```

### Error Handling (Python)

```python
import asyncio
import logging
from relaydns_sdk import Client, ClientConfig

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

async def main():
    config = ClientConfig(
        server_url="http://localhost:8080",
        target_tcp="127.0.0.1:8081",
        name="python-service"
    )

    client = Client(config)

    try:
        await client.start()
        logger.info(f"✓ Started. Peer ID: {client.peer_id()}")

        # Keep running
        await asyncio.Event().wait()

    except ConnectionError as e:
        logger.error(f"✗ Connection failed: {e}")
        return 1

    except Exception as e:
        logger.error(f"✗ Unexpected error: {e}")
        return 1

    finally:
        await client.close()
        logger.info("✓ Closed gracefully")

    return 0

if __name__ == "__main__":
    exit_code = asyncio.run(main())
    exit(exit_code)
```

### Production Example (Rust)

```rust
use relaydns_sdk::{Client, ClientConfig};
use tokio;
use tracing::{info, error};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Initialize logging
    tracing_subscriber::fmt::init();

    // Configuration
    let config = ClientConfig {
        server_url: "http://localhost:8080".to_string(),
        target_tcp: "127.0.0.1:8081".to_string(),
        name: "rust-service".to_string(),
        ..Default::default()
    };

    // Create client
    let mut client = Client::new(config).await.map_err(|e| {
        error!("Failed to create client: {}", e);
        e
    })?;

    // Start
    client.start().await.map_err(|e| {
        error!("Failed to start client: {}", e);
        e
    })?;

    info!("✓ Started. Peer ID: {}", client.peer_id());

    // Wait for shutdown signal
    tokio::select! {
        _ = tokio::signal::ctrl_c() => {
            info!("Received Ctrl+C, shutting down...");
        }
    }

    // Graceful shutdown
    client.close().await?;
    info!("✓ Closed gracefully");

    Ok(())
}
```

---

## Appendix

### A. Multiaddr Format

Address format used in RelayDNS:

```
/ip4/1.2.3.4/tcp/4001/p2p/QmPeerID...
│    │       │   │    │   └─ Peer ID (required)
│    │       │   │    └───── Transport protocol
│    │       │   └────────── Port
│    │       └────────────── Transport
│    └────────────────────── IP address
└─────────────────────────── IP version
```

Other examples:
- `/ip4/127.0.0.1/tcp/4001/p2p/Qm...` - Local TCP
- `/ip6/::1/tcp/4001/p2p/Qm...` - IPv6
- `/ip4/1.2.3.4/udp/4001/quic-v1/p2p/Qm...` - QUIC

### B. Debugging Tips

#### "Cannot connect to bootstrap"
```bash
# Check server status
curl http://localhost:8080/health

# Check port
nc -zv localhost 4001

# Check firewall
sudo ufw status
```

#### "Stream handler not called"
```bash
# Verify protocol match
# Client: "/relaydns/1.0.0"
# Server: Same protocol

# Check GossipSub messages in logs
# Should see "Published advertisement" messages
```

#### "Local service connection refused"
```bash
# Check local service
nc -zv 127.0.0.1 8081

# Or
curl http://127.0.0.1:8081
```

### C. Performance Considerations

#### Buffer Size
```
Recommended: 32KB read/write buffer
- Larger: Throughput ↑, Memory ↑
- Smaller: Latency ↓, Memory ↓
```

#### GossipSub Tuning
```
Low latency:
- heartbeat: 500ms
- fanout: 8

Bandwidth saving:
- heartbeat: 2s
- fanout: 4
```

#### Connection Pooling
```
Problem: New TCP connection per stream
Solution: Maintain TCP connection pool
- Pool size: 10-20
- Idle timeout: 60 seconds
```

### D. Security

#### Encryption
- All streams: Encrypted with Noise protocol
- Forward secrecy guaranteed

#### Authentication
- Automatic authentication with Peer ID
- Public key-based

#### Authorization
- Current: All peers can connect
- Future: ACL (Peer ID-based)

---

## References

### Official Documentation
- libp2p: https://docs.libp2p.io/
- GossipSub: https://github.com/libp2p/specs/tree/master/pubsub/gossipsub
- RelayDNS: https://github.com/gosuda/relaydns

### Library Documentation
- Go libp2p: https://pkg.go.dev/github.com/libp2p/go-libp2p
- TypeScript libp2p: https://github.com/libp2p/js-libp2p
- Python libp2p: https://github.com/libp2p/py-libp2p
- Rust libp2p: https://docs.rs/libp2p

---

**Document Version**: 1.0.0
**Last Updated**: 2025-10-23
**Feedback**: https://github.com/gosuda/relaydns/issues
