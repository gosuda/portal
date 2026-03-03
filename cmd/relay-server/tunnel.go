package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"
)

const tunnelScriptTemplate = `#!/usr/bin/env sh
set -e

OS="$(uname -s)"
case "$OS" in
  Linux) TUNNEL_OS="linux" ;;
  Darwin) TUNNEL_OS="darwin" ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) TUNNEL_ARCH="amd64" ;;
  arm64|aarch64) TUNNEL_ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

BASE_URL="${BASE_URL:-%s}"
RELAYS="${RELAYS:-$BASE_URL}"
BIN_URL="${BIN_URL:-$BASE_URL/tunnel/bin/$TUNNEL_OS-$TUNNEL_ARCH}"

TMPDIR="${TMPDIR:-/tmp}"
WORKDIR="$(mktemp -d "$TMPDIR/portal-tunnel.XXXXXX" 2>/dev/null || mktemp -d -t portal-tunnel)"
BIN_PATH="$WORKDIR/portal-tunnel"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT INT TERM

echo "Downloading portal-tunnel ($TUNNEL_OS/$TUNNEL_ARCH)..." >&2
curl -fsSL "$BIN_URL" -o "$BIN_PATH"
chmod +x "$BIN_PATH"

set -- "$BIN_PATH" --relay "$RELAYS" --host "${APP_HOST:-localhost:3000}"
[ -n "${APP_NAME:-}" ] && set -- "$@" --name "$APP_NAME"
[ -n "${APP_DESCRIPTION:-}" ] && set -- "$@" --description "$APP_DESCRIPTION"
[ -n "${APP_TAGS:-}" ] && set -- "$@" --tags "$APP_TAGS"
[ -n "${APP_THUMBNAIL:-}" ] && set -- "$@" --thumbnail "$APP_THUMBNAIL"
[ -n "${APP_OWNER:-}" ] && set -- "$@" --owner "$APP_OWNER"
if [ "${APP_HIDE:-}" = "1" ] || [ "${APP_HIDE:-}" = "true" ]; then
  set -- "$@" --hide
fi
if [ "${TLS:-}" = "1" ] || [ "${TLS:-}" = "true" ]; then
  set -- "$@" --tls
fi

echo "Starting portal-tunnel..." >&2
exec "$@"
`

const tunnelPowerShellScriptTemplate = `$ErrorActionPreference = "Stop"

$BaseUrl = if ($env:BASE_URL) { $env:BASE_URL } else { "%s" }
$Relays = if ($env:RELAYS) { $env:RELAYS } else { $BaseUrl }

$Arch = $env:PROCESSOR_ARCHITECTURE
if ($Arch -eq "AMD64") {
    $TunnelArch = "amd64"
} elseif ($Arch -eq "ARM64") {
    $TunnelArch = "arm64"
} else {
    Write-Error "Unsupported architecture: $Arch"
    exit 1
}

$BinUrl = if ($env:BIN_URL) { $env:BIN_URL } else { "$BaseUrl/tunnel/bin/windows-$TunnelArch" }

$WorkDir = Join-Path $env:TEMP ("portal-tunnel-" + [Guid]::NewGuid().ToString())
New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
$BinPath = Join-Path $WorkDir "portal-tunnel.exe"

try {
    Write-Host "Downloading portal-tunnel (windows/$TunnelArch)..."
    Invoke-WebRequest -Uri $BinUrl -OutFile $BinPath
} catch {
    Write-Error "Failed to download portal-tunnel: $_"
    Remove-Item -Recurse -Force $WorkDir
    exit 1
}

$ArgsList = @("--relay", $Relays)

if ($env:APP_HOST) { $ArgsList += "--host", $env:APP_HOST } else { $ArgsList += "--host", "localhost:3000" }
if ($env:APP_NAME) { $ArgsList += "--name", $env:APP_NAME }
if ($env:APP_DESCRIPTION) { $ArgsList += "--description", $env:APP_DESCRIPTION }
if ($env:APP_TAGS) { $ArgsList += "--tags", $env:APP_TAGS }
if ($env:APP_THUMBNAIL) { $ArgsList += "--thumbnail", $env:APP_THUMBNAIL }
if ($env:APP_OWNER) { $ArgsList += "--owner", $env:APP_OWNER }
if ($env:APP_HIDE -eq "1" -or $env:APP_HIDE -eq "true") { $ArgsList += "--hide" }
if ($env:TLS -eq "1" -or $env:TLS -eq "true") { $ArgsList += "--tls" }

Write-Host "Starting portal-tunnel..."
try {
    & $BinPath $ArgsList
} finally {
    if (Test-Path $WorkDir) {
        Remove-Item -Recurse -Force $WorkDir
    }
}
`

func serveTunnelScript(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	targetOS := r.URL.Query().Get("os")
	isWindows := false
	if targetOS != "" {
		isWindows = strings.EqualFold(targetOS, "windows")
	} else {
		// Fallback: check User-Agent
		ua := strings.ToLower(r.UserAgent())
		isWindows = strings.Contains(ua, "windows")
	}

	var script string
	var contentType string
	var filename string

	if isWindows {
		script = fmt.Sprintf(tunnelPowerShellScriptTemplate, flagPortalURL)
		contentType = "text/plain" // or application/x-powershell
		filename = "tunnel.ps1"
	} else {
		script = fmt.Sprintf(tunnelScriptTemplate, flagPortalURL)
		contentType = "text/x-shellscript"
		filename = "tunnel.sh"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filename))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		if _, err := w.Write([]byte(script)); err != nil {
			log.Debug().Err(err).Msg("failed to write tunnel script")
		}
	}
}

func serveTunnelBinary(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.TrimPrefix(r.URL.Path, "/tunnel/bin/")
	slug = strings.Trim(slug, "/")
	path, ok := map[string]string{
		"linux-amd64":   "dist/tunnel/portal-tunnel-linux-amd64",
		"linux-arm64":   "dist/tunnel/portal-tunnel-linux-arm64",
		"darwin-amd64":  "dist/tunnel/portal-tunnel-darwin-amd64",
		"darwin-arm64":  "dist/tunnel/portal-tunnel-darwin-arm64",
		"windows-amd64": "dist/tunnel/portal-tunnel-windows-amd64.exe",
		"windows-arm64": "dist/tunnel/portal-tunnel-windows-arm64.exe",
	}[slug]
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, err := distFS.ReadFile(path)
	if err != nil {
		log.Error().Err(err).Str("path", path).Msg("failed to read embedded tunnel binary")
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"portal-tunnel-%s\"", slug))
	w.Header().Set("Cache-Control", "public, max-age=600")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		if _, err := w.Write(data); err != nil {
			log.Debug().Err(err).Str("slug", slug).Msg("failed to write tunnel binary")
		}
	}
}
