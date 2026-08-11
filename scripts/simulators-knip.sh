#!/usr/bin/env bash
# Run knip (TypeScript dead-exports / unused-files / unlisted-deps checker) on
# the simulator dashboard UI packages (simulator-aws/gcp/azure).
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
fail=0
for cloud in aws gcp azure; do
  out=$(cd "$ROOT/ui/packages/simulator-$cloud" && npx knip 2>&1)
  # knip exits 0 on success; strip the Node deprecation noise before checking
  filtered=$(echo "$out" | grep -v "DeprecationWarning\|module.register\|trace-deprecation\|registerHooks\|node:process" || true)
  if [[ -n "$filtered" ]]; then
    echo "FAIL: simulator-$cloud knip found dead exports/files/unlisted deps:" >&2
    echo "$filtered" >&2
    fail=1
  else
    echo "simulators-knip: $cloud OK"
  fi
done
exit "$fail"
