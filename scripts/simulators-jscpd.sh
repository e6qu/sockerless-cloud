#!/usr/bin/env bash
# Run jscpd (JavaScript/TypeScript copy-paste detector) on the simulator
# dashboard UI sources (simulator-aws/gcp/azure). Threshold: 200 tokens; test
# files excluded from the count.
#
# The exclusion uses --ignore, which takes file globs. It used to use
# --ignore-pattern, which in jscpd 5 takes code-level regexes for skipping
# tokens, so a file glob handed to it excluded nothing at all. That went
# unnoticed because the clone detection below never matched either: both faults
# were invisible while the gate reported OK on everything.
#
# jscpd is a pinned devDependency of the UI workspace and is invoked from
# node_modules, not through `npx --yes`. It used to be fetched that way, once
# per cloud, and the cost was not the analysis: measured on 2026-09-04 the three
# runs took 5m03s wall-clock for 1.69s of CPU at 0% utilisation — all of it npm
# round-trips. The job that runs this is budgeted at eight minutes and also
# installs deadcode, dupl and the UI dependencies, so on a slow npm day the
# budget went to resolution and GitHub killed the job mid-step, which it reports
# as "cancelled" rather than as a timeout. The workspace install this job
# already performs now provides the binary, so the gate does no network at all.
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
  # Match unanchored. jscpd prints each clone as "Clone found (tsx)" preceded by
  # an ANSI bold escape, so a "^Clone found" anchor never matches a single one —
  # and jscpd exits 0 for clones unless --threshold is given, so this gate could
  # not fail at all. It reported OK on a tree with 539 clones at a 20-token
  # threshold, which is how that was found.
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
