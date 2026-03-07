# Portal — VSCode Extension

Expose your local service to the internet via a [Portal](https://github.com/gosuda/portal) relay tunnel, directly from VSCode — no terminal copy-paste needed.

## Features

- **Portal: Start Tunnel** — prompts for host, service name, relay URL, and optional thumbnail, then runs the tunnel command in the integrated terminal
- **Portal: Stop Tunnel** — stops the active tunnel terminal
- Persisted settings for relay URLs, default host, and default service name
- Auto-detects OS (macOS/Linux uses `curl`, Windows uses PowerShell)

## Requirements

- A running [Portal relay server](https://github.com/gosuda/portal) (self-hosted or public)
- `curl` on macOS/Linux, PowerShell on Windows

## Settings

| Setting | Default | Description |
|---|---|---|
| `portal.relayUrls` | `[]` | Relay server URLs. If empty, prompted on each start. |
| `portal.defaultHost` | `localhost:3000` | Default local host:port to expose. |
| `portal.defaultName` | `""` | Default tunnel service name. Falls back to workspace folder name. |

Example `settings.json`:

```json
{
  "portal.relayUrls": ["https://my-relay.example.com"],
  "portal.defaultHost": "localhost:3000",
  "portal.defaultName": "my-app"
}
```

## Usage

1. Open Command Palette (`Cmd+Shift+P` / `Ctrl+Shift+P`)
2. Run **Portal: Start Tunnel**
3. Fill in the prompts (host, name, relay URL, thumbnail URL — all pre-filled from settings)
4. Tunnel starts in the integrated terminal and prints the public URL

To stop: run **Portal: Stop Tunnel** or close the `Portal Tunnel` terminal.

## Development

```bash
git clone https://github.com/gosuda/portal
cd portal/extensions/vscode/portal
pnpm install
```

Open the folder in VSCode, then press `F5` to launch the Extension Development Host.

## Release Notes

### 0.0.1

Initial release — Start/Stop Tunnel commands with thumbnail support.
