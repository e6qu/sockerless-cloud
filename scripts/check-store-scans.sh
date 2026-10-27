#!/usr/bin/env bash
# check-store-scans.sh — hold the number of full store reads on a path that
# every request pays to a count that may only fall.
#
# A List decodes every row it holds, so a full store read on a path every
# request pays makes each request cost the whole store: a proxied request that
# decoded every stored Amazon ECS task to find one target's address capped the
# data plane at the guest's vCPU count. Profiles of live deployments find these
# one at a time; this counts the shape instead.
#
# The fix is an index keyed by the store's Generation: see
# sim/index.go for the shared implementation, and
# elbv2LoadBalancerFromDataPlaneHost or ecsRunningTaskENIs for a call site.
# Their tests count reads of the store rather than timing anything.

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
	sim
	simulator-aws
	simulator-gcp
	simulator-azure
	testutil
	ui-auth
)

# The analyzer does not count a scan whose function also reads the same
# store's Generation(): that is the amortized rebuild of a generation-keyed
# index, the fix itself.
#
# The floor is zero. A new scan on a request path is a regression, not a
# candidate for an exemption paragraph here: every exemption this file ever
# recorded turned out to be a keyed lookup on a second reading.
readonly STORE_SCAN_FLOOR=0

report=$(mktemp)
diag=$(mktemp)
trap 'rm -f "$report" "$diag"' EXIT

if ! go run scripts/check-store-scans.go "${SCAN_DIRS[@]}" >"$report" 2>"$diag"; then
	echo "check-store-scans: the analyzer did not run:" >&2
	cat "$diag" >&2
	exit 1
fi

found=$(grep -c '^store-scan ' "$report" || true)

if ((found > STORE_SCAN_FLOOR)); then
	echo "check-store-scans: $found full store reads on request paths, above the floor of $STORE_SCAN_FLOOR." >&2
	echo "  A List() or Filter() on one of these paths decodes every row the store" >&2
	echo "  holds, for every request. Answer it from a GenerationIndex instead." >&2
	cat "$report" >&2
	exit 1
fi

if ((found < STORE_SCAN_FLOOR)); then
	echo "check-store-scans: $found findings, below the floor of $STORE_SCAN_FLOOR." >&2
	echo "  Lower STORE_SCAN_FLOOR in scripts/check-store-scans.sh to $found so the progress is held." >&2
	exit 1
fi

echo "check-store-scans: $found full store reads on request paths (floor $STORE_SCAN_FLOOR)"
