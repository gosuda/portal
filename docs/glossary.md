Portal is unlike traditional server–client architectures, both the App and the Client act as clients within the Portal network, which can easily cause terminology confusion. In addition, names such as "Portal" (relay server) and legacy RD-related function names often overlap. This glossary clarifies those terms.

### Portal (Relay Server)

The Portal is the relay hub provided by this project.
It acts as a central mediator, while other components either advertise themselves to the Portal or discover their counterparts through it.

* Participates in the RDSEC handshake using its own Ed25519 credential.
* Manages multiplexed streams with yamux and handles lease registration/deletion/query, connection forwarding, and traffic control (BPS, concurrent streams).
* Does not decrypt payloads and only provides routing. App and Client maintain end-to-end encryption (SecureConnection) even when traversing the Portal.

### App (Service Publisher)

An App is a service-providing entity that publishes services to the Portal.
In practice, server-side code that uses the Portal SDK to communicate with the Portal is considered an App.

* Holds at least one credential, connects to the Portal with it, and registers a Lease that represents the App.
* Maintains a yamux session with the Portal and proxies incoming connection requests to a local service (TCP/HTTP, etc.).
* Updates metadata, ALPN, and other advertising attributes via the Portal Frontend or API to improve discoverability.

### Client (Service Consumer)

A Client is the end-user entity that attempts to access services published by an App through the Portal.

* Currently, this is primarily the browser (Portal WebClient + Service Worker).
* A Client requests a connection to the Portal by specifying a Lease ID or name. The Portal matches this request to the App owning that Lease, then performs an RDSEC handshake to establish a SecureConnection and exchange data.
* A Client must also possess a credential, which grants connection-request permission and enables mutual authentication with the App (E2EE).

### Lease (Advertising Slot)

A Lease is the advertisement unit an App publishes to the Portal.

* Consists of an identity (App credential), expiration time, display name, allowed ALPN list, and optional metadata (JSON).
* Managed by the Portal’s LeaseManager, which handles registration, renewal, expiration, deletion, and enforces name conflict rules, banned IDs, and TTL/BPS policies.
* A Client requests a connection from the Portal using a Lease ID (= credential ID) or name, and the Portal uses this information to locate the corresponding App’s RelayClient.

A Lease is the fundamental routing unit: **one Lease equals one public endpoint.**
