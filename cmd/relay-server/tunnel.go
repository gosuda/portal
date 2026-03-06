package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
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
CHECKSUM_URL="${BIN_URL}.sha256"

TMPDIR="${TMPDIR:-/tmp}"
WORKDIR="$(mktemp -d "$TMPDIR/portal-tunnel.XXXXXX" 2>/dev/null || mktemp -d -t portal-tunnel)"
BIN_PATH="$WORKDIR/portal-tunnel"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT INT TERM

echo "Downloading portal-tunnel ($TUNNEL_OS/$TUNNEL_ARCH)..." >&2
curl -fsSL "$BIN_URL" -o "$BIN_PATH"

echo "Verifying SHA256 checksum..." >&2
CHECKSUM_PAYLOAD="$(curl -fsSL "$CHECKSUM_URL")" || {
  echo "Failed to download checksum from $CHECKSUM_URL. Aborting (fail-closed)." >&2
  echo "Hint: verify relay artifact publishing or CDN cache freshness." >&2
  exit 1
}

EXPECTED_SHA="$(printf '%%s\n' "$CHECKSUM_PAYLOAD" | awk '{print $1}' | tr 'A-Z' 'a-z')"
if ! printf '%%s\n' "$EXPECTED_SHA" | grep -Eq '^[0-9a-f]{64}$'; then
  echo "Invalid checksum payload from $CHECKSUM_URL. Aborting (fail-closed)." >&2
  echo "Hint: expected SHA256 sidecar format '<sha256>  <filename>'." >&2
  exit 1
fi

ACTUAL_SHA="$(sha256sum "$BIN_PATH" | awk '{print $1}')"
if [ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]; then
  echo "Checksum mismatch for portal-tunnel binary. Aborting (fail-closed)." >&2
  echo "Hint: relay artifact and checksum may be out of sync or cached stale." >&2
  exit 1
fi

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
$ChecksumUrl = "$BinUrl.sha256"

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

try {
    Write-Host "Verifying SHA256 checksum..."
    $ChecksumPayload = (Invoke-WebRequest -Uri $ChecksumUrl).Content
} catch {
    Write-Error "Failed to download checksum from $ChecksumUrl. Aborting (fail-closed)."
    Write-Error "Hint: verify relay artifact publishing or CDN cache freshness."
    Remove-Item -Recurse -Force $WorkDir
    exit 1
}

$ChecksumMatch = [regex]::Match($ChecksumPayload, '([A-Fa-f0-9]{64})')
if (-not $ChecksumMatch.Success) {
    Write-Error "Invalid checksum payload from $ChecksumUrl. Aborting (fail-closed)."
    Write-Error "Hint: expected SHA256 sidecar format '<sha256>  <filename>'."
    Remove-Item -Recurse -Force $WorkDir
    exit 1
}

$ExpectedHash = $ChecksumMatch.Groups[1].Value.ToLowerInvariant()
$ActualHash = (Get-FileHash -Algorithm SHA256 -Path $BinPath).Hash.ToLowerInvariant()
if ($ActualHash -ne $ExpectedHash) {
    Write-Error "Checksum mismatch for portal-tunnel binary. Aborting (fail-closed)."
    Write-Error "Hint: relay artifact and checksum may be out of sync or cached stale."
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

Write-Host "Starting portal-tunnel..."
try {
    & $BinPath $ArgsList
} finally {
    if (Test-Path $WorkDir) {
        Remove-Item -Recurse -Force $WorkDir
    }
}
`

func serveTunnelScript(w http.ResponseWriter, r *http.Request, portalURL string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	isWindows := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("os")), "windows")
	script := fmt.Sprintf(tunnelScriptTemplate, portalURL)
	contentType := "text/x-shellscript"
	filename := "tunnel.sh"
	if isWindows {
		script = fmt.Sprintf(tunnelPowerShellScriptTemplate, portalURL)
		contentType = "text/plain; charset=utf-8"
		filename = "tunnel.ps1"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", filename))
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(script))
	}
}

func serveTunnelBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tunnel/bin/"), "/")
	checksumRequest := strings.HasSuffix(slug, ".sha256")
	if checksumRequest {
		slug = strings.TrimSuffix(slug, ".sha256")
	}

	data, filename, ok := tunnelBinaryBySlug(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256(data)
	checksumHex := hex.EncodeToString(sum[:])

	if checksumRequest {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprintf(w, "%s  %s\n", checksumHex, filename)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("X-Checksum-Sha256", checksumHex)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func tunnelBinaryBySlug(slug string) ([]byte, string, bool) {
	filename := tunnelBinaryName(slug)
	if data, err := embeddedDistFS.ReadFile("dist/tunnel/" + filename); err == nil {
		return data, filename, true
	}
	return nil, "", false
}

func tunnelBinaryName(slug string) string {
	if strings.HasPrefix(slug, "windows-") {
		return "portal-tunnel-" + slug + ".exe"
	}
	return "portal-tunnel-" + slug
}
