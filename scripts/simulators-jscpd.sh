#!/usr/bin/env bash
# Run jscpd (JavaScript/TypeScript copy-paste detector) on the simulator
# dashboard UI sources (simulator-aws/gcp/azure). Threshold: 200 tokens; test
# files excluded from the count.
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
fail=0
for cloud in aws gcp azure; do
  set +e
  out=$(cd "$ROOT/ui/packages/simulator-$cloud" && npx --yes jscpd \
    --min-tokens 200 \
    --ignore-pattern "src/__tests__/**" \
    --reporters "console" \
    src 2>&1)
  status=$?
  set -e
  if echo "$out" | grep -q "^Clone found"; then
    count=$(echo "$out" | grep -c "^Clone found" || true)
    echo "FAIL: simulator-$cloud jscpd found $count clone(s) above threshold (200 tokens):" >&2
    echo "$out" >&2
    fail=1
  elif [ "$status" -ne 0 ]; then
    echo "FAIL: simulator-$cloud jscpd exited with status $status:" >&2
    echo "$out" >&2
    fail=1
  else
    echo "simulators-jscpd: $cloud OK (threshold: 200 tokens)"
  fi
done
exit "$fail"
