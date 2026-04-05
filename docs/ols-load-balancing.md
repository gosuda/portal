# OLS-based L7 Load Balancing Architecture

This document describes the design and implementation of the L7 load balancing system using a Reverse Siamese paired-square topology.

## 1. Design Principles

The portal nodes (RelayServers) form an $n \times n$ grid where $n = \lfloor\sqrt{N}\rfloor$ and $N$ is the total number of nodes. This grid provides a structured yet flexible way to distribute L7 traffic (HTTP/WebSocket) across a cluster of master nodes.

### Core Features:
- **Policy-Only OLS**: The `OLSManager` (in `portal/policy`) is strictly responsible for the load balancing algorithm. It does not handle networking or data transfer.
- **Dynamic 90-degree Rotation**: If traffic becomes unbalanced (detected via load variance across rows vs. columns), the grid's routing logic rotates by 90 degrees to redistribute the load.

---

## 2. Mapping Foundation: Reverse Siamese Pair

Routing now relies on a pair of Siamese squares:

1.  **Forward Square (A)**: Generated via the standard Siamese walk (start at the top-middle cell, move up-right, fall back one cell down when occupied). The square stores visit order $0 \ldots n^2-1$.
2.  **Reverse Square (B)**: Derived from A through a horizontal mirror followed by a complement transform $B_{i,j} = (n^2-1) - A_{i,n-1-j}$.
3.  **Row/Column Projection**: The manager converts $B$ into row indices ($\lfloor B_{i,j} / n \rfloor$) and $A$ into column indices ($A_{i,j} \bmod n$). This keeps every entry within `[0, n)` while preserving the coupled "forward/reverse" behavior.

This Reverse Siamese coupling replaces the previous recursive MOLS builder, reducing indirection while still guaranteeing deterministic, decorrelated coordinates for every `(clientID, leaseID)` pair.

---

## 3. Structure and Flow

```mermaid
graph TD
    Client[Client Request] --> NodeA[Relay Node A]
    
    subgraph OLS_Grid [n x n OLS Grid Topology]
        NodeA
        NodeB[Relay Node B]
        NodeC[Relay Node C]
        NodeD[Relay Node D]
    end

    NodeA -- "policy.OLSManager.GetTargetNodeID(ClientID, LeaseID)" --> TargetID{Is local?}
    TargetID -- Yes --> LocalDial[Dial Local Tunnel]
    TargetID -- No --> Proxy[Proxy via inter-relay transport]
    
    Proxy -- "inter-relay transport" --> NodeB
    NodeB -- "BridgeConns" --> Tunnel[Target Tunnel Client]

    subgraph LoadBalancer [Dynamic Stabilization]
        direction LR
        LoadMonitor[Load Variance Monitor] --> |"Row Var > Col Var * 1.5"| Rotation[90 deg Rotation]
        Rotation --> UpdateRoutes[Update OLS Mapping]
    end
```

### Routing Logic
For a given `ClientID` (source) and `LeaseID` (destination):
1.  Calculate grid coordinates $(i, j)$ using hashes.
2.  Lookup $L_1(i, j)$ (row indices from the reverse square) and $L_2(i, j)$ (column indices from the forward square) to find the target cell in the $n \times n$ grid.
3.  Apply current linear transformation (rotation) to the coordinates.
4.  Route the request to the node ID mapped to those coordinates.

---

## 4. Master-to-Master Tunneling

The inter-relay transport layer is pluggable. The previous WireGuard overlay
has been removed, so relays will serve traffic locally until a new transport is
introduced. When a transport is available, the same OLS routing logic forwards
connections over that data plane to the optimal node.

### Onion Cell Metadata (Phase 1)

- Each relay hop now receives a fixed-size "cell" that contains only:
  - hop counter + TTL (`ForwardingMeta`)
  - a hashed hint of **the next relay only**
- Cells are encoded via the new onion helper in `portal/transport/onioncell.go`; payload streams are untouched.
- SDK users do **not** need extra flags: `portal expose` automatically emits the onion header before the tunnel payload begins.
- Future phases can swap the placeholder cipher for AEAD without changing the SDK surface.
