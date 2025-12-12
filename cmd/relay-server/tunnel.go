package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/rs/zerolog/log"

	"gosuda.org/portal/utils"
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

echo "Starting portal-tunnel..." >&2
exec "$@"
`

func serveTunnelScript(w http.ResponseWriter, r *http.Request) {
	utils.SetCORSHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	script := fmt.Sprintf(tunnelScriptTemplate, flagPortalURL)

	w.Header().Set("Content-Type", "text/x-shellscript")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Disposition", "inline; filename=\"tunnel.sh\"")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write([]byte(script))
	}
}

func serveTunnelBinary(w http.ResponseWriter, r *http.Request) {
	utils.SetCORSHeaders(w)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	slug := strings.TrimPrefix(r.URL.Path, "/tunnel/bin/")
	slug = strings.Trim(slug, "/")
	path, ok := map[string]string{
		"linux-amd64":  "dist/tunnel/portal-tunnel-linux-amd64",
		"linux-arm64":  "dist/tunnel/portal-tunnel-linux-arm64",
		"darwin-amd64": "dist/tunnel/portal-tunnel-darwin-amd64",
		"darwin-arm64": "dist/tunnel/portal-tunnel-darwin-arm64",
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
