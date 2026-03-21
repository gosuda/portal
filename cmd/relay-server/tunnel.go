package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/gosuda/portal/v2/types"
)

const installShellScriptTemplate = `#!/usr/bin/env sh
set -eu

OS="$(uname -s)"
case "$OS" in
  Linux) PORTAL_OS="linux" ;;
  Darwin) PORTAL_OS="darwin" ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) PORTAL_ARCH="amd64" ;;
  arm64|aarch64) PORTAL_ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

BASE_URL="${BASE_URL:-}"
if [ -z "$BASE_URL" ]; then
  BASE_URL=%s
fi
BIN_URL="${BIN_URL:-$BASE_URL/install/bin/$PORTAL_OS-$PORTAL_ARCH}"
CHECKSUM_URL="${BIN_URL}.sha256"
CURL_INSECURE_FLAG=""

case "$BASE_URL" in
  https://localhost|https://localhost:*|https://127.0.0.1|https://127.0.0.1:*|https://[::1]|https://[::1]:*|https://*.localhost|https://*.localhost:*)
    CURL_INSECURE_FLAG="-k"
    ;;
esac

TMPDIR="${TMPDIR:-/tmp}"
WORKDIR="$(mktemp -d "$TMPDIR/portal-install.XXXXXX" 2>/dev/null || mktemp -d -t portal-install)"
BIN_PATH="$WORKDIR/portal"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT INT TERM

echo "Downloading portal ($PORTAL_OS/$PORTAL_ARCH)..." >&2
curl $CURL_INSECURE_FLAG -fsSL "$BIN_URL" -o "$BIN_PATH"

echo "Verifying SHA256 checksum..." >&2
CHECKSUM_PAYLOAD="$(curl $CURL_INSECURE_FLAG -fsSL "$CHECKSUM_URL")" || {
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

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SHA="$(sha256sum "$BIN_PATH" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SHA="$(shasum -a 256 "$BIN_PATH" | awk '{print $1}')"
else
  echo "No SHA256 checksum tool found (need sha256sum or shasum)." >&2
  exit 1
fi
if [ "$ACTUAL_SHA" != "$EXPECTED_SHA" ]; then
  echo "Checksum mismatch for portal binary. Aborting (fail-closed)." >&2
  echo "Hint: relay artifact and checksum may be out of sync or cached stale." >&2
  exit 1
fi

pick_install_path() {
  EXISTING="$(command -v portal 2>/dev/null || true)"
  if [ -n "$EXISTING" ]; then
    EXISTING_DIR="$(dirname "$EXISTING")"
    if [ -d "$EXISTING_DIR" ] && [ -w "$EXISTING_DIR" ]; then
      printf '%%s\n' "$EXISTING"
      return 0
    fi
  fi

  if [ -n "${HOME:-}" ]; then
    for DIR in "$HOME/.local/bin" "$HOME/bin"; do
      mkdir -p "$DIR" 2>/dev/null || true
      if [ -d "$DIR" ] && [ -w "$DIR" ]; then
        printf '%%s\n' "$DIR/portal"
        return 0
      fi
    done
  fi

  return 1
}

INSTALL_PATH="$(pick_install_path)" || {
  echo "No writable install directory found. Ensure an existing portal install is writable or create \$HOME/.local/bin or \$HOME/bin." >&2
  exit 1
}

cp "$BIN_PATH" "$INSTALL_PATH"
chmod +x "$INSTALL_PATH"

echo "Installed portal to $INSTALL_PATH" >&2

INSTALL_DIR="$(dirname "$INSTALL_PATH")"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo "Warning: $INSTALL_DIR is not on PATH. Add it before running 'portal expose 3000'." >&2
    ;;
esac

echo "Next step:" >&2
echo "  portal expose 3000 --relays $BASE_URL" >&2
`

