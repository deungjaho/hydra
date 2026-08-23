#!/bin/bash
# sync-hydra-catalog.sh — fetch Codex model catalog from Hydra and write
# to ~/.codex/hydra-catalog.json for use with model_catalog_json config.
#
# Usage:
#   ./sync-hydra-catalog.sh [HYDRA_URL] [API_KEY]
#
# Defaults read from ~/.codex/config.toml if not provided.

set -euo pipefail

CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"
OUTPUT="$CODEX_HOME/hydra-catalog.json"

# Parse Hydra base_url and auth from config.toml if args not provided.
HYDRA_URL="${1:-}"
API_KEY="${2:-}"

if [ -z "$HYDRA_URL" ]; then
    HYDRA_URL=$(grep -A5 'model_providers.hydra' "$CODEX_HOME/config.toml" 2>/dev/null \
        | grep 'base_url' | head -1 | sed 's/.*= *"\(.*\)"/\1/')
fi
if [ -z "$API_KEY" ]; then
    # Try to find a hydra API key in auth.json or config.
    API_KEY=$(grep -o 'hydra-[a-f0-9]*' "$CODEX_HOME/config.toml" 2>/dev/null | head -1)
fi

if [ -z "$HYDRA_URL" ]; then
    HYDRA_URL="http://127.0.0.1:18045"
fi

if [ -z "$API_KEY" ]; then
    echo "Error: no Hydra API key found. Pass as second argument." >&2
    exit 1
fi

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

HTTP_CODE=$(curl -s -o "$TMP" -w "%{http_code}" \
    -H "Authorization: Bearer $API_KEY" \
    "$HYDRA_URL/v1/catalog")

if [ "$HTTP_CODE" != "200" ]; then
    echo "Error: Hydra catalog fetch failed (HTTP $HTTP_CODE)" >&2
    cat "$TMP" >&2
    exit 1
fi

# Validate JSON before writing.
if ! jq empty "$TMP" 2>/dev/null; then
    echo "Error: invalid JSON response" >&2
    cat "$TMP" >&2
    exit 1
fi

mv "$TMP" "$OUTPUT"
trap - EXIT
echo "Synced Hydra catalog to $OUTPUT ($(jq '.models | length' "$OUTPUT") models)"
