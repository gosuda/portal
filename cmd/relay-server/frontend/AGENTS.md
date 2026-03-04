# Frontend AGENTS.md

High-signal constraints for the relay-server frontend. Only items expensive to rediscover.

## Frontend-Backend Contracts (Manually Synced)

1. **SSR data shape is a 3-way contract.**
   Go `leaseRow` (`cmd/relay-server/utils.go`) ↔ TS `ServerData` (`src/hooks/useSSRData.ts`) ↔ `<script id="__SSR_DATA__">` injection (`cmd/relay-server/frontend.go`).
   - Why: no shared schema or codegen. Field drift silently breaks SSR hydration. The script tag ID `__SSR_DATA__` is hardcoded in all three locations.

2. **API path constants require dual maintenance.**
   Go definitions in `types/api.go`, TS duplicates in `src/lib/apiPaths.ts`.
   - Why: no codegen. A path mismatch produces silent 404s on same-origin requests.

3. **API envelope shape must match across Go and TS.**
   All responses use `{ ok, data?, error?: { code, message } }`. Go helpers (`utils.go`) and TS parser (`src/lib/apiClient.ts`) must agree. TS treats a missing boolean `ok` field as `invalid_envelope`.
   - Why: backend responses that skip the envelope (e.g., raw middleware errors) throw `invalid_envelope` instead of displaying the error.

4. **Lease ID encoding uses base64url without padding.**
   Frontend encodes via `btoa` + character replacement (`src/lib/apiPaths.ts`). Backend two-pass decodes: `base64.URLEncoding` then `base64.RawURLEncoding` (`cmd/relay-server/utils.go`).
   - Why: standard base64 in either direction → 400 errors. `btoa` is browser-only — not portable to Node/SSR without polyfill.

5. **Build output renames `index.html` to `portal.html`.**
   Vite plugin `rename-index` (`vite.config.ts`) performs this post-build. Go backend serves `portal.html`, not `index.html`. The rename is skipped when `VITEST` env is set.
   - Why: any tooling or script assuming `index.html` post-build will fail.

6. **OG metadata placeholders must match between HTML and Go.**
   `index.html` (becomes `portal.html`) contains `[%OG_TITLE%]`, `[%OG_DESCRIPTION%]`, `[%OG_IMAGE_URL%]`. Server-side substitution happens in `cmd/relay-server/frontend.go`.
   - Why: renaming a placeholder in one place without the other → raw uninjected strings in responses.

## Frontend Conventions

1. **Do not use `useCallback` in new code.**
   React Compiler (`babel-plugin-react-compiler`, enabled in `vite.config.ts`) handles memoization automatically.
   - Why: manual `useCallback` is redundant with the compiler and adds noise. Existing usage in `useAdmin.ts` and `ServerListView.tsx` is legacy — remove when touching those files.
