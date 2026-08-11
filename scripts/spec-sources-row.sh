#!/usr/bin/env bash
# Idempotently upsert one provenance row in a specs/cloud-api/<cloud>/SOURCES.md
# table. Shared by the three fetch-*-spec.sh scripts; not invoked directly.
#
# Usage:
#   spec-sources-row.sh <sources-md> <cloud> <local-file> <repo-or-host> \
#                       <upstream-path> <license> <pin>
set -euo pipefail

if [ $# -ne 7 ]; then
  echo "usage: $0 <sources-md> <cloud> <local-file> <repo-or-host> <upstream-path> <license> <pin>" >&2
  exit 2
fi

SOURCES="$1"
CLOUD="$2"
LOCAL_FILE="$3"
UPSTREAM_REPO="$4"
UPSTREAM_PATH="$5"
LICENSE="$6"
PIN="$7"
FETCHED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

if [ ! -f "$SOURCES" ]; then
  cat > "$SOURCES" <<EOF
# Vendored spec provenance — ${CLOUD}

Verbatim upstream snapshots (gzipped); never edited. Refresh with the
matching \`scripts/fetch-*-spec.sh\` script, which rewrites this table.

| Local file | Upstream repo / host | Upstream path | License | Pinned at | Fetched (UTC) |
|---|---|---|---|---|---|
EOF
fi

ROW="| \`$LOCAL_FILE\` | \`$UPSTREAM_REPO\` | \`$UPSTREAM_PATH\` | $LICENSE | \`$PIN\` | $FETCHED |"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT
grep -vF "| \`$LOCAL_FILE\` |" "$SOURCES" > "$tmp" || true
printf '%s\n' "$ROW" >> "$tmp"
mv "$tmp" "$SOURCES"
trap - EXIT