const installPowerShellTemplate = `$ErrorActionPreference = "Stop"
$BaseUrl = if ($env:BASE_URL) { $env:BASE_URL } else { %s }
$OriginalSecurityProtocol = [System.Net.ServicePointManager]::SecurityProtocol
[System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12
$WorkDir = $null
try {
    $Arch = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($Arch -eq "ARM64") {
        $PortalArch = "arm64"
    } elseif ($Arch -eq "AMD64" -or $Arch -eq "x86_64") {
        $PortalArch = "amd64"
    } else {
        throw "Unsupported architecture: $Arch"
    }

    $BinUrl = if ($env:BIN_URL) { $env:BIN_URL } else { "$BaseUrl/install/bin/windows-$PortalArch" }
    $ChecksumUrl = "$BinUrl.sha256"
    $WorkDir = Join-Path $env:TEMP ("portal-install-" + [Guid]::NewGuid().ToString())
    New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
    $BinPath = Join-Path $WorkDir "portal.exe"

    Write-Host "Downloading portal (windows/$PortalArch)..."
    Invoke-WebRequest -UseBasicParsing -Uri $BinUrl -OutFile $BinPath

    Write-Host "Verifying SHA256 checksum..."
    $ChecksumPayload = (Invoke-WebRequest -UseBasicParsing -Uri $ChecksumUrl).Content
    $ChecksumMatch = [regex]::Match($ChecksumPayload, '([A-Fa-f0-9]{64})')
    if (-not $ChecksumMatch.Success) {
        throw "Invalid checksum payload from $ChecksumUrl. Expected '<sha256>  <filename>'."
    }

    $ExpectedHash = $ChecksumMatch.Groups[1].Value.ToLowerInvariant()
    $ActualHash = (Get-FileHash -Algorithm SHA256 -Path $BinPath).Hash.ToLowerInvariant()
    if ($ActualHash -ne $ExpectedHash) {
        throw "Checksum mismatch for portal binary."
    }

    $InstallDir = Join-Path $env:LOCALAPPDATA "portal\bin"
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $InstallPath = Join-Path $InstallDir "portal.exe"
    Copy-Item -Force $BinPath $InstallPath
    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $UserEntries = @()
    if (-not [string]::IsNullOrWhiteSpace($UserPath)) {
        $UserEntries = @($UserPath -split ';' | Where-Object { $_ -ne "" })
    }
    if (-not ($UserEntries -contains $InstallDir)) {
        $NewUserPath = if ([string]::IsNullOrWhiteSpace($UserPath)) {
            $InstallDir
        } else {
            "$InstallDir;$UserPath"
        }
        [Environment]::SetEnvironmentVariable("Path", $NewUserPath, "User")
    }

    $SessionEntries = @($env:Path -split ';' | Where-Object { $_ -ne "" })
    if (-not ($SessionEntries -contains $InstallDir)) {
        $env:Path = "$InstallDir;$env:Path"
    }

    Write-Host "Installed portal to $InstallPath"
    Write-Host "Next step:"
    Write-Host "  portal expose 3000 --relays $BaseUrl"
} finally {
    [System.Net.ServicePointManager]::SecurityProtocol = $OriginalSecurityProtocol
    if ($WorkDir -and (Test-Path $WorkDir)) {
        Remove-Item -Recurse -Force $WorkDir
    }
}
`

func serveInstallBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, types.PathInstallBinPrefix), "/")
	checksumRequest := strings.HasSuffix(slug, ".sha256")
	if checksumRequest {
		slug = strings.TrimSuffix(slug, ".sha256")
	}

	data, filename, ok := installBinaryBySlug(slug)
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

func installBinaryBySlug(slug string) ([]byte, string, bool) {
	filename := "portal-" + slug
	if strings.HasPrefix(slug, "windows-") {
		filename += ".exe"
	}
	if data, err := embeddedDistFS.ReadFile("dist/tunnel/" + filename); err == nil {
		return data, filename, true
	}
	return nil, "", false
}

func serveInstallScript(w http.ResponseWriter, r *http.Request, portalURL string, isWindows bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	script := buildInstallScript(portalURL, isWindows)
	contentType := "text/x-shellscript"
	filename := "install.sh"
	if isWindows {
		contentType = "text/plain; charset=utf-8"
		filename = "install.ps1"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", filename))
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(script))
	}
}

func buildInstallScript(portalURL string, isWindows bool) string {
	if !isWindows {
		quotedPortalURL := "'" + strings.ReplaceAll(portalURL, "'", `'"'"'`) + "'"
		return fmt.Sprintf(installShellScriptTemplate, quotedPortalURL)
	}

	quotedPortalURL := "'" + strings.ReplaceAll(portalURL, "'", "''") + "'"
	return fmt.Sprintf(installPowerShellTemplate, quotedPortalURL)
}
