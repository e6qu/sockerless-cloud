#!/usr/bin/env bash
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
build_recipe=$(awk '
  /^build:/ { in_build = 1; next }
  in_build && /^[^[:space:]]/ { exit }
  in_build { print }
' "$root/Makefile")

required="APPS=\"\$(UI_APPS) \$(GO_UI_APPS) \$(GO_APPS)\""
if [[ "$build_recipe" != *"$required"* ]]; then
	echo "root make build must build UI_APPS before the Go binaries that embed them" >&2
	exit 1
fi

echo "root production build creates embedded web interfaces before Go binaries"
