#!/usr/bin/env bash
# check-azure-tf-shard-coverage.sh
#
# The Azure Terraform job in .github/workflows/ci.yml runs one `go test -run`
# pattern per matrix shard, named by a `tf_run:` entry on an azure row. A test
# that no pattern matches compiles but never runs in CI, and one that two
# patterns match runs twice. This gate asserts every Azure TestTerraform…
# function matches exactly one shard, and that every shard matches a test.
#
# The sibling gate check-tf-shard-coverage.sh does the same for the AWS
# Terraform packages.
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
ci="$repo_root/.github/workflows/ci.yml"
tf_dir="$repo_root/simulator-azure/terraform-tests"

regex_file=$(mktemp)
test_file=$(mktemp)
trap 'rm -f "$regex_file" "$test_file"' EXIT

# tf_run appears only on the azure rows of the tf job's matrix.
awk '
  /^ *- suite:/ { cloud=""; next }
  /^ *cloud:/   { cloud=$2; next }
  /^ *tf_run:/  { if (cloud == "azure") { sub(/^ *tf_run: */, ""); print } }
' "$ci" | sed -E "s/^['\"]//; s/['\"]\$//" >"$regex_file"
if [ ! -s "$regex_file" ]; then
  echo "FAIL: no Azure Terraform shard patterns (tf_run: …) found in $ci" >&2
  exit 1
fi

grep -hoE '^func TestTerraform[A-Za-z0-9_]*\(' "$tf_dir"/*_test.go \
  | sed -E 's/^func //; s/\($//' | sort -u >"$test_file"
if [ ! -s "$test_file" ]; then
  echo "FAIL: no TestTerraform… functions found under $tf_dir" >&2
  exit 1
fi

exit_status=0
while IFS= read -r t; do
  matches=0
  while IFS= read -r re; do
    if printf '%s\n' "$t" | grep -qE "$re"; then
      matches=$((matches + 1))
    fi
  done <"$regex_file"
  if [ "$matches" -eq 0 ]; then
    echo "FAIL: Azure Terraform test $t matches NO tf_run shard — it never runs in CI" >&2
    exit_status=1
  elif [ "$matches" -gt 1 ]; then
    echo "FAIL: Azure Terraform test $t matches $matches tf_run shards — it runs more than once" >&2
    exit_status=1
  fi
done <"$test_file"

while IFS= read -r re; do
  if ! grep -qE "$re" "$test_file"; then
    echo "FAIL: Azure tf_run shard '$re' matches no TestTerraform… function under $tf_dir" >&2
    exit_status=1
  fi
done <"$regex_file"

if [ "$exit_status" -ne 0 ]; then
  echo "Fix the azure 'tf_run' matrix entries in $ci so each test matches exactly one shard." >&2
  exit 1
fi

echo "azure-tf-shard-coverage: ok ($(grep -c . "$test_file") tests across $(grep -c . "$regex_file") shards, each covered once)"
