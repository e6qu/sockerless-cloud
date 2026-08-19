#!/usr/bin/env bash
# check-fake-tests.sh — hold the repository's tests to failing when the thing
# they name breaks.
#
# scripts/check-fake-tests.go classifies tests that cannot fail, or that can
# pass without exercising what they name. This wrapper turns that report into a
# gate: classes with no instances left are held at zero, and the two classes
# with a standing population carry a floor that may only fall.
#
# The floors are not a tolerance. They are the count on the day the gate landed,
# recorded so the class stays visible while it is burned down; raising one to
# make a red run green re-admits exactly the defect the gate exists to keep out.
# Lower them as instances are fixed.

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

# The Go modules holding tests. testdata/ holds workload programs rather than
# suites, and ui/ is TypeScript with its own gates.
readonly SCAN_DIRS=(
	simulator-aws
	simulator-gcp
	simulator-azure
	realexec
	testutil
	ui-auth
)

# Held at zero: every instance of these was either fixed or shown to be a
# detector defect. A single new one fails the gate.
readonly ZERO_CLASSES=(
	empty-table
	self-compare
	trivial-eventually
	empty-subtest
	fatal-in-goroutine
	any-error
)

# no-assertion counts the six TestMain entry points plus the terraform and
# container-reaper harness mains, which assert nothing because they are
# harnesses rather than tests. Any growth is a real test that asserts nothing.
readonly NO_ASSERTION_FLOOR=10

# any-error is at zero and stays there. It began at 62 error-path assertions
# that accepted any error at all — a transport fault, a 500 and a
# deserialisation failure all satisfied them, and none showed the service
# refusing anything. Every one now names the code its handler returns, read out
# of that handler rather than guessed: ObjectNotFoundException for a scaling
# policy with no target, ActiveInstanceRefreshNotFound for a cancel with no
# refresh, PopReceiptMismatch for a superseded Azure queue receipt.
#
# Two of the 62 were the detector's fault rather than the tests': a message read
# through strings.Contains(err.Error(), …) identifies a refusal as surely as
# ErrorContains does, and a helper that hands its error back to its caller has
# moved the obligation rather than dodged it. Both are understood by the
# analyzer now, and a caller that ignores what such a helper returns is reported
# in the helper's place.

# status-only counts a 2xx whose body is never read. The ones left are handlers
# that answer with no body at all — an authentication redirect, a wrapper
# claiming a host-addressed request — where there is nothing to read.
readonly STATUS_ONLY_FLOOR=9

# sleep-then-assert counts a bare sleep standing between an action and the
# assertion that reads its result. The ones left are sleeps that separate
# timestamps or hold still to prove nothing further arrives, each paired with a
# positive control.
readonly SLEEP_THEN_ASSERT_FLOOR=9

report=$(mktemp)
totals=$(mktemp)
trap 'rm -f "$report" "$totals"' EXIT

# The analyzer's own failure must be visible: redirecting both streams into
# temporary files the trap deletes made a build error exit 1 with nothing
# printed at all, which is a gate that fails for a reason nobody can read.
if ! go run scripts/check-fake-tests.go "${SCAN_DIRS[@]}" >"$report" 2>"$totals"; then
	echo "check-fake-tests: the analyzer did not run:" >&2
	cat "$totals" >&2
	exit 1
fi

count_of() { grep -c "^$1 " "$report" || true; }

fail=0
for class in "${ZERO_CLASSES[@]}"; do
	found=$(count_of "$class")
	if ((found > 0)); then
		echo "check-fake-tests: $found $class finding(s); this class is held at zero:" >&2
		grep "^$class " "$report" >&2
		fail=1
	fi
done

check_floor() {
	local class=$1 floor=$2 found
	found=$(count_of "$class")
	if ((found > floor)); then
		echo "check-fake-tests: $found $class finding(s), above the floor of $floor." >&2
		echo "  Fix the new one rather than raising the floor — the floor records what is" >&2
		echo "  left to burn down, not how much of this is acceptable." >&2
		grep "^$class " "$report" >&2
		fail=1
	elif ((found < floor)); then
		echo "check-fake-tests: $found $class finding(s), below the floor of $floor." >&2
		echo "  Lower ${class//-/_} floor in scripts/check-fake-tests.sh to $found so the" >&2
		echo "  progress is held." >&2
		fail=1
	fi
}

check_floor no-assertion "$NO_ASSERTION_FLOOR"
check_floor status-only "$STATUS_ONLY_FLOOR"
check_floor sleep-then-assert "$SLEEP_THEN_ASSERT_FLOOR"

if ((fail != 0)); then
	exit 1
fi

echo "check-fake-tests: no test can pass without exercising what it names"
sed 's/^/  /' "$totals"
