#!/usr/bin/env bash
# Verify generated simulator surface tables match the registered operations.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_DIR="$REPO_ROOT/specs/SIM_SURFACE_TABLES"
TEMP_ROOT="$(mktemp -d)"
trap 'rm -rf "$TEMP_ROOT"' EXIT
GENERATED_DIR="$TEMP_ROOT/SIM_SURFACE_TABLES"

mkdir -p "$GENERATED_DIR"
cp -R "$SOURCE_DIR/." "$GENERATED_DIR/"

SEED_SURFACE_TABLES_OUT_DIR="$GENERATED_DIR" \
  bash "$REPO_ROOT/scripts/seed-surface-tables.sh" >/dev/null

if ! diff -ru "$SOURCE_DIR" "$GENERATED_DIR"; then
  echo "simulator surface tables are stale; run scripts/seed-surface-tables.sh" >&2
  exit 1
fi

echo "simulator surface tables are current"
