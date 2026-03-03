# Architecture Decision Records (ADR)

This directory is the source of truth for major Portal architecture decisions.

## Status Values

- `Accepted`: active and expected in current code paths
- `Superseded`: replaced by a newer ADR
- `Deprecated`: still present but planned for removal
- `Proposed`: under review, not yet implemented

## ADR Index

- [0001 - Raw TCP Reverse Connect and ACME TLS for Portal Root](./0001-raw-tcp-reverse-connect-and-autocert-tls.md) (`Accepted`)
- [0002 - Remove WebSocket and Legacy Compatibility Paths](./0002-remove-websocket-and-legacy-compatibility.md) (`Accepted`)
- [0003 - Security and Anti-Abuse Hardening](./0003-security-and-anti-abuse-hardening.md) (`Accepted`)

## Authoring Notes

- Use [template.md](./template.md) for new ADRs.
- Keep each ADR focused on one decision and its consequences.
- Update this index whenever adding, superseding, or deprecating an ADR.
