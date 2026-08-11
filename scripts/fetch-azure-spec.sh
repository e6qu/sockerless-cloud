#!/usr/bin/env bash
# Vendor (or refresh) one Azure Swagger 2.0 spec into specs/cloud-api/azure/.
#
# Azure/azure-rest-api-specs is the machine-readable source the Azure SDKs
# and terraform provider surfaces are generated from. One service usually
# needs the resource-manager spec for the exact api-version the simulator
# registers (and sometimes a data-plane spec as well), so the upstream path
# is given in full.
#
# Usage:
#   scripts/fetch-azure-spec.sh <upstream-path> <local-name> [<ref>]
#   scripts/fetch-azure-spec.sh \
#     specification/app/resource-manager/Microsoft.App/stable/2024-03-01/ContainerApps.json \
#     containerapps-2024-03-01
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
  echo "usage: $0 <upstream-path> <local-name> [<ref>]" >&2
  exit 2
fi

UPSTREAM_PATH="$1"
NAME="$2"
REF_EXPLICIT="${3:-}"
REF="${3:-main}"
REPO="Azure/azure-rest-api-specs"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/azure"
DEST="$DEST_DIR/${NAME}.swagger.json.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

# Pin the commit that last touched THIS file, not the branch tip. The freshness
# check compares a pin against `commits?path=…`, so pinning the tip made every
# freshly vendored spec report drift immediately and buried genuine staleness in
# bookkeeping noise. An explicit ref argument still wins.
if [ -n "${REF_EXPLICIT:-}" ]; then
  SHA="$(gh api "repos/$REPO/commits/$REF" --jq .sha)"
else
  SHA="$(gh api "repos/$REPO/commits?path=$UPSTREAM_PATH&per_page=1" --jq '.[0].sha')"
  if [ -z "$SHA" ] || [ "$SHA" = "null" ]; then
    echo "no upstream commit found for $UPSTREAM_PATH in $REPO" >&2
    exit 1
  fi
fi

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
echo "Fetching $REPO/$UPSTREAM_PATH @ ${SHA:0:12}"
curl -fsSL -o "$tmp" "https://raw.githubusercontent.com/$REPO/$SHA/$UPSTREAM_PATH"

if ! jq -e '.swagger == "2.0" and (.paths | type == "object")' "$tmp" >/dev/null 2>&1; then
  echo "error: downloaded file is not a Swagger 2.0 document" >&2
  exit 1
fi

gzip -9 -n -c "$tmp" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "azure" \
  "${NAME}.swagger.json.gz" "$REPO" "$UPSTREAM_PATH" "MIT" "$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped)"
