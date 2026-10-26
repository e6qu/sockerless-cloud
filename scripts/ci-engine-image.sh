#!/usr/bin/env bash
# Build an image the simulators build on the runner's engine — the network
# sidecar (`sidecar-image`) or the microVM node agent (`node-agent-image`) —
# while the job does other work, and block on it before the suites need it.
#
# A cold build of either takes longer than a test waits for its first
# workload, so a suite that starts its simulators without the image fails on
# whichever test places first.
#
# Usage:
#   scripts/ci-engine-image.sh <make target> start   launch the build in the background
#   scripts/ci-engine-image.sh <make target> wait    block until it ends; exit with its status
#
# `start` takes a lock on a file in RUNNER_TEMP that the build holds until it
# ends, so `wait` blocks on the lock rather than polling.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
usage="usage: $0 sidecar-image|node-agent-image start|wait"
target="${1:-}"
case "$target" in
sidecar-image | node-agent-image) ;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
dir="${RUNNER_TEMP:?RUNNER_TEMP names no directory}"
lock="$dir/$target.lock"
log="$dir/$target.log"
exit_file="$dir/$target.status"

case "${2:-}" in
start)
	rm -f "$exit_file" "$log"
	exec 9>"$lock"
	flock 9
	# The module download belongs to the build: in the workspace it fetches every
	# module's dependencies, which a cold cache cannot do inside the start step's
	# budget.
	(
		if bash "$root/scripts/ci-go-mod-download.sh" "$root/sim" && make -C "$root" "$target"; then
			echo 0 >"$exit_file"
		else
			echo $? >"$exit_file"
		fi
	) </dev/null >>"$log" 2>&1 &
	;;
wait)
	flock "$lock" true
	cat "$log"
	exit "$(cat "$exit_file")"
	;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac
