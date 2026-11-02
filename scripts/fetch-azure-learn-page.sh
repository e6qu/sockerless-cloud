#!/usr/bin/env bash
# Vendor (or refresh) one Microsoft Learn page into specs/cloud-api/azure/ as
# the Markdown learn.microsoft.com serves for it.
#
# scripts/fetch-azure-doc.sh reads a page from its public MicrosoftDocs
# repository. Some pages, such as the Azure Cache for Redis articles, are
# built from a private repository (their front matter names
# MicrosoftDocs/nosql-docs-pr), so no public clone holds them. Learn serves
# every page's Markdown at the page's own URL to a request that accepts
# text/markdown, with front matter naming the source commit (git_commit_id).
# The pin is that commit, which scripts/check-spec-freshness.sh compares
# against the live page.
#
# Usage:
#   scripts/fetch-azure-learn-page.sh <page-path> <local-name>
#   scripts/fetch-azure-learn-page.sh \
#     en-us/azure/azure-cache-for-redis/cache-how-to-import-export-data \
#     redis-import-export-data
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <page-path> <local-name>" >&2
  exit 2
fi

PAGE_PATH="${1#/}"
NAME="$2"
HOST="learn.microsoft.com"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/azure"
DEST="$DEST_DIR/${NAME}.md.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Fetching https://$HOST/$PAGE_PATH"
curl -sSL --fail --max-time 60 -H 'Accept: text/markdown' -o "$work/page.md" "https://$HOST/$PAGE_PATH"
if ! grep -q '^# ' "$work/page.md"; then
  echo "error: https://$HOST/$PAGE_PATH carries no Markdown title" >&2
  exit 1
fi
SHA="$(sed -n 's/^git_commit_id: *\([0-9a-f]\{40\}\) *$/\1/p' "$work/page.md" | head -1)"
if [ -z "$SHA" ]; then
  echo "error: https://$HOST/$PAGE_PATH names no git_commit_id in its front matter" >&2
  exit 1
fi

gzip -9 -n -c "$work/page.md" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "azure" \
  "${NAME}.md.gz" "$HOST" "$PAGE_PATH" "CC-BY-4.0" "commit$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped, commit ${SHA:0:12})"
