# Frontend AGENTS.md

High-signal constraints for the relay-server frontend. Only items expensive to rediscover.

## Frontend-Backend Contracts (Manually Synced)

1. **SSR data shape is a 4-way contract.**
   Go lease contracts (`../types/lease.go`) + portal snapshot producer (`../portal/lease.go`) + relay-server frontend filtering/injection (`../cmd/relay-server/frontend.go`) -> TS `ServerData` (`src/hooks/useSSRData.ts`).
   - Why: no shared schema or codegen. Field drift silently breaks SSR hydration. The script tag ID `__SSR_DATA__` is hardcoded in all three locations.

2. **API path constants require dual maintenance.**
   Go definitions live in `types/paths.go`; TS duplicates live in `src/lib/apiPaths.ts`.
   - Why: no codegen. A path mismatch produces same-origin 404s.

3. **API envelope shape must match across Go and TS.**
   All JSON control-plane responses use `{ ok, data?, error?: { code, message } }`.
   Go shape is `types.APIEnvelope` in `types/api.go`; Go writers live in `portal/api.go`; TS parser lives in `src/lib/apiClient.ts`.
   - Why: backend responses that skip the envelope surface as `invalid_envelope` in the frontend.

4. **Build output renames `index.html` to `portal.html`.**
   Vite plugin `rename-index` (`vite.config.ts`) performs this post-build. Go backend serves `portal.html`, not `index.html`. The rename is skipped when `VITEST` is set.
   - Why: any tooling or script assuming `index.html` post-build will fail.

5. **HTML metadata placeholders must match between HTML and Go.**
   `index.html` (renamed to `portal.html`) contains `[%..%]` placeholders substituted server-side in `cmd/relay-server/frontend.go`.
   - Why: renaming a placeholder in one place without the other leaves raw placeholder strings in production HTML.

6. **Admin state reads are aggregated through `/admin/snapshot`.**
   `src/hooks/useAdmin.ts` expects one payload carrying `leases` and `approval_mode`.
   - Why: splitting those reads across multiple endpoints reintroduces extra request coordination and drift in the admin bootstrap path.

7. **Lease/AdminLease JSON casing is a mixed implicit/explicit contract.**
   `Lease` (`../types/identity.go`): `Name` has `json:"name"` tag, but `ExpiresAt`, `FirstSeenAt`, `LastSeenAt`, `Hostname`, `Ready` have NO tags — Go defaults to PascalCase. `AdminLease`: `IdentityKey` and `Address` have snake_case json tags, but `BPS`, `ClientIP`, `ReportedIP`, `IsApproved`, `IsBanned`, `IsDenied`, `IsIPBanned` have NO tags — also PascalCase. TS `PublicLeaseData` and `AdminLeaseData` (`src/hooks/useSSRData.ts`) consume a subset of these fields and match this mixed casing.
   - Why: adding a `json:"..."` tag to any currently untagged field silently changes the wire name and breaks the TS consumer. Go also sends `UDPEnabled`, `TCPEnabled`, `TCPAddr` on `Lease` which the frontend does not consume — these are not part of the frontend contract.

8. **Admin lease paths use base64-url encoding with URI-component escaping.**
   TS `encodePathPart()` (`src/lib/apiPaths.ts`): `btoa(value)` → replace `+/=` with `-/_/""` → `encodeURIComponent()`. Go decodes via `utils.DecodeBase64URLString()`.
   - Why: two-layer codec. Changing either side silently produces 400s on admin lease actions.

9. **`Metadata` is typed `unknown` in TS but has a concrete Go struct.**
   Go `LeaseMetadata` (`../types/identity.go`): `description`, `owner`, `thumbnail`, `tags`, `hide` — all with json tags. TS declares `Metadata: unknown` in `PublicLeaseData`, then runtime-parses in `src/lib/metadata.ts`.
   - Why: adding or renaming a Go metadata field silently drops data in the frontend. No compile-time contract exists.

10. **ApprovalMode is a closed two-value enum: `"auto"` | `"manual"`.**
    TS `normalizeApprovalMode()` (`src/hooks/useAdmin.ts`) collapses any non-`"manual"` value to `"auto"`.
    - Why: adding a third mode in Go without updating the TS normalizer silently collapses it to "auto".

## Frontend Conventions

1. **Do not use `useCallback` in new code.**
   React Compiler (`babel-plugin-react-compiler`, enabled in `vite.config.ts`) handles memoization automatically.
   - Why: manual `useCallback` is redundant with the compiler and adds noise.

2. **Feature state lives in page-level hooks and is prop-drilled. No global state library.**
   `useServerList`, `useAdmin`, `useAuth` own feature state at the page level. Theme is the exception — it uses a dedicated `ThemeProvider` context (`src/components/ThemeProvider.tsx`). `localStorage` for persistence (favorites, theme, tunnel seed) with silent fallback on errors.
   - Why: the prop-drilling pattern for feature state is intentional. Adding shared state providers for feature data changes the data flow architecture.

3. **Only `handleBPSChange` uses optimistic update with rollback.**
   All other admin actions use `runAdminAction()` which awaits the API call then refreshes via `fetchData()`. BPS is the exception: it mutates local state immediately and rolls back on error (`src/hooks/useAdmin.ts`).
   - Why: treating other admin handlers as optimistic will skip the server-refresh step and show stale data.
