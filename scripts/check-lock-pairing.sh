#!/usr/bin/env bash
# check-lock-pairing.sh — fail on a read-write lock whose acquire and release do
# not match: an RLock released with Unlock, or a Lock released with RUnlock.
#
# sync.RWMutex answers a mismatch with `fatal error: sync: Unlock of unlocked
# RWMutex`. That is not a recoverable panic — it takes the process down, so in a
# simulator the visible failure is not the one handler that got it wrong but
# every test that runs afterwards, each reporting `connection refused` against a
# port nothing is listening on any more. Converting Amazon Lambda's durable
# executions to read locks left four Unlock calls behind, and the first request
# to reach one of them killed the simulator mid-suite.
#
# Neither the compiler nor `go vet` sees it: both calls are real methods on a
# real receiver, and only executing the path proves the pair wrong. So this is a
# floor of zero rather than a ratchet — there is no such thing as an acceptable
# count of these, and a new one is a crash waiting for the first caller.

set -euo pipefail

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
	script_path=$0
	case "$script_path" in
	*/*) ;;
	*) script_path="./$script_path" ;;
	esac
	REPO_ROOT="$(cd "$(dirname "$script_path")/.." && pwd)"
fi
cd "$REPO_ROOT"

readonly SCAN_DIRS=(
	simulator-aws
	simulator-gcp
	simulator-azure
	realexec
	testutil
	ui-auth
	scripts
)

report=$(mktemp)
diag=$(mktemp)
trap 'rm -f "$report" "$diag"' EXIT

if ! go run scripts/check-lock-pairing.go "${SCAN_DIRS[@]}" >"$report" 2>"$diag"; then
	echo "check-lock-pairing: the analyzer did not run:" >&2
	cat "$diag" >&2
	exit 1
fi

found=$(grep -c '^lock-pairing ' "$report" || true)

if ((found > 0)); then
	echo "check-lock-pairing: $found read-write locks are released with the wrong call." >&2
	echo "  A mismatched release is a process-wide fatal error, not a failed request:" >&2
	echo "  every later test sees the simulator's port refuse connections instead." >&2
	echo "  Release RLock with RUnlock and Lock with Unlock." >&2
	cat "$report" >&2
	exit 1
fi

echo "check-lock-pairing: every read-write lock is released by its matching call"
