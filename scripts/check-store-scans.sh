#!/usr/bin/env bash
# check-store-scans.sh — hold the number of full store reads on a path that
# every request pays to a count that may only fall.
#
# A CPU profile of the deployed AWS simulator, taken while twelve concurrent
# requests were in flight against one load-balanced application, attributed
# 84.8% of all its CPU to one function and 99.7% of that to a single
# `ecsTasks.List()` — JSON-decoding every stored Amazon ECS task, stopped ones
# included, once per proxied request, to find the one holding a target's ENI
# address. The guest has two vCPUs, so the data plane ran at an effective
# concurrency of two and the symptom was thirty-second browser timeouts rather
# than ordinary slowness.
#
# It was reported as one function's problem. It was a class: the load
# balancer's own hostname match had the identical shape on a hotter path — a
# handler wrapper, so every request into the simulator paid it before any
# handler ran, an Amazon DynamoDB call as much as a proxied page load — and it
# was invisible only because a deployment holds a handful of load balancers
# against a few hundred tasks. Nothing but a profile of a live deployment would
# have found either, which is why this counts the shape instead.
#
# The fix is an index keyed by the store's Generation: see
# simulator-*/shared/index.go for the shared implementation, and
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
	simulator-aws
	simulator-gcp
	simulator-azure
	realexec
	testutil
	ui-auth
)

# What the floor holds, and what it no longer counts.
#
# The analyzer stopped counting a scan whose function also reads the same
# store's Generation() — that is the amortized rebuild of a generation-keyed
# index, the fix itself, and counting it meant the floor could never reach
# zero however completely the class was converted.
#
# Every single-row-by-stable-key lookup on a request path is converted: the
# handler wrappers, the AWS WAF ARN resolutions (web ACL, IP set, regex set,
# rule group), the Azure storage-account-by-name lookups behind the storage
# data plane's authorization, the resource-group and Service Bus namespace
# resolutions, and the managed-identity principal check.
#
# What remains is not that class. It is, by inspection: List operations whose
# response is the collection (Key Vault and Service Bus admin listings, role
# assignments, deleted shares); bulk mutations over a parent's children (share
# deletion, entry moves, table batch snapshot/restore); fan-outs that visit
# every row by design (Event Grid delivery, CloudTrail trails); joins over
# small stores (load-balancer and Application Gateway NIC pools, Route 53
# zones); and the ACME scans, which reconcile each row as they read it and so
# cannot answer from an index without changing what a read means. Convert one
# faithfully and lower the floor; none of them is a one-row lookup paid per
# request any more.
readonly STORE_SCAN_FLOOR=46

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
