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
RELAY_URL="${RELAY_URL:-$BASE_URL}"
BIN_URL="${BIN_URL:-$BASE_URL/tunnel/bin/$TUNNEL_OS-$TUNNEL_ARCH}"

TMPDIR="${TMPDIR:-/tmp}"
WORKDIR="$(mktemp -d "$TMPDIR/portal-tunnel.XXXXXX" 2>/dev/null || mktemp -d -t portal-tunnel)"
BIN_PATH="$WORKDIR/portal-tunnel"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT INT TERM

echo "Downloading portal-tunnel ($TUNNEL_OS/$TUNNEL_ARCH)..." >&2
curl -fsSL "$BIN_URL" -o "$BIN_PATH"
chmod +x "$BIN_PATH"

set -- "$BIN_PATH" --relay "$RELAY_URL" --host "${HOST:-localhost:3000}"
[ -n "${NAME:-}" ] && set -- "$@" --name "$NAME"
[ -n "${DESCRIPTION:-}" ] && set -- "$@" --description "$DESCRIPTION"
[ -n "${TAGS:-}" ] && set -- "$@" --tags "$TAGS"
[ -n "${THUMBNAIL:-}" ] && set -- "$@" --thumbnail "$THUMBNAIL"
[ -n "${OWNER:-}" ] && set -- "$@" --owner "$OWNER"
if [ "${HIDE:-}" = "1" ] || [ "${HIDE:-}" = "true" ]; then
  set -- "$@" --hide
fi
[ -n "${TLS_MODE:-}" ] && set -- "$@" --tls-mode "$TLS_MODE"
if [ "${TLS_MODE:-}" = "self" ]; then
  [ -n "${TLS_CERT_FILE:-}" ] && set -- "$@" --tls-cert-file "$TLS_CERT_FILE"
  [ -n "${TLS_KEY_FILE:-}" ] && set -- "$@" --tls-key-file "$TLS_KEY_FILE"
fi

echo "Starting portal-tunnel..." >&2
exec "$@"
`

const tunnelPowerShellScriptTemplate = `$ErrorActionPreference = "Stop"

$BaseUrl = if ($env:BASE_URL) { $env:BASE_URL } else { "%s" }
$RelayUrl = if ($env:RELAY_URL) { $env:RELAY_URL } else { $BaseUrl }

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

$ArgsList = @("--relay", $RelayUrl)

if ($env:HOST) { $ArgsList += "--host", $env:HOST } else { $ArgsList += "--host", "localhost:3000" }
if ($env:NAME) { $ArgsList += "--name", $env:NAME }
if ($env:DESCRIPTION) { $ArgsList += "--description", $env:DESCRIPTION }
if ($env:TAGS) { $ArgsList += "--tags", $env:TAGS }
if ($env:THUMBNAIL) { $ArgsList += "--thumbnail", $env:THUMBNAIL }
if ($env:OWNER) { $ArgsList += "--owner", $env:OWNER }
if ($env:HIDE -eq "1" -or $env:HIDE -eq "true") { $ArgsList += "--hide" }
if ($env:TLS_MODE) { $ArgsList += "--tls-mode", $env:TLS_MODE }
if ($env:TLS_MODE -eq "self") {
    if ($env:TLS_CERT_FILE) { $ArgsList += "--tls-cert-file", $env:TLS_CERT_FILE }
    if ($env:TLS_KEY_FILE) { $ArgsList += "--tls-key-file", $env:TLS_KEY_FILE }
}

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
		w.Write([]byte(script))
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
		w.Write(data)
	}
}
