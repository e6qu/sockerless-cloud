#!/usr/bin/env bash
# check-azure-cli-shard-coverage.sh
#
# The Azure CLI suite runs as two shards in .github/workflows/ci.yml, selected
# by `run: '^Test[A-M]'` and `run: '^Test[N-Z]'`. A test whose name matches
# neither silently never runs in CI, and one matching both runs twice — and the
# partition is by first letter, so a test named in any other shape (a lower-case
# initial, a digit) falls out of it without anything else noticing.
#
# The AWS suites have the same guard, and each of the three reads only its own
# job's shard regexes: two of the three declare them in the same character-class
# shape, so a gate that read the whole workflow would find another suite's.
#
# Portable: POSIX-ish, runs the same under bash and zsh on macOS and Linux.
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
ci="$repo_root/.github/workflows/ci.yml"
cli_dir="$repo_root/simulator-azure/cli-tests"

regex_file=$(mktemp)
test_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file"' EXIT

# Only the shared `sim` job's shards — the Azure CLI suite is one of its matrix
# entries.
job_block=$(awk '/^  sim:$/{inside=1; next} /^  [a-z][a-z0-9-]*:$/{inside=0} inside' "$ci")
grep -oE "run: '\^Test\[[^']*'" <<<"$job_block" | sed -E "s/^run: '//; s/'\$//" > "$regex_file"
if [ ! -s "$regex_file" ]; then
  echo "FAIL: no Azure CLI shard regexes (run: '^Test[...]') found in the sim job of $ci" >&2
  exit 1
fi

# Every Azure CLI test function (TestMain is the harness entrypoint, not a test).
grep -hoE '^func Test[A-Za-z0-9_]+\(' "$cli_dir"/*.go \
  | sed -E 's/^func //; s/\($//' | sort -u | grep -v '^TestMain$' > "$test_file"

orphans=0
doubles=0
n_tests=0
while IFS= read -r t; do
  [ -n "$t" ] || continue
  n_tests=$((n_tests + 1))
  matches=0
  while IFS= read -r re; do
    [ -n "$re" ] || continue
    if printf '%s\n' "$t" | grep -qE "$re"; then
      matches=$((matches + 1))
    fi
  done < "$regex_file"
  if [ "$matches" -eq 0 ]; then
    echo "FAIL: Azure CLI test $t matches NO shard regex — it never runs in CI" >&2
    orphans=$((orphans + 1))
  elif [ "$matches" -gt 1 ]; then
    echo "FAIL: Azure CLI test $t matches $matches shard regexes — runs more than once" >&2
    doubles=$((doubles + 1))
  fi
done < "$test_file"

n_shards=$(grep -c . "$regex_file")
if [ "$orphans" -ne 0 ] || [ "$doubles" -ne 0 ]; then
  echo "azure-cli-shard-coverage: $orphans orphan(s), $doubles double(s) across $n_tests tests / $n_shards shards." >&2
  echo "Fix the shard 'run' regexes in $ci so each test matches exactly one shard." >&2
  exit 1
fi

echo "azure-cli-shard-coverage: ok ($n_tests tests across $n_shards shards, each covered once)"
