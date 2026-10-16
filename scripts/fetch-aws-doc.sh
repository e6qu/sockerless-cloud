#!/usr/bin/env bash
# Vendor (or refresh) one page of an AWS user guide's Markdown source into
# specs/cloud-api/aws/.
#
# Some behaviour the Smithy models leave out — the event messages a service
# writes, the shape of the events it sends to Amazon EventBridge — is stated
# only in the service's user guide. AWS published the guides' Markdown source
# in the awsdocs GitHub organization; the simulator takes such facts from a
# vendored, pinned copy of the page, never from memory.
#
# Usage:
#   scripts/fetch-aws-doc.sh <repo> <page> [<local-name> [<branch>]]
#   scripts/fetch-aws-doc.sh awsdocs/amazon-rds-user-guide USER_Events.Messages rds-event-messages
#
# <page> is the file stem under doc_source/ on <branch> (default main: an
# archived guide's default branch deletes the pages). The pin is the commit
# that last touched the page, so the freshness check reports drift only when
# the page itself changes.
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 4 ]; then
  echo "usage: $0 <repo> <page> [<local-name> [<branch>]]" >&2
  exit 2
fi

REPO="$1"
PAGE="$2"
NAME="${3:-$PAGE}"
BRANCH="${4:-main}"
UPSTREAM_PATH="doc_source/${PAGE}.md"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/aws"
DEST="$DEST_DIR/${NAME}.md.gz"
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
git -C "$work/repo" show "$SHA:$UPSTREAM_PATH" > "$work/page.md"
if ! head -1 "$work/page.md" | grep -q '^# '; then
  echo "error: $UPSTREAM_PATH does not start with a Markdown title" >&2
  exit 1
fi

gzip -9 -n -c "$work/page.md" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "aws" \
  "${NAME}.md.gz" "$REPO@$BRANCH" "$UPSTREAM_PATH" "CC-BY-SA-4.0" "$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped)"
