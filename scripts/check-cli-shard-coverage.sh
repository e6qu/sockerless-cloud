#!/usr/bin/env bash
# check-cli-shard-coverage.sh
#
# The AWS CLI test job in .github/workflows/ci.yml fans out across parallel
# shards, each selected by a `-run '^Test(...)'` regex. A test whose name
# matches NO shard regex is silently never run in CI; one matching MORE THAN
# ONE shard wastes runner time and can mask ordering bugs. This guard asserts
# every AWS CLI test function is covered by exactly one shard regex, so adding a
# test for a new service can't slip through unrun.
#
# Portable: POSIX-ish, runs the same under bash and zsh on macOS and Linux
# (no mapfile, no BASH_SOURCE, no GNU/BSD-only flags).
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
ci="$repo_root/.github/workflows/ci.yml"
cli_dir="$repo_root/simulator-aws/cli-tests"

regex_file=$(mktemp)
test_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file"' EXIT

# Every `run: '^Test(...)'` shard regex declared in the workflow.
# Only this job's shards. Another job's matrix may declare regexes in the
# same shape — the Azure CLI suite does — and reading those as this suite's
# would make every test here match several shards at once.
job_block=$(awk '/^  sim-aws-cli:$/{inside=1; next} /^  [a-z][a-z0-9-]*:$/{inside=0} inside' "$ci")
grep -oE "run: '\^Test\([^']*\)'" <<<"$job_block" | sed -E "s/^run: '//; s/'\$//" > "$regex_file"
if [ ! -s "$regex_file" ]; then
  echo "FAIL: no AWS CLI shard regexes (run: '^Test(...)') found in $ci" >&2
  exit 1
fi

# Every AWS CLI test function (TestMain is the harness entrypoint, not a test).
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
    echo "FAIL: CLI test $t matches NO shard regex — it never runs in CI" >&2
    orphans=$((orphans + 1))
  elif [ "$matches" -gt 1 ]; then
    echo "FAIL: CLI test $t matches $matches shard regexes — runs more than once" >&2
    doubles=$((doubles + 1))
  fi
done < "$test_file"

n_shards=$(grep -c . "$regex_file")
if [ "$orphans" -ne 0 ] || [ "$doubles" -ne 0 ]; then
  echo "cli-shard-coverage: $orphans orphan(s), $doubles double(s) across $n_tests tests / $n_shards shards." >&2
  echo "Fix the shard 'run' regexes in $ci so each test matches exactly one shard." >&2
  exit 1
fi

echo "cli-shard-coverage: ok ($n_tests tests across $n_shards shards, each covered once)"
