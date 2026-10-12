#!/usr/bin/env bash
# go-test-failure-summary.sh — print each failing test's own lines from a
# `go test -v` log.
#
# A suite's verbose output runs to megabytes and its parallel tests interleave,
# so a log reader that sees only the tail sees a bare FAIL. The summary groups
# the indented lines `go test -v` prints for each test (t.Log, t.Error,
# testify's assertion blocks) under the test that printed them, following the
# `=== RUN`, `=== CONT`, `=== NAME` and `=== PAUSE` markers that say whose
# output comes next, and prints a panic whole.
#
# Usage: scripts/go-test-failure-summary.sh <logfile>
set -euo pipefail

if [ "$#" -ne 1 ]; then
	echo "usage: $0 <logfile>" >&2
	exit 2
fi

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
' "$1"
