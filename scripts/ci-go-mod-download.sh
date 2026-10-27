#!/usr/bin/env bash
# Retry `go mod download` for CI dependency setup.
#
# GitHub-hosted runners occasionally see proxy.golang.org streams reset or stall
# while fetching large module zips. A stalled fetch never returns on its own, so
# each attempt is bounded; retrying the same fetch adds no fallback source.
set -euo pipefail

module_dir="${1:-.}"
# Modules an attempt completed stay in the cache, so a bounded attempt that
# stalls still leaves the next one less to fetch; the calling step's own
# timeout bounds the total.
attempt_seconds=50
attempts=8

for attempt in $(seq "$attempts"); do
	if (cd "$module_dir" && timeout "$attempt_seconds" go mod download); then
		exit 0
	fi
	if [ "$attempt" = "$attempts" ]; then
		break
	fi
	echo "::warning::go mod download failed or stalled past ${attempt_seconds}s in ${module_dir} on attempt ${attempt}; retrying"
	sleep 5
done

echo "::error::go mod download failed in ${module_dir} after ${attempts} attempts"
exit 1
