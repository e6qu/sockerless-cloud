#!/usr/bin/env bash
# go-test-summary.sh — run `go test -v` and, when it fails, end the output with
# each failing test's own lines (scripts/go-test-failure-summary.sh).
#
# Usage: scripts/go-test-summary.sh <go test arguments...>
set -uo pipefail

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

go test -v "$@" 2>&1 | tee "$log"
rc="${PIPESTATUS[0]}"
[ "$rc" = 0 ] && exit 0

bash "$(dirname "${BASH_SOURCE[0]}")/go-test-failure-summary.sh" "$log"
exit "$rc"
