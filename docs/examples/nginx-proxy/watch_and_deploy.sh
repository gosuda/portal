#!/usr/bin/env bash
set -euo pipefail

IMAGE="ghcr.io/gosuda/portal:latest"
DIGEST_FILE="${DIGEST_FILE:-.portal_image_digest}"
INTERVAL="${INTERVAL:-60}"
DEPLOY_SCRIPT="${DEPLOY_SCRIPT:-deploy_portal.sh}"

get_remote_digest() {
    docker manifest inspect "$IMAGE" 2>/dev/null \
        | grep -m1 '"digest"' \
        | awk -F'"' '{print $4}'
}

echo "Watching $IMAGE for digest changes (interval: ${INTERVAL}s)"
echo "Deploy script: $DEPLOY_SCRIPT"

while true; do
    NEW_DIGEST=$(get_remote_digest)

    if [[ -z "$NEW_DIGEST" ]]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Failed to fetch digest, retrying in ${INTERVAL}s"
        sleep "$INTERVAL"
        continue
    fi

    OLD_DIGEST=""
    if [[ -f "$DIGEST_FILE" ]]; then
        OLD_DIGEST=$(cat "$DIGEST_FILE")
    fi

    if [[ "$NEW_DIGEST" != "$OLD_DIGEST" ]]; then
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Digest changed: ${OLD_DIGEST:-<none>} -> $NEW_DIGEST"
        echo "$NEW_DIGEST" > "$DIGEST_FILE"
        bash "$DEPLOY_SCRIPT"
        echo "[$(date '+%Y-%m-%d %H:%M:%S')] Deploy completed"
    fi

    sleep "$INTERVAL"
done
