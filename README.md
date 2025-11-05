# PORTAL — Public Open Relay To Access Localhost

<p align="center">
  <img src="/portal.jpg" alt="Portal logo" width="540" />
</p>

Portal is a secure, encrypted relay service that enables end-to-end encrypted communication between clients through a central relay server. It provides mutual authentication, forward secrecy, and secure connection management with cryptographic identity verification.

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Architecture](#architecture)
- [Security](#security)
- [Installation](#installation)
- [Usage](#usage)
- [API Reference](#api-reference)
- [Protocol Specification](#protocol-specification)
- [Development](#development)
- [Contributing](#contributing)
- [License](#license)

## Overview

Portal implements a secure relay protocol that allows clients to register leases and establish encrypted connections through a central server. The system uses modern cryptographic primitives to ensure:

- **End-to-end encryption**: All communication is encrypted using ChaCha20-Poly1305 AEAD
- **Mutual authentication**: Ed25519 signatures verify client identities
- **Forward secrecy**: Ephemeral X25519 key exchange per connection
- **Secure relay**: The relay server cannot decrypt client communications

## Features

- 🔐 **End-to-End Encryption**: Client-to-client communication is fully encrypted
- 🔑 **Cryptographic Identity**: Ed25519-based identity system with verifiable signatures
- 🔄 **Connection Relay**: Secure connection forwarding through central server
- ⏰ **Lease Management**: Time-based lease system with automatic cleanup
- 🌐 **Protocol Support**: Application-Layer Protocol Negotiation (ALPN)
- 🚀 **High Performance**: Multiplexed connections using yamux
- 🐳 **Docker Support**: Containerized deployment ready
- 🌍 **Browser E2EE Proxy**: WASM-based Service Worker for automatic browser encryption
- 📱 **Multi-Platform**: Go SDK for servers, WASM SDK for browsers

## Architecture

### System Architecture

```mermaid
graph TB
    subgraph "Client A"
        CA[Client A]
        CA --> CA_ID[Identity: Ed25519]
        CA --> CA_LEASE[Lease Manager]
    end

    subgraph "Client B"
        CB[Client B]
        CB --> CB_ID[Identity: Ed25519]
        CB --> CB_LEASE[Lease Manager]
    end

    subgraph "Relay Server"
        RS[Relay Server]
        RS --> RS_ID[Server Identity]
        RS --> LM[Lease Manager]
        RS --> CM[Connection Manager]
        RS --> FH[Forwarding Handler]
    end

    CA -.->|1. Register Lease| RS
    CB -.->|2. Register Lease| RS
    CB -.->|3. Request Connection| RS
    RS -.->|4. Forward Request| CA
    CA -.->|5. Accept Connection| RS
    RS -.->|6. Establish E2EE| CB

    CA <-->|7. Encrypted Data| CB
```

### Component Architecture

```mermaid
graph LR
    subgraph "Client Components"
        C[RelayClient]
        C --> H[Handshaker]
        C --> LM[LeaseManager]
        C --> SC[SecureConnection]
    end

    subgraph "Server Components"
        S[RelayServer]
        S --> LH[LeaseHandler]
        S --> CH[ConnectionHandler]
        S --> FH[ForwardingHandler]
        S --> LM2[LeaseManager]
    end

    subgraph "Crypto Operations"
        CO[CryptoOps]
        CO --> CRED[Credential]
        CO --> SIG[Signature]
        CO --> E2EE[End-to-End Encryption]
    end

    C <-->|Protocol Messages| S
    H --> CO
    SC --> CO
    LH --> LM2
    CH --> FH
```

### Connection Flow

```mermaid
sequenceDiagram
    participant C1 as Client 1
    participant RS as Relay Server
    participant C2 as Client 2

    Note over C1,C2: Lease Registration Phase
    C1->>RS: Register Lease (Identity, ALPN)
    RS->>C1: Lease Confirmation

    C2->>RS: Register Lease (Identity, ALPN)
    RS->>C2: Lease Confirmation

    Note over C1,C2: Connection Establishment Phase
    C2->>RS: Request Connection (to Client 1)
    RS->>C1: Forward Connection Request
    C1->>RS: Accept Connection
    RS->>C2: Connection Accepted

    Note over C1,C2: Secure Handshake Phase
    C2->>C1: X25519 Handshake (via relay)
    C1->>C2: X25519 Response (via relay)

    Note over C1,C2: End-to-End Encrypted Communication
    C2->>C1: Encrypted Data (ChaCha20-Poly1305)
    C1->>C2: Encrypted Data (ChaCha20-Poly1305)
```

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
