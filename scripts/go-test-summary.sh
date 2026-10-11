#!/usr/bin/env bash
# go-test-summary.sh — run `go test -v` and, when it fails, end the output with
# each failing test's own lines.
#
# A suite's verbose output runs to megabytes and its parallel tests interleave,
# so a log reader that sees only the tail sees a bare FAIL. The summary groups
# the indented lines `go test -v` prints for each test (t.Log, t.Error,
# testify's assertion blocks) under the test that printed them, following the
# `=== RUN`, `=== CONT` and `=== NAME` markers that say whose output comes next.
#
# Usage: scripts/go-test-summary.sh <go test arguments...>
set -uo pipefail

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

go test -v "$@" 2>&1 | tee "$log"
rc="${PIPESTATUS[0]}"
[ "$rc" = 0 ] && exit 0

echo
echo "Failed:"
awk '
	/^=== (RUN|CONT|NAME|PAUSE) / { current = $3; next }
	/^[[:space:]]*--- FAIL: / {
		name = $3
		print
		if (name in out) printf "%s", out[name]
		next
	}
	/^panic:/ { print; panicking = 1; next }
	panicking { print; next }
	/^    / && current != "" { out[current] = out[current] $0 "\n" }
' "$log"
exit "$rc"
