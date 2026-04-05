# Reverse Siamese Onion – Phase 1

1. **Current forwarding path**
   - `portal/ols/engine.go`: `RouteConn` dials the next relay and writes the JSON `RouteContext`; `ServeInterRelayConn` reads it and decides whether to proxy again.
   - `portal/policy/ols.go`: `GetTargetNodeID` computes the deterministic Reverse Siamese target using `RouteContext` metadata.

2. **Insertion points**
   - Replace `writeRouteContext`/`readRouteContext` in `portal/ols/engine.go` with onion cell encode/decode that runs before `bridge`.
   - Slim `RouteContext` in `portal/policy/ols.go` to track only hop counters so no extra topology data leaks.
   - Embed the new cell framing in the transport boundary (stream accept loop stays untouched; only metadata hop header is sent before payload).

3. **New types**
   - `Cell`: fixed-size `[OnionCellSize]byte` buffer written once per hop.
   - `OnionLayer`: holds `ForwardingMeta` plus a per-hop `NextHopHint` (hash of the recipient) protected by a cipher.
   - `ForwardingMeta`: `{Hop uint8, MaxHops uint8, Nonce [12]byte}` minimal state used for TTL-style checks.
   - `HopCipher` interface with `Seal`/`Open` so we can plug AEAD later; phase 1 ships with a noop implementation that copies bytes.

4. **Route disclosure reduction**
   - No visited list or origin ID leaves the ingress; only the opaque `ForwardingMeta` travels, so downstream relays cannot reconstruct prior hops.
   - Each hop receives a cell whose `NextHopHint` matches only itself; if it forwards, it rewrites the header with a fresh hint for the next relay, so the old metadata is never forwarded further.
   - The payload is untouched (still one end-to-end TLS stream), so per-hop work decrypts only the tiny fixed header.

5. **Stub vs implemented**
   - Implemented: fixed-size cell framing, encode/decode helpers, header verification, TTL enforcement, hash-based hints, and updated docs.
   - Stubbed: real AEAD (current cipher passthrough), batching hooks (left as TODO comment in the helper), and advanced key exchange; these will plug into the `HopCipher` interface later without changing call sites.
