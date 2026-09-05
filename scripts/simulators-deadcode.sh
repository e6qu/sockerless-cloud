#!/usr/bin/env bash
# Run deadcode on the cloud simulators to detect unreachable functions.
#
# Each simulator (aws/gcp/azure) is its own Go module with a `package main`
# entry point at the module root, so deadcode is run per module with the `noui`
# build tag (so the embedded dist/ build artifact is not required). Findings in
# the module fail the gate.
#
# The framework module `sim` has no main package of its own, so it is judged
# from the three programs that link it: a framework function every simulator
# leaves unreachable is dead, one any simulator reaches is not. The per-module
# runs report the framework's functions too (`-filter` widens the report to it)
# and the gate intersects those reports.
#
# Requires golang.org/x/tools/cmd/deadcode in $PATH or ~/go/bin.
set -euo pipefail

DEADCODE=$(command -v deadcode 2>/dev/null || echo "$HOME/go/bin/deadcode")
if [[ ! -x "$DEADCODE" ]]; then
  echo "simulators-deadcode: deadcode not found; install with:" >&2
  echo "  go install golang.org/x/tools/cmd/deadcode@latest" >&2
  exit 1
fi

ROOT=$(git rev-parse --show-toplevel)
fail=0
sim_reports=()
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
for cloud in aws gcp azure; do
	if ! out=$(cd "$ROOT/simulator-$cloud" && "$DEADCODE" -tags noui -test \
		-filter 'github.com/e6qu/sockerless-cloud/(simulator-'"$cloud"'|sim)$' . 2>&1); then
		echo "FAIL: simulator-$cloud deadcode analysis did not complete:" >&2
		echo "$out" >&2
		fail=1
		continue
	fi
	# The framework's findings name files under the repository's sim/ directory;
	# the module's own name files under simulator-<cloud>/.
	module_findings=$(grep -Ev '(^|/)sim/[^/]+\.go:' <<<"$out" || true)
	grep -E '(^|/)sim/[^/]+\.go:' <<<"$out" | sed -E 's|^.*/sim/|sim/|' | sort >"$work/sim-$cloud" || true
	sim_reports+=("$work/sim-$cloud")
	if [[ -n "$module_findings" ]]; then
		echo "FAIL: simulator-$cloud deadcode found unreachable functions:" >&2
		echo "$module_findings" >&2
		fail=1
	else
		echo "simulators-deadcode: $cloud OK"
	fi
done

if [[ ${#sim_reports[@]} -eq 3 ]]; then
	dead_everywhere=$(comm -12 "${sim_reports[0]}" "${sim_reports[1]}" | comm -12 - "${sim_reports[2]}")
	if [[ -n "$dead_everywhere" ]]; then
		echo "FAIL: sim deadcode found functions no simulator reaches:" >&2
		echo "$dead_everywhere" >&2
		fail=1
	else
		echo "simulators-deadcode: sim OK (judged from all three simulators)"
	fi
fi
exit "$fail"
