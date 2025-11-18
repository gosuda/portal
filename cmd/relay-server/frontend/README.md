# Relay Server Frontend

ServerShare-style frontend built with React + TypeScript + shadcn/ui, featuring dynamic GeoIP-based server filtering and SSR data integration.

## Tech Stack

- **React 19** - UI library
- **TypeScript** - Type safety
- **Vite 7** - Build tool
- **Tailwind CSS 4** - CSS-first configuration styling
- **shadcn/ui** - UI components (Radix UI-based)
- **Lucide React** - Icons

## Project Structure

```
frontend/
├── src/
│   ├── components/
│   │   ├── ui/              # shadcn/ui base components
│   │   │   ├── button.tsx
│   │   │   ├── input.tsx
│   │   │   └── select.tsx
│   │   ├── Header.tsx       # Header component
│   │   ├── SearchBar.tsx    # Search and filter bar (Country, Status, Sort By)
│   │   ├── ServerCard.tsx   # Server card component
│   │   └── Pagination.tsx   # Pagination component
│   ├── hooks/
│   │   └── useSSRData.ts    # SSR data hook (reads __SSR_DATA__ from Go backend)
│   ├── lib/
│   │   ├── utils.ts         # Utility functions
│   │   └── countries.ts     # ISO 3166-1 alpha-2 country code mapping (~200 countries)
│   ├── App.tsx              # Main app component (filter logic, dynamic country extraction)
│   ├── main.tsx             # Entry point
│   └── index.css            # Global styles and Tailwind CSS 4 configuration
├── index.html
├── package.json
├── tsconfig.json
└── vite.config.ts           # @tailwindcss/vite plugin
```

## Key Features

### 1. Server-Side Rendering (SSR)
- Go backend injects server data into HTML via `<script id="__SSR_DATA__">` tag
- Frontend reads SSR data using `useSSRData()` hook
- Zero initial loading time for server list

### 2. GeoIP Integration
- Backend uses MaxMind GeoLite2-Country.mmdb database
- Automatically detects server location based on IP address
- Provides ISO 3166-1 alpha-2 country codes (e.g., "US", "KR", "JP")

### 3. Dynamic Country Filtering
- Extracts unique countries from currently connected servers
- Only displays countries that actually have servers online
- Full country mapping (~200 countries) in `lib/countries.ts`
- Converts country codes to human-readable names

### 4. Advanced Filter System
Three Select dropdowns:
- **Country**: Filter by GeoIP-detected location (dynamic list)
- **Status**: Filter by online/offline status
- **Sort By**: Sort by Description, Tags, or Owner

### 5. Search Functionality
Search across:
- Server names
- Descriptions
- Tags

### Tailwind CSS 4 Migration

This project uses Tailwind CSS v4:

- ✅ CSS-first configuration (`@theme` directive in `index.css`)
- ✅ `@tailwindcss/vite` plugin
- ✅ Removed `tailwind.config.js` (config moved to CSS)
- ✅ Removed `postcss.config.js` (auto-handled)
- ✅ `@import "tailwindcss"` syntax

For details, see [Tailwind CSS v4 Official Documentation](https://tailwindcss.com/docs/upgrade-guide).

## Installation and Build

### Install Dependencies

```bash
cd cmd/relay-server/frontend
npm install
```

### Development Server

```bash
npm run dev
```

Development server runs at `http://localhost:5173`.

### Production Build

```bash
npm run build
```

Build output in `/dist` directory:
- `portal.html` - Main HTML file (served by Go server with SSR data injection)
- `assets/` - JS and CSS bundles

### Using Makefile

```bash
# Install dependencies
make install

# Build
make build

# Development server
make dev

# Clean
make clean
```

## Go Server Integration

The relay-server serves the frontend at:

- `/` - React frontend (ServerShare UI with SSR data)
- `/app/` - React app static assets
- `/healthz` - Health check endpoint
- `/relay` - WebSocket relay endpoint

### Running the Server

```bash
# Build frontend
cd cmd/relay-server/frontend
npm run build

# Run server
cd ../../..
go run cmd/relay-server/*.go -port 4017
```

Or specify static directory:

```bash
STATIC_DIR=./dist go run cmd/relay-server/*.go -port 4017
```

### SSR Data Flow

1. Go backend reads lease entries and GeoIP data
2. Converts to JSON and injects into `portal.html` as `<script id="__SSR_DATA__">`
3. React app reads SSR data via `useSSRData()` hook
4. Displays server list with GeoIP-based filtering

## Design System

### Colors

- **Primary**: `#47cdff` - Primary action buttons
- **Background Light**: `#f5f8f8` - Light mode background
- **Background Dark**: `#0f1e23` - Dark mode background (default)
- **Green Status**: `#50E3C2` - Online status indicator

### Components

#### Header
Navigation header with logo and "Add Your Server" button

#### SearchBar
Search input + three filter dropdowns:
- Country (GeoIP-based, dynamic)
- Status (All/Online/Offline)
- Sort By (Default/Description/Tags/Owner)

#### ServerCard
Server information card displaying:
- Thumbnail image
- Online/offline status badge
- Server name
- Description
- Tags (including country)
- Owner information
- Connect button

#### Pagination
Page navigation with previous/next buttons (6 items per page)

## Implementation Status

### Completed Features
- ✅ Server-side rendering (SSR) with Go backend
- ✅ GeoIP integration with MaxMind GeoLite2-Country database
- ✅ Dynamic country filtering (shows only countries with connected servers)
- ✅ Search functionality (name, description, tags)
- ✅ Status filtering (online/offline)
- ✅ Sort by Description, Tags, or Owner
- ✅ Pagination (6 servers per page)
- ✅ Dark mode UI (default)
- ✅ Responsive design (mobile/tablet/desktop)
- ✅ ISO 3166-1 alpha-2 country code mapping (~200 countries)
- ✅ Radix UI Select components with proper value handling

## Technical Notes

### Radix UI Select Constraints
- Select values cannot be empty strings (`""`)
- Use meaningful defaults: `"all"`, `"default"` instead of `""`

### GeoIP Detection
- Detects country based on server IP address
- Localhost (::1) connections show no country
- Requires external IP addresses for proper country detection

### Dynamic Country List
- Extracts unique countries from connected servers using `useMemo` and `Set`
- Updates automatically as servers connect/disconnect
- Maps country codes to human-readable names via `COUNTRY_NAMES`

## License

This project is part of gosuda/portal.
