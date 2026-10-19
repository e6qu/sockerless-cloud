#!/usr/bin/env bash
# check-azure-sdk-shard-coverage.sh
#
# The Azure SDK suite runs as two shards in .github/workflows/ci.yml, selected
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
sdk_dir="$repo_root/simulator-azure/sdk-tests"

regex_file=$(mktemp)
test_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file"' EXIT

# Only the shared `sim` job's Azure SDK shards. Two suites in that job are
# sharded now, so selecting on the regex shape alone would pull in the SDK
# suite's — the awk below tracks which matrix entry each `run:` belongs to and
# keeps only the ones under `cloud: azure` + `suite: cli`.
job_block=$(awk '/^  sim:$/{inside=1; next} /^  [a-z][a-z0-9-]*:$/{inside=0} inside' "$ci")
# Each line is a shard: its run regex, a tab, and its skip regex (empty when the
# shard skips nothing). A test belongs to a shard when run matches and skip does not.
# A matrix entry with a `placement:` runs its tests a second time on another
# backend, by design; its regex is not a shard of the partition, so it is
# kept apart and checked only for naming tests that exist.
placement_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file" "$placement_file"' EXIT
awk '
  function flush() { if (cloud == "azure" && suite == "sdk" && run != "") print (placement != "" ? "placement" : "shard") "\t" run "\t" skip }
  /^ *steps:/     { flush(); exit }
  /^ *- cloud:/   { flush(); cloud=$3; suite=""; run=""; skip=""; placement=""; next }
  /^ *suite:/     { suite=$2; next }
  /^ *run:/       { run=$0; sub(/^ *run: /, "", run); gsub(/^\047|\047$/, "", run); next }
  /^ *skip:/      { skip=$0; sub(/^ *skip: /, "", skip); gsub(/^\047|\047$/, "", skip); next }
  /^ *placement:/ { placement=$2; next }
' <<<"$job_block" > "$placement_file"
awk -F'\t' '$1 == "shard" { print $2 "\t" $3 }' "$placement_file" > "$regex_file"
if [ ! -s "$regex_file" ]; then
  echo "FAIL: no Azure SDK shard regexes (run: '^Test[...]') found in the sim job of $ci" >&2
  exit 1
fi

# Every Azure SDK test function (TestMain is the harness entrypoint, not a test).
grep -hoE '^func Test[A-Za-z0-9_]+\(' "$sdk_dir"/*.go \
  | sed -E 's/^func //; s/\($//' | sort -u | grep -v '^TestMain$' > "$test_file"

orphans=0
doubles=0
n_tests=0
while IFS= read -r t; do
  [ -n "$t" ] || continue
  n_tests=$((n_tests + 1))
  matches=0
  while IFS=$'\t' read -r re skip; do
    [ -n "$re" ] || continue
    if printf '%s\n' "$t" | grep -qE "$re" && { [ -z "$skip" ] || ! printf '%s\n' "$t" | grep -qE "$skip"; }; then
      matches=$((matches + 1))
    fi
  done < "$regex_file"
  # A test every container shard skips runs on the guest shard that names
  # it: what it proves holds only on that backend.
  if [ "$matches" -eq 0 ]; then
    while IFS=$'\t' read -r re; do
      [ -n "$re" ] || continue
      if printf '%s\n' "$t" | grep -qE "$re"; then
        matches=1
        break
      fi
    done < <(awk -F'\t' '$1 == "placement" { print $2 }' "$placement_file")
  fi
  if [ "$matches" -eq 0 ]; then
    echo "FAIL: Azure SDK test $t matches NO shard regex — it never runs in CI" >&2
    orphans=$((orphans + 1))
  elif [ "$matches" -gt 1 ]; then
    echo "FAIL: Azure SDK test $t matches $matches shard regexes — runs more than once" >&2
    doubles=$((doubles + 1))
  fi
done < "$test_file"

empty=0
while IFS= read -r re; do
  [ -n "$re" ] || continue
  if ! grep -qE "$re" "$test_file"; then
    echo "FAIL: placement shard regex $re matches no Azure SDK test" >&2
    empty=$((empty + 1))
  fi
done < <(awk -F'\t' '$1 == "placement" { print $2 }' "$placement_file")

n_shards=$(grep -c . "$regex_file")
if [ "$orphans" -ne 0 ] || [ "$doubles" -ne 0 ] || [ "$empty" -ne 0 ]; then
  echo "azure-sdk-shard-coverage: $orphans orphan(s), $doubles double(s), $empty empty placement shard(s) across $n_tests tests / $n_shards shards." >&2
  echo "Fix the shard 'run' regexes in $ci so each test matches exactly one shard." >&2
  exit 1
fi

echo "azure-sdk-shard-coverage: ok ($n_tests tests across $n_shards shards, each covered once)"
