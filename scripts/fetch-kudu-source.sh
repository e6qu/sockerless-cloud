#!/usr/bin/env bash
# Vendor (or refresh) one source file of Kudu, the App Service SCM site, into
# specs/cloud-api/azure/.
#
# Kudu has no published API specification: the behaviour of its zip, dump,
# process and log-stream APIs and the file it keeps its settings in are stated
# only by its source, which Microsoft publishes at github.com/projectkudu/kudu.
# The simulator takes such facts from a vendored, pinned copy of the file,
# never from memory.
#
# Usage:
#   scripts/fetch-kudu-source.sh <upstream-path> <local-name> [<branch>]
#   scripts/fetch-kudu-source.sh Kudu.Services/Zip/ZipController.cs kudu-zip-controller
#
# The pin is the commit that last touched the file, so the freshness check
# reports drift only when the file itself changes.
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
  echo "usage: $0 <upstream-path> <local-name> [<branch>]" >&2
  exit 2
fi

REPO="projectkudu/kudu"
UPSTREAM_PATH="$1"
NAME="$2"
BRANCH="${3:-master}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/azure"
DEST="$DEST_DIR/${NAME}.cs.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
git clone --quiet --filter=blob:none --no-checkout --single-branch --branch "$BRANCH" "https://github.com/$REPO" "$work/repo"
SHA="$(git -C "$work/repo" log -1 --format=%H -- "$UPSTREAM_PATH")"
if [ -z "$SHA" ]; then
  echo "no upstream commit found for $UPSTREAM_PATH in $REPO" >&2
  exit 1
fi

echo "Fetching $REPO/$UPSTREAM_PATH @ ${SHA:0:12}"
git -C "$work/repo" show "$SHA:$UPSTREAM_PATH" > "$work/source.cs"
if ! grep -q '^namespace Kudu' "$work/source.cs"; then
  echo "error: $UPSTREAM_PATH is not a Kudu C# source file" >&2
  exit 1
fi

gzip -9 -n -c "$work/source.cs" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "azure" \
  "${NAME}.cs.gz" "$REPO@$BRANCH" "$UPSTREAM_PATH" "Apache-2.0" "$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped)"
