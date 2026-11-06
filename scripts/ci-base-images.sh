#!/usr/bin/env bash
# Load a job's base images (scripts/warm-base-images.sh) while the job does
# other work, and block on them before the suites need them.
#
# Loading the cached tarball takes minutes of a simulator job's budget on an
# ordinary runner and more on a slow one; run in the foreground it delays the
# compile and the tests behind it.
#
# Usage:
#   scripts/ci-base-images.sh start <tarball> <image>...   launch the load in the background
#   scripts/ci-base-images.sh wait                         block until it ends; exit with its status
#
# `start` takes a lock on a file in RUNNER_TEMP that the load holds until it
# ends, so `wait`, and scripts/ci-engine-image.sh's builds, which read the same
# base images, block on the lock rather than polling.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
dir="${RUNNER_TEMP:?RUNNER_TEMP names no directory}"
lock="$dir/base-images.lock"
log="$dir/base-images.log"
exit_file="$dir/base-images.status"

case "${1:-}" in
start)
	shift
	rm -f "$exit_file" "$log"
	exec 9>"$lock"
	flock 9
	(
		if bash "$root/scripts/warm-base-images.sh" "$@"; then
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
	echo "usage: $0 start <tarball> <image>... | wait" >&2
	exit 2
	;;
esac
