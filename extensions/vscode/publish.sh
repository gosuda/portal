#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  bash ./publish.sh package
  bash ./publish.sh publish [vsce-args...]

Examples:
  bash ./publish.sh package
  bash ./publish.sh publish
  bash ./publish.sh publish patch

Notes:
  - Run this from WSL or another Unix-like shell.
  - Export VSCE_PAT before publish.
  - The script uses npm/npx and skips vsce dependency scanning because
    this extension ships the bundled dist output, not node_modules.
EOF
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

main() {
  local action="${1:-}"
  if [[ -z "$action" ]]; then
    usage
    exit 1
  fi
  shift || true

  require_command npm
  require_command npx

  cd "$(dirname "$0")"

  rm -rf node_modules package-lock.json
  npm install --include=dev
  npm run package

  case "$action" in
    package)
      npx @vscode/vsce package --no-dependencies "$@"
      ;;
    publish)
      if [[ -z "${VSCE_PAT:-}" ]]; then
        echo "VSCE_PAT is required for publish." >&2
        exit 1
      fi
      npx @vscode/vsce publish --no-dependencies "$@"
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"
