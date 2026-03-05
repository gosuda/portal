# Relay Server Frontend

React + TypeScript frontend for relay server discovery and onboarding.

## Tech Stack

- React 19
- TypeScript
- Vite 7
- Tailwind CSS 4
- shadcn/ui (Radix-based)
- Lucide React
- @ssgoi/react (page transitions)
- React Compiler (`babel-plugin-react-compiler`, enabled in `vite.config.ts`) — do not use `useCallback` in new code

## Project Structure

```text
frontend/
├── src/
│   ├── components/
│   │   ├── ui/              # shadcn/ui base components
│   │   ├── Header.tsx       # Header + add-server entry point
│   │   ├── SearchBar.tsx    # Search + filters (status, sort, tags)
│   │   ├── ServerCard.tsx   # Server list card
│   │   ├── ServerListView.tsx # Shared server/admin list view
│   │   ├── TagCombobox.tsx  # Tag filter control
│   │   └── FloatingActionBar.tsx # Admin bulk actions
│   ├── hooks/
│   │   ├── useSSRData.ts    # Reads __SSR_DATA__ injected by Go backend
│   │   ├── useServerList.ts  # Converts SSR payload into list models
│   │   ├── useAdmin.ts       # Admin API integration and actions
│   │   ├── useList.ts        # Shared list filtering/sorting state
│   │   └── useAuth.ts        # Admin auth helper hooks
│   ├── lib/
│   │   ├── apiClient.ts
│   │   ├── apiPaths.ts
│   │   ├── testUtils.ts      # Optional test fixtures
│   │   └── utils.ts
│   ├── pages/
│   │   ├── Admin.tsx         # Admin area shell
│   │   ├── AdminLogin.tsx    # Login flow UI
│   │   ├── ServerDetail.tsx  # Server detail view with page transition
│   │   └── ServerList.tsx    # Listing pages and route assembly
│   ├── App.tsx
│   ├── main.tsx
│   └── index.css
├── index.html
├── package.json
├── tsconfig.json
└── vite.config.ts
```

## Core Behavior

### Server-Side Data Bootstrap

1. Go backend injects lease data into `portal.html` using `<script id="__SSR_DATA__">`.
2. Frontend reads it with `useSSRData()`.
3. UI renders server list immediately without an initial fetch.
4. Admin pages then call `/admin/*` endpoints through `apiClient` for stateful actions (approve, deny, ban, settings).

### List Filtering and Sort

- List logic is centralized in `useList` and shared across admin and public server views.
- Search fields include server name, description, and tags.
- Filters include status and tag selection.
- Sort options include default, description, tags, owner, and timestamp ordering.

## Tailwind CSS v4 Notes

- Uses CSS-first config (`@theme` in `index.css`)
- Uses `@tailwindcss/vite` plugin
- Uses `@import "tailwindcss"` syntax

## Install and Build

### Install

```bash
cd frontend
npm install
```

### Development

```bash
npm run dev
```

Default dev URL: `http://localhost:5173`.

### Production Build

```bash
npm run build
```

Build output:

- `dist/portal.html` (entry HTML served by Go server)
- `dist/assets/` (bundled JS/CSS)

### NPM Scripts

| Script | Purpose |
| --- | --- |
| `npm run dev` | Start the Vite development server (`http://localhost:5173`). |
| `npm run build` | Type-check and build production assets into `dist/`. |
| `npm run lint` | Run ESLint with warnings treated as errors. |
| `npm run typecheck` | Run TypeScript checking with `--noEmit`. |
| `npm test` | Run the frontend test suite with `vitest run`. |
| `npm run test:watch` | Run Vitest in watch mode for local TDD cycles. |
| `npm run test:coverage` | Run Vitest with coverage reporting. |
| `npm run preview` | Preview the production bundle with Vite. |
| `npm run build:go` | Build the relay server binary used by local serve flow. |
| `npm run serve` | Build frontend + Go binary, then launch relay server on admin port `4017`. |

## Relay Server Integration

Relay server exposes:

- `/` - React frontend with SSR bootstrap payload
- `/app/` - Static frontend assets
- `/healthz` - Health endpoint
- `/admin/*` - Admin API/control endpoints used by server management UI
- `/sdk/*` - SDK/control endpoints (`/sdk/connect` opens the raw TCP reverse channel used by the relay)

Admin endpoints use a JSON envelope contract (`{ ok, data, error }`) and reject malformed or non-JSON responses with explicit API client errors.

Admin lease ID contract:

- `/admin/leases` rows return plain lease IDs in `Peer`.
- `/admin/leases/banned` returns plain lease IDs (`[]string`).
- Frontend only Base64URL-encodes lease IDs when constructing admin action routes (`/admin/leases/{encodedLeaseID}/{action}`).

### SDK-Related Runtime Contract

The relay enforces a consistent anti-abuse gate for both control APIs and reverse admission:

- `/sdk/register`, `/sdk/unregister`, `/sdk/renew`, and `/sdk/domain` return JSON envelopes (`{ ok, data, error }`).
- `/sdk/register`, `/sdk/connect`, `/sdk/renew`, and `/sdk/unregister` use token-based admission.
- Control-plane admission order is deterministic: `IP -> Lease -> Token`.
- `/sdk/connect` is additionally re-validated inside `ReverseHub` before pooling so token and IP authorization are applied at both admission layers.

### Run with Relay Server

```bash
# Build frontend (output: ../cmd/relay-server/dist/app/)
cd frontend
npm run build

# Run relay server (embeds dist/ at compile time)
cd ..
go run ./cmd/relay-server/*.go -adminport 4017
```

Or use the combined script:

```bash
cd frontend
npm run serve
```

## Technical Notes

- Backend relay/tunnel transport is raw TCP reverse-connect only.
- SNI routing keeps exact `PORTAL_URL` host fallbacks on the admin/API listener to preserve portal dashboard control-plane locality.

### Connection Responsibilities

- Conn #1 (`browser -> app`) is the data plane and keeps existing tenant-facing TLS behavior.
- Conn #2 (`relay -> tunnel`) is the control plane and enforces lease token admission.

### Breaking-Change Expectation

- Clients with invalid lease tokens are expected to fail admission.
- Client certificates are not required for `/sdk/*` admission.

### Radix Select Values

Radix Select values cannot be empty strings. Use stable values such as `"all"` and `"default"`.

### API-Response Edge Cases

- API responses are validated via `APIClient` envelope decoding; malformed payloads are surfaced as explicit runtime errors.
- Non-admin rendering still works using SSR bootstrap data when admin calls are unavailable.

## License

Part of `gosuda/portal`.
