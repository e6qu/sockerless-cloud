#!/usr/bin/env bash
# Vendor (or refresh) one AWS Smithy 2.0 service model into specs/cloud-api/aws/.
#
# The models are the machine-readable source the AWS SDKs are generated from
# (aws/aws-sdk-go-v2 codegen/sdk-codegen/aws-models/<service>.json). The
# simulator spec-conformance tests validate every registered AWS operation
# against these vendored snapshots, so CI never downloads them.
#
# Usage:
#   scripts/fetch-aws-spec.sh <model-name> [<ref>]
#   scripts/fetch-aws-spec.sh ecs            # pin to current main
#   scripts/fetch-aws-spec.sh ecs release-2026-06-05
#
# <model-name> is the filename stem under codegen/sdk-codegen/aws-models/.
# The ref (default: main) is resolved to a concrete commit SHA so the
# vendored file is reproducibly pinned; the SHA + fetch time are recorded
# in specs/cloud-api/aws/SOURCES.md.
set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "usage: $0 <model-name> [<ref>]" >&2
  exit 2
fi

MODEL="$1"
REF_EXPLICIT="${2:-}"
REF="${2:-main}"
REPO="aws/aws-sdk-go-v2"
UPSTREAM_PATH="codegen/sdk-codegen/aws-models/${MODEL}.json"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/aws"
DEST="$DEST_DIR/${MODEL}.smithy.json.gz"
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

# A Smithy 2.0 AST has a "smithy" version key and a "shapes" map containing
# exactly one service shape.
if ! jq -e '.smithy and (.shapes | type == "object")' "$tmp" >/dev/null 2>&1; then
  echo "error: downloaded file is not a Smithy AST model" >&2
  exit 1
fi
services="$(jq '[.shapes[] | select(.type == "service")] | length' "$tmp")"
if [ "$services" != "1" ]; then
  echo "error: expected exactly 1 service shape, found $services" >&2
  exit 1
fi

gzip -9 -n -c "$tmp" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "aws" \
  "${MODEL}.smithy.json.gz" "$REPO" "$UPSTREAM_PATH" "Apache-2.0" "$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped)"
