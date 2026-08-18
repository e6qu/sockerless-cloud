#!/usr/bin/env bash
# check-readonly-locks.sh — hold the number of read-only critical sections that
# take an exclusive lock to a count that may only fall.
#
# The same defect arrived three times from one service in two days: a Query
# that read the whole table under a per-item lock (#37), a Scan copying under
# one acquisition per item (#39), and every single-item read queueing behind
# every other operation because the store's lock was exclusive (#43). Each was
# found by a user watching a page time out. They are one shape — a lock taken
# for reading, so a service's read concurrency is one — and the shape is what
# this counts.
#
# Converting a service is not a mechanical edit and must not be done as one.
# Every site holding that lock has to be classified first: a section that reads
# and then writes based on what it read is a lost update under RLock, not a
# faster read, and neither lock is reentrant. Amazon DynamoDB is the worked
# example — see ddbItemsMu in simulator-aws/dynamodb.go for the contract, and
# TestDDBReadsRunConcurrently for how the result is measured rather than timed.

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
)

# The count on the day this landed, with Amazon DynamoDB's item store already
# converted. It is what is left to audit, not what is acceptable: raising it to
# make a run green re-admits the defect this exists to keep out. Lower it as
# services are converted.
readonly READONLY_LOCK_FLOOR=32

report=$(mktemp)
totals=$(mktemp)
trap 'rm -f "$report" "$totals"' EXIT

if ! go run scripts/check-readonly-locks.go "${SCAN_DIRS[@]}" >"$report" 2>"$totals"; then
	echo "check-readonly-locks: the analyzer did not run:" >&2
	cat "$totals" >&2
	exit 1
fi

found=$(grep -c '^readonly-lock ' "$report" || true)

if ((found > READONLY_LOCK_FLOOR)); then
	echo "check-readonly-locks: $found read-only critical sections hold an exclusive lock, above the floor of $READONLY_LOCK_FLOOR." >&2
	echo "  A read path that takes Lock excludes every other reader for no reason." >&2
	echo "  Classify the lock's sites and give the read paths RLock, or say in a" >&2
	echo "  comment which later write the section is being atomic with." >&2
	cat "$report" >&2
	exit 1
fi

if ((found < READONLY_LOCK_FLOOR)); then
	echo "check-readonly-locks: $found findings, below the floor of $READONLY_LOCK_FLOOR." >&2
	echo "  Lower READONLY_LOCK_FLOOR in scripts/check-readonly-locks.sh to $found so the progress is held." >&2
	exit 1
fi

echo "check-readonly-locks: $found read-only critical sections still hold an exclusive lock (floor $READONLY_LOCK_FLOOR)"
