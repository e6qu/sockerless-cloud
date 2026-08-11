#!/usr/bin/env bash
# Run deadcode on the cloud simulators to detect unreachable functions.
# Each simulator (aws/gcp/azure) is its own Go module with a `package main`
# entry point at the module root, so deadcode is run per-module with the
# `noui` build tag (so the embedded dist/ build artifact is not required).
#
# Scope: findings in the simulator package itself. The per-cloud shared/
# framework package is excluded from gating for now — it entered the module
# (and therefore deadcode's same-module reporting set) when the simulators
# moved to this repository, carrying cross-cloud helpers that each diverged
# copy retains without using. Gating shared/ requires a Linux-verified
# cleanup pass (reachability differs by GOOS and build tags such as
# realexec_host); tracked in BUGS.md.
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
for cloud in aws gcp azure; do
	if ! out=$(cd "$ROOT/simulator-$cloud" && "$DEADCODE" -tags noui -test . 2>&1); then
		echo "FAIL: simulator-$cloud deadcode analysis did not complete:" >&2
		echo "$out" >&2
		fail=1
		continue
	fi
	out=$(printf '%s\n' "$out" | grep -v '^shared/' || true)
	if [[ -n "$out" ]]; then
    echo "FAIL: simulator-$cloud deadcode found unreachable functions:" >&2
    echo "$out" >&2
    fail=1
  else
    echo "simulators-deadcode: $cloud OK"
  fi
done
exit "$fail"
