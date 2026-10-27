#!/usr/bin/env bash
# check-console-query-client.sh — every console query client is a
# ConsoleQueryClient.
#
# A plain QueryClient joins an invalidation onto a fetch still in flight when
# the query has no data yet, so a create that lands during a list's first load
# leaves the list without the resource it just created.
# ConsoleQueryClient (ui/packages/core/src/query) cancels that fetch first.

set -euo pipefail

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
	REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$REPO_ROOT"

findings=$(git grep -nE 'new QueryClient\(' -- 'ui/packages/*.ts' 'ui/packages/*.tsx' || true)
if [[ -n $findings ]]; then
	echo "check-console-query-client: construct a ConsoleQueryClient from @sockerless/ui-core/query instead:" >&2
	echo "$findings" >&2
	exit 1
fi
