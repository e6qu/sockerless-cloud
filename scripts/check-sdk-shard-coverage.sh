#!/usr/bin/env bash
# check-sdk-shard-coverage.sh
#
# The AWS SDK test job in .github/workflows/ci.yml fans out across parallel
# shards, each selected by a `-test.run` regex — either the character-class
# form `run: '^Test[...]'` or the alternation form `run: '^Test(...)'`. Both
# are read, because a shard boundary that falls inside a name rather than on
# its first letter (Amazon ECS split away from the rest of the E tests, to
# even out the two shards that were running against the job budget) cannot be
# written as a character class. Reading only this job's block is what keeps
# another matrix's regexes out; the shape is not what distinguishes them.
# A test whose name matches NO shard regex silently never runs in CI; one
# matching MORE THAN ONE runs twice. This guard asserts every AWS SDK test
# function is covered by exactly one shard regex.
#
# Portable: POSIX-ish, runs the same under bash and zsh on macOS and Linux.
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
ci="$repo_root/.github/workflows/ci.yml"
sdk_dir="$repo_root/simulator-aws/sdk-tests"

regex_file=$(mktemp)
test_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file"' EXIT

# Every shard regex declared in this job, in either form. Only this job's
# shards: another job's matrix declares regexes in the same shapes — the Azure
# CLI suite does — and reading those as this suite's would make every test here
# match several shards at once.
# A matrix entry with a `placement:` runs its tests a second time on another
# backend, by design; its regex is not a shard of the partition, so it is
# kept apart and checked only for naming tests that exist.
job_block=$(awk '/^  sim-aws-sdk:$/{inside=1; next} /^  [a-z][a-z0-9-]*:$/{inside=0} inside' "$ci")
placement_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file" "$placement_file"' EXIT
awk '
  function flush() { if (run != "") print (placement != "" ? "placement" : "shard") "\t" run }
  /^ *steps:/      { flush(); exit }
  /^ *- suite:/    { flush(); run=""; placement=""; next }
  /^ *run:/        { run=$0; sub(/^ *run: /, "", run); gsub(/^\047|\047$/, "", run); next }
  /^ *placement:/  { placement=$2; next }
' <<<"$job_block" > "$placement_file"
awk -F'\t' '$1 == "shard" { print $2 }' "$placement_file" > "$regex_file"
if [ ! -s "$regex_file" ]; then
  echo "FAIL: no AWS SDK shard regexes (run: '^Test[...]' or '^Test(...)') found in $ci" >&2
  exit 1
fi

# Every AWS SDK test function (TestMain is the harness entrypoint, not a test).
grep -hoE '^func Test[A-Za-z0-9_]+\(' "$sdk_dir"/*.go \
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
    echo "FAIL: SDK test $t matches NO shard regex — it never runs in CI" >&2
    orphans=$((orphans + 1))
  elif [ "$matches" -gt 1 ]; then
    echo "FAIL: SDK test $t matches $matches shard regexes — runs more than once" >&2
    doubles=$((doubles + 1))
  fi
done < "$test_file"

empty=0
while IFS= read -r re; do
  [ -n "$re" ] || continue
  if ! grep -qE "$re" "$test_file"; then
    echo "FAIL: placement shard regex $re matches no SDK test" >&2
    empty=$((empty + 1))
  fi
done < <(awk -F'\t' '$1 == "placement" { print $2 }' "$placement_file")

n_shards=$(grep -c . "$regex_file")
if [ "$orphans" -ne 0 ] || [ "$doubles" -ne 0 ] || [ "$empty" -ne 0 ]; then
  echo "sdk-shard-coverage: $orphans orphan(s), $doubles double(s), $empty empty placement shard(s) across $n_tests tests / $n_shards shards." >&2
  echo "Fix the shard 'run' regexes in $ci so each test matches exactly one shard." >&2
  exit 1
fi

echo "sdk-shard-coverage: ok ($n_tests tests across $n_shards shards, each covered once)"
