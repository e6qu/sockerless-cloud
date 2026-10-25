#!/usr/bin/env bash
# Vendor (or refresh) one page of an AWS guide on docs.aws.amazon.com into
# specs/cloud-api/aws/ as Markdown.
#
# AWS retired the guides' Markdown source on GitHub (scripts/fetch-aws-doc.sh
# reads the guides that kept it); the AWS Lambda Developer Guide is one that
# did not. docs.aws.amazon.com serves each page's Markdown beside its HTML at
# the same path with a .md suffix. The page carries no revision of its own, so
# the pin is the SHA-256 of the Markdown as served, which
# scripts/check-spec-freshness.sh compares against the live page.
#
# Usage:
#   scripts/fetch-aws-docs-page.sh <page-path> <local-name>
#   scripts/fetch-aws-docs-page.sh lambda/latest/dg/lambda-concurrency lambda-concurrency
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <page-path> <local-name>" >&2
  exit 2
fi

PAGE_PATH="${1#/}"
PAGE_PATH="${PAGE_PATH%.md}"
PAGE_PATH="${PAGE_PATH%.html}"
NAME="$2"
HOST="docs.aws.amazon.com"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/aws"
DEST="$DEST_DIR/${NAME}.md.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Fetching https://$HOST/$PAGE_PATH.md"
curl -sSL --fail --max-time 60 -o "$work/page.md" "https://$HOST/$PAGE_PATH.md"
if ! grep -m1 -v '^[[:space:]]*$' "$work/page.md" | grep -q '^# '; then
  echo "error: $PAGE_PATH.md does not start with a Markdown title" >&2
  exit 1
fi
SHA="$(sha256sum "$work/page.md" | cut -d' ' -f1)"

gzip -9 -n -c "$work/page.md" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "aws" \
  "${NAME}.md.gz" "$HOST" "$PAGE_PATH.md" "CC-BY-SA-4.0" "sha256$SHA"

echo "Vendored $DEST ($(wc -c < "$DEST" | tr -d ' ') bytes gzipped, sha256 ${SHA:0:12})"
