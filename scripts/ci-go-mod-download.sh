#!/usr/bin/env bash
# Retry `go mod download` for CI dependency setup.
#
# Module downloads are read-only cache population. GitHub-hosted runners
# occasionally see transient proxy.golang.org HTTP/2 stream resets while
# fetching large module zips; retrying the same required dependency fetch keeps
# CI truthful without adding a fallback source or skipping tests.
set -euo pipefail

module_dir="${1:-.}"

for attempt in 1 2 3; do
	if (cd "$module_dir" && go mod download); then
		exit 0
	fi
	if [ "$attempt" = 3 ]; then
		break
	fi
	echo "::warning::go mod download failed in ${module_dir} on attempt ${attempt}; retrying"
	sleep 5
done

echo "::error::go mod download failed in ${module_dir} after 3 attempts"
exit 1
