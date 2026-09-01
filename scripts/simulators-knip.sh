#!/usr/bin/env bash
# Run knip (TypeScript dead-exports / unused-files / unlisted-deps checker) on
# the simulator dashboard UI packages (simulator-aws/gcp/azure).
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
fail=0
for cloud in aws gcp azure; do
  # `out=$(...)` under `set -e` aborts the script the moment knip exits
  # non-zero — before any of the reporting below runs. This gate spent a CI run
  # doing exactly that: exit 1, not one line of output, nothing to act on. The
  # status is captured instead, so a failing knip is reported rather than fatal.
  #
  # --no-config-hints because a hint is not a finding. knip prints one for every
  # package here ("Compiled extension excluded by project" for .css), and any
  # output at all reads as failure below, so without this the gate can never
  # pass however clean the code is.
  set +e
  out=$(cd "$ROOT/ui/packages/simulator-$cloud" && npx knip --no-config-hints 2>&1)
  status=$?
  set -e
  # Strip the Node deprecation noise before deciding.
  filtered=$(echo "$out" | grep -v "DeprecationWarning\|module.register\|trace-deprecation\|registerHooks\|node:process" || true)
  if [[ -n "$filtered" ]]; then
    echo "FAIL: simulator-$cloud knip found dead exports/files/unlisted deps:" >&2
    echo "$filtered" >&2
    fail=1
  elif [[ "$status" -ne 0 ]]; then
    # Non-zero with nothing to say is knip itself failing — a bad config, a
    # missing binary — and is not a clean run.
    echo "FAIL: simulator-$cloud knip exited $status without reporting anything; knip itself failed" >&2
    fail=1
  else
    echo "simulators-knip: $cloud OK"
  fi
done
exit "$fail"
