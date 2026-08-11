#!/usr/bin/env bash
# Bounded, retrying `apt-get update` for CI.
#
# GitHub-hosted runners' default apt mirror (azure.archive.ubuntu.com)
# intermittently stalls mid-index-fetch: the connection establishes but
# the Packages indices never finish downloading, so the step hangs until
# the job timeout kills it. The per-request `Acquire::http::Timeout`
# helps apt give up on one dead mirror and fall back to another, but a
# second-level stall on the fallback still hangs the whole `update`.
#
# `apt-get update` only writes to /var/lib/apt/lists (a kill leaves
# partial files there that the next run cleans up) — it runs NO dpkg
# transaction, so it is safe to wrap in a kill-`timeout` and retry. That
# is the crucial difference from `apt-get install`/`upgrade`, where a
# mid-transaction kill corrupts the dpkg database (so those stay
# unguarded, protected only by the `Acquire` options).
set -eu

for attempt in 1 2 3; do
	if sudo timeout 150 apt-get \
		-o Acquire::Retries=3 \
		-o Acquire::http::Timeout=30 \
		-o Acquire::https::Timeout=30 \
		update; then
		exit 0
	fi
	echo "::warning::apt-get update attempt ${attempt} stalled or failed; retrying"
	sleep 5
done

disabled=0
for source in /etc/apt/sources.list.d/microsoft-prod.list /etc/apt/sources.list.d/azure-cli.sources /etc/apt/sources.list.d/azure-cli.list; do
	if [ -f "$source" ]; then
		sudo mv "$source" "$source.disabled-by-sockerless-ci"
		disabled=1
	fi
done

if [ "$disabled" = 1 ]; then
	echo "::warning::apt-get update failed with runner-provided Microsoft apt sources enabled; retrying with those third-party sources disabled"
	if sudo timeout 150 apt-get \
		-o Acquire::Retries=3 \
		-o Acquire::http::Timeout=30 \
		-o Acquire::https::Timeout=30 \
		update; then
		exit 0
	fi
fi

echo "::error::apt-get update failed after 3 attempts (apt mirror degraded)"
exit 1
