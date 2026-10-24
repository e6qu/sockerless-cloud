#!/usr/bin/env bash
# Vendor (or refresh) one page of Google Cloud's documentation into
# specs/cloud-api/gcp/ as Markdown.
#
# Some behaviour the Discovery documents leave out — how Cloud Run turns a
# JSON line a container writes into a LogEntry, how it divides service-level
# instance counts among the revisions of a traffic split — is stated only on a
# documentation page. Google publishes no source for those pages, so the
# page's article element is fetched as served and converted to Markdown with
# pandoc; the simulator takes such facts from this pinned copy, never from
# memory. The pin is the "Last updated" date the page carries, which is what
# scripts/check-spec-freshness.sh compares against the live page.
#
# Usage:
#   scripts/fetch-gcp-doc.sh <page-path> <local-name>
#   scripts/fetch-gcp-doc.sh run/docs/logging cloudrun-logging
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <page-path> <local-name>" >&2
  exit 2
fi

PAGE_PATH="${1#/}"
NAME="$2"
HOST="docs.cloud.google.com"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/gcp"
DEST="$DEST_DIR/${NAME}.md.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

command -v pandoc >/dev/null || { echo "pandoc is required to convert the page to Markdown" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Fetching https://$HOST/$PAGE_PATH"
curl -sSL --fail --max-time 60 -o "$work/page.html" "https://$HOST/$PAGE_PATH"

MODIFIED="$(grep -o 'Last updated [0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\} UTC' "$work/page.html" | head -1 | sed 's/Last updated \(.*\) UTC/\1/')"
if [ -z "$MODIFIED" ]; then
  echo "error: the page carries no 'Last updated' date to pin" >&2
  exit 1
fi

python3 -I - "$work/page.html" "$work/article.html" <<'PY'
import re, sys

html = open(sys.argv[1], encoding='utf-8').read()
article = re.search(r'<article[^>]*>(.*?)</article>', html, re.S)
if not article:
    sys.exit('the page has no <article> element; the layout changed')
title = re.search(r'<h1[^>]*>(.*?)</h1>', article.group(1), re.S)
body = re.search(r'(<div class="devsite-article-body[^"]*">.*)', article.group(1), re.S)
if not title or not body:
    sys.exit('the article has no <h1> or no devsite-article-body; the layout changed')
# The popout spans inside the title are site chrome, not the page's name.
heading = re.sub(r'<span slot="popout-[^"]*">.*?</span>', '', title.group(1), flags=re.S)
open(sys.argv[2], 'w', encoding='utf-8').write('<h1>' + heading.strip() + '</h1>\n' + body.group(1))
PY

pandoc -f html -t gfm --wrap=none "$work/article.html" -o "$work/page.md"
if ! head -1 "$work/page.md" | grep -q '^# '; then
  echo "error: the converted page does not start with a Markdown title" >&2
  exit 1
fi

gzip -9 -n -c "$work/page.md" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "gcp" \
  "${NAME}.md.gz" "$HOST" "$PAGE_PATH" "CC-BY-4.0" "modified$MODIFIED"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped, last updated $MODIFIED)"
