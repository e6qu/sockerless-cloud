#!/usr/bin/env bash
# Run jscpd (JavaScript/TypeScript copy-paste detector) on the simulator
# dashboard UI sources (simulator-aws/gcp/azure). Threshold: 200 tokens; test
# files excluded from the count.
#
# The exclusion uses --ignore, which takes file globs; --ignore-pattern takes
# code-level regexes for skipping tokens, and a file glob handed to it
# excludes nothing.
#
# jscpd is a pinned devDependency of the UI workspace and runs from
# node_modules, not through `npx --yes`: the workspace install this job
# already performs provides the binary, so the gate does no network at all and
# npm round-trips cannot spend the job's time budget.
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
JSCPD="$ROOT/ui/node_modules/.bin/jscpd"
if [[ ! -x "$JSCPD" ]]; then
  echo "simulators-jscpd: $JSCPD is missing — run 'cd ui && bun install' first" >&2
  exit 1
fi
fail=0
for cloud in aws gcp azure; do
  set +e
  out=$(cd "$ROOT/ui/packages/simulator-$cloud" && "$JSCPD" \
    --min-tokens 200 \
    --ignore "**/__tests__/**" \
    --reporters "console" \
    src 2>&1)
  exit_status=$?
  set -e
  # Match unanchored: jscpd prints each clone as "Clone found (tsx)" preceded by
  # an ANSI bold escape, so a "^Clone found" anchor never matches, and jscpd
  # exits 0 for clones unless --threshold is given.
  if echo "$out" | grep -q "Clone found"; then
    count=$(echo "$out" | grep -c "Clone found" || true)
    echo "FAIL: simulator-$cloud jscpd found $count clone(s) above threshold (200 tokens):" >&2
    echo "$out" >&2
    fail=1
  elif [ "$exit_status" -ne 0 ]; then
    echo "FAIL: simulator-$cloud jscpd exited with status $exit_status:" >&2
    echo "$out" >&2
    fail=1
  else
    echo "simulators-jscpd: $cloud OK (threshold: 200 tokens)"
  fi
done
exit "$fail"
