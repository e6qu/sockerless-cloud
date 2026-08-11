#!/usr/bin/env bash
# Run dupl (Go copy-paste detector) on the cloud simulator sources.
# Threshold: 200 tokens (irreducible structural HTTP-handler clones below this
# — one threshold for every simulator). Each simulator (aws/gcp/azure) is scanned
# over its top-level handwritten, non-test Go files (the cloud-API handler
# surface). Generated tables are compile/conformance-tested and intentionally
# repeat model-derived structures, so *_gen.go is excluded from clone analysis.
#
# File names are fed to dupl via its `-files` stdin interface (one per line).
# This is the documented scripting interface; passing a long list of paths as
# positional arguments makes dupl mis-parse them as a single path
# ("file name too long") and silently scan nothing.
#
# Requires github.com/mibk/dupl in $PATH or ~/go/bin.
set -euo pipefail

DUPL=$(command -v dupl 2>/dev/null || echo "$HOME/go/bin/dupl")
if [[ ! -x "$DUPL" ]]; then
  echo "simulators-dupl: dupl not found; install with:" >&2
  echo "  go install github.com/mibk/dupl@latest" >&2
  exit 1
fi

ROOT=$(git rev-parse --show-toplevel)
fail=0
for cloud in aws gcp azure; do
  dir="$ROOT/simulator-$cloud"
  out=$(find "$dir" -maxdepth 1 -name "*.go" ! -name "*_test.go" ! -name "*_gen.go" -type f | sort | "$DUPL" -t 200 -files 2>&1)
  count=$(echo "$out" | grep -c "^found" || true)
  if [[ "$count" -gt 0 ]]; then
    echo "FAIL: simulator-$cloud dupl found $count clone group(s) above threshold (200 tokens):" >&2
    echo "$out" >&2
    fail=1
  else
    echo "simulators-dupl: $cloud OK (threshold: 200 tokens)"
  fi
done
exit "$fail"
