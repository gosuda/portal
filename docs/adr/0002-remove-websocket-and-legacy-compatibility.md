# ADR 0002: Remove WebSocket and Legacy Compatibility Paths

- Status: `Accepted`
- Date: `2026-03-03`
- Owners: `Portal maintainers`

## Context

Portal transport and registration flows previously carried compatibility behavior for websocket-era clients. This increased code-path count, made failure handling inconsistent, and obscured the canonical tunnel behavior.

## Decision

- Treat raw TCP reverse-connect as the only supported data-plane transport.
- Remove websocket compatibility expectations from architecture guidance and operational assumptions.
- Keep SDK registration APIs aligned with current raw transport behavior only.

## Consequences

### Benefits

- Fewer protocol paths to secure, test, and debug.
- Clearer invariants for lease registration, route updates, and reverse-hub acquisition.
- Lower maintenance cost by removing compatibility-only logic from design decisions.

### Trade-offs

- Older websocket-based clients are intentionally unsupported.
- Migration burden moves to client operators that still depend on websocket semantics.

### Risks and Mitigations

- Risk: clients attempt deprecated websocket workflows and fail unexpectedly.
  Mitigation: keep docs explicit that raw TCP is the single supported transport and reject unsupported paths clearly.
- Risk: hidden compatibility assumptions in future changes.
  Mitigation: use this ADR as a gate in design/review to avoid reintroducing websocket dependencies.

## Alternatives Considered

- Keep dual-stack (raw TCP + websocket): rejected due to complexity and security surface growth.
- Keep websocket as fallback only: rejected because fallback behavior still multiplies test and incident scenarios.
