# AGENTS.md

Keep this file short and behavioral.
Architecture, product behavior, and design rationale belong in `docs/architecture.md` and `docs/adr/README.md`.

## Development Principles

- Minimizing concepts, duplication, and ceremony.
- Prefer a single stable contract with one real owner.
- Prefer local simplicity over premature or speculative abstraction.
- Add indirection only when it removes real coupling or protects a real boundary.
- Tests should protect stable contracts and invariants, not drive the spec.

## Project Principles

- No wrapper functions or helpers without demonstrated value.
- Prefer fewer concepts and direct code over extra layers, facades, and indirection.
- Prefer flattening and merging nearby responsibilities over splitting files or packages by default.
- Remove dead fields, dead methods, dead config, and stale state while touching nearby code.
- Avoid duplicate normalization, copying, and caching unless aliasing or trust boundaries require it.
- Keep stable shared contracts, shared constants, and public paths in `types/`, not runtime state, package-local logic, or generic helpers.
- Keep shared stateless transforms in `utils/`; keep stateful and domain-shaped logic with the real owner.
- Resolve complexity in the lowest coherent owner and expose only the minimum necessary surface upward.
- Shared runtime logic should live in one real owner and be reused, not mirrored by parallel helpers.
- Prefer not preserving backward compatibility by default, but ask when breaking it may cause real downstream problems.

## Verification

- CI commands: `make vet`, `make lint`, `make test`, `make vuln`.
- `make tidy` is local maintenance, not a CI requirement.
- Run tests only when explicitly requested.
- If verification seems necessary, ask before running it.
