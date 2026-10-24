#!/usr/bin/env bash
# Vendor (or refresh) one Microsoft Learn page's Markdown source into
# specs/cloud-api/azure/.
#
# Some behaviour the Swagger specifications leave out — the error a platform
# endpoint answers, the header a front end injects, the file a service writes
# — is stated only on Microsoft Learn. Microsoft publishes the pages' Markdown
# source in the MicrosoftDocs GitHub organization; the simulator takes such
# facts from a vendored, pinned copy of the page, never from memory. The KEDA
# scalers Azure Container Apps runs are documented in kedacore/keda-docs, under
# Apache-2.0, which <license> names.
#
# Usage:
#   scripts/fetch-azure-doc.sh <repo> <upstream-path> <local-name> [<branch> [<license>]]
#   scripts/fetch-azure-doc.sh MicrosoftDocs/entra-docs \
#     docs/identity/managed-identities-azure-resources/how-to-use-vm-token.md \
#     imds-managed-identity-token
#
# The pin is the commit that last touched the page, so the freshness check
# reports drift only when the page itself changes.
set -euo pipefail

if [ $# -lt 3 ] || [ $# -gt 5 ]; then
  echo "usage: $0 <repo> <upstream-path> <local-name> [<branch> [<license>]]" >&2
  exit 2
fi

REPO="$1"
UPSTREAM_PATH="$2"
NAME="$3"
BRANCH="${4:-main}"
LICENSE="${5:-CC-BY-4.0}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/azure"
DEST="$DEST_DIR/${NAME}.md.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
GIT_LFS_SKIP_SMUDGE=1 git clone --quiet --filter=blob:none --no-checkout --single-branch --branch "$BRANCH" "https://github.com/$REPO" "$work/repo"
SHA="$(git -C "$work/repo" log -1 --format=%H -- "$UPSTREAM_PATH")"
if [ -z "$SHA" ]; then
  echo "no upstream commit found for $UPSTREAM_PATH in $REPO" >&2
  exit 1
fi

echo "Fetching $REPO/$UPSTREAM_PATH @ ${SHA:0:12}"
git -C "$work/repo" show "$SHA:$UPSTREAM_PATH" > "$work/page.md"
# A Learn page opens with a YAML metadata block, then its title; a KEDA page
# opens with TOML front matter that carries its title.
if ! grep -q '^# ' "$work/page.md" && ! { head -1 "$work/page.md" | grep -q '^+++$' && grep -q '^title = ' "$work/page.md"; }; then
  echo "error: $UPSTREAM_PATH carries no Markdown title" >&2
  exit 1
fi

gzip -9 -n -c "$work/page.md" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "azure" \
  "${NAME}.md.gz" "$REPO@$BRANCH" "$UPSTREAM_PATH" "$LICENSE" "$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped)"
