#!/usr/bin/env bash
# check-installable-build.sh — build every simulator the way `go install` does.
#
# `go install github.com/e6qu/sockerless-cloud/simulator-<cloud>@<tag>` resolves
# the support modules from their pinned versions, not from the repo-root
# workspace. Every SDK harness builds its simulator the same way, with
# GOWORK=off. A local `go build` uses the workspace and sees the working tree,
# so a support module that gained an exported symbol builds locally and fails
# for anyone installing the binary.
#
# That is not hypothetical: all three simulators referenced
# uiauth.Config.MonitoringToken while pinning a ui-auth version that predated
# it, and every workspace build passed while every install was broken.
#
# The dependency freshness gate cannot catch this. A module this repository
# publishes carries no per-module tag, so its pin is the pseudo-version of a
# release commit — which always sorts below the deleted-but-proxy-cached
# bootstrap tags, making every correct pin look like a downgrade. That check is
# skipped for self-published modules, and this gate is what replaces it.

set -euo pipefail

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
	REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$REPO_ROOT"

failed=0
for cloud in aws gcp azure; do
	module="simulator-${cloud}"
	[[ -d $module ]] || continue
	if output=$(cd "$module" && GOWORK=off CGO_ENABLED=0 go build -tags noui -o /dev/null . 2>&1); then
		echo "check-installable-build: ${module} builds from its pinned modules"
		continue
	fi
	echo "check-installable-build: ${module} does not build with GOWORK=off." >&2
	echo "  This is how \`go install .../${module}@<tag>\` and every SDK harness build it." >&2
	echo "  A support module's working-tree state is invisible here: bump the pin in" >&2
	echo "  ${module}/go.mod to a version that carries the symbol." >&2
	echo "$output" >&2
	failed=1
done

exit "$failed"
