# AGENTS.md

Keep this file short and behavioral.
Architecture, product behavior, and design rationale belong in `docs/architecture.md` and `docs/adr/README.md`.

## Principles

- Minimize concepts, duplication, and ceremony.
- One real owner per contract. No mirroring, no wrappers unless they remove real coupling.
- Local simplicity over speculative abstraction. Add indirection only when it removes real coupling or protects a real boundary.
- When caller and callee are both local with no real boundary, change both directly.
- Remove dead code, fields, config, and stale state while touching nearby code.

## Go Mandates

- Validate on construction. `NewX` functions reject invalid state; callers never receive a half-built value.
- Fail fast with clear errors. Wrap with context; surface the root cause.
- Zero external dependencies unless the alternative is re-implementing a non-trivial, correctness-critical algorithm. Justify in the commit message.
- Interfaces express behavior, not taxonomy. One or two methods. If an interface has no consumer, delete it.

## Testing

- A test exists to catch real bugs. If deleting the test would not let a bug reach production, delete the test.
- Test contracts and boundaries: protocol compliance, error semantics, security invariants, integration across real I/O.
- Do not test configuration shapes, constructor output fields, or struct assembly — the type system and constructors already guarantee those.
- Do not test that a function returns exactly what you passed in.

## Verification

- CI commands: `make vet`, `make lint`, `make test`, `make vuln`.
- `make tidy` is local maintenance, not a CI requirement.
- Run targeted tests after contract-touching or risky edits. Ask before running the full suite.
