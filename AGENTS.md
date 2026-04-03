# AGENTS.md

Keep this file short and behavioral.
Architecture, product behavior, and design rationale belong in `docs/architecture.md`.

## Principles

These are mandates, not suggestions.

- Minimize concepts, duplication, and ceremony.
- One real owner per contract. No mirroring, no wrappers unless they remove real coupling.
- Local simplicity over speculative abstraction. Add indirection only when it removes real coupling or protects a real boundary.
- When caller and callee are both local with no real boundary, change both directly.
- Remove dead code, fields, config, and stale state while touching nearby code.
- Reject invalid state at construction. `NewX` functions never return a half-built value; callers never check after the fact.
- Fail fast. Wrap errors with context; surface the root cause.
- Zero external dependencies unless the alternative is re-implementing a non-trivial, correctness-critical algorithm. Justify in the commit message.
- Interfaces express behavior, not taxonomy. One or two methods. If an interface has no consumer, delete it.

## Testing

- A test exists to catch real bugs. If deleting the test would not let a bug reach production, delete the test.
- Test contracts and boundaries: protocol compliance, error semantics, security invariants, integration across real I/O.
- Do not test configuration shapes, constructor output fields, or struct assembly — the type system and constructors already guarantee those.
