# Frontend AGENTS.md

High-signal constraints for the relay-server frontend. Only items expensive to rediscover.

## Frontend-Backend Contracts (Manually Synced)

1. **SSR data shape is a 4-way contract.**
   Go lease contracts (`../types/lease.go`) + portal snapshot producer (`../portal/lease.go`) + relay-server frontend filtering/injection (`../cmd/relay-server/frontend.go`) -> TS `ServerData` (`src/hooks/useSSRData.ts`).
   - Why: no shared schema or codegen. Field drift silently breaks SSR hydration. The script tag ID `__SSR_DATA__` is hardcoded in all three locations.

2. **API path constants require dual maintenance.**
   Go definitions live in `types/paths.go`; TS duplicates live in `src/lib/apiPaths.ts`.
   - Why: no codegen. A path mismatch produces same-origin 404s.
   - Current Go runtime serves `/admin` and `/admin/snapshot` for admin reads; `/admin/leases/*`, `/admin/settings/approval-mode`, and related auth/IP routes are action surfaces. Extra TS admin paths still need backend work or they will 404.

3. **API envelope shape must match across Go and TS.**
   All JSON control-plane responses use `{ ok, data?, error?: { code, message } }`.
   Go shape is `types.APIEnvelope` in `types/api.go`; Go writers live in `portal/api.go`; TS parser lives in `src/lib/apiClient.ts`.
   - Why: backend responses that skip the envelope surface as `invalid_envelope` in the frontend.

4. **Build output renames `index.html` to `portal.html`.**
   Vite plugin `rename-index` (`vite.config.ts`) performs this post-build. Go backend serves `portal.html`, not `index.html`. The rename is skipped when `VITEST` is set.
   - Why: any tooling or script assuming `index.html` post-build will fail.

5. **HTML metadata placeholders must match between HTML and Go.**
   `index.html` (renamed to `portal.html`) contains `[%OG_TITLE%]`, `[%OG_DESCRIPTION%]`, `[%OG_IMAGE_URL%]`, `[%RELEASE_VERSION%]`. Server-side substitution happens in `cmd/relay-server/frontend.go`.
   - Why: renaming a placeholder in one place without the other leaves raw placeholder strings in production HTML.

6. **Admin state reads are aggregated through `/admin/snapshot`.**
   `src/hooks/useAdmin.ts` expects one payload carrying `leases` and `approval_mode`.
   - Why: splitting those reads across multiple endpoints reintroduces extra request coordination and drift in the admin bootstrap path.

## Frontend Conventions

1. **Do not use `useCallback` in new code.**
   React Compiler (`babel-plugin-react-compiler`, enabled in `vite.config.ts`) handles memoization automatically.
   - Why: manual `useCallback` is redundant with the compiler and adds noise.
