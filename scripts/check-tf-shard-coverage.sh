#!/usr/bin/env bash
# check-tf-shard-coverage.sh
#
# The AWS Terraform job in .github/workflows/ci.yml runs one package per
# matrix shard, named by a `tf_packages: "<package>"` entry. A stack directory
# under simulator-aws/terraform-tests/ that no shard names never runs in CI —
# and, because each stack is its own Go package, it is never even compiled, so
# nothing about it can fail. This guard asserts every stack package is named by
# exactly one AWS shard, and that every AWS shard names a package that exists.
#
# The sibling gates check-sdk-shard-coverage.sh and check-cli-shard-coverage.sh
# do the same for the SDK and CLI test names.
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
ci="$repo_root/.github/workflows/ci.yml"
tf_dir="$repo_root/simulator-aws/terraform-tests"

packages_file=$(mktemp)
shards_file=$(mktemp)
trap 'rm -f "$packages_file" "$shards_file"' EXIT

# Every Terraform test package: the root package plus each subdirectory that
# holds a Go test file. internal/ carries the shared harness and no tests.
{
  if compgen -G "$tf_dir/*_test.go" >/dev/null; then echo "."; fi
  for dir in "$tf_dir"/*/; do
    name=$(basename "$dir")
    if compgen -G "$dir*_test.go" >/dev/null; then echo "./$name"; fi
  done
} | sort -u >"$packages_file"

if [ ! -s "$packages_file" ]; then
  echo "FAIL: no Terraform test packages found under $tf_dir" >&2
  exit 1
fi

# Every AWS shard's package. tf_packages appears only on the aws rows; the gcp
# and azure suites take their package set from their own Makefiles.
grep -oE 'tf_packages: "[^"]*"' "$ci" \
  | sed -E 's/^tf_packages: "//; s/"$//' \
  | tr ' ' '\n' | grep -v '^$' | sort >"$shards_file"

if [ ! -s "$shards_file" ]; then
  echo "FAIL: no AWS Terraform shard packages (tf_packages: \"…\") found in $ci" >&2
  exit 1
fi

status=0

while IFS= read -r package; do
  [ -n "$package" ] || continue
  count=$(grep -cxF "$package" "$shards_file" || true)
  if [ "$count" -eq 0 ]; then
    echo "FAIL: Terraform package $package matches NO CI shard — it never runs, and never compiles, in CI" >&2
    status=1
  elif [ "$count" -gt 1 ]; then
    echo "FAIL: Terraform package $package is named by $count CI shards — it runs more than once" >&2
    status=1
  fi
done <"$packages_file"

while IFS= read -r shard; do
  [ -n "$shard" ] || continue
  if ! grep -qxF "$shard" "$packages_file"; then
    echo "FAIL: CI shard names Terraform package $shard, which has no tests under $tf_dir" >&2
    status=1
  fi
done <"$shards_file"

if [ "$status" -ne 0 ]; then
  echo "Fix the aws 'tf_packages' matrix entries in $ci so each package is sharded exactly once." >&2
  exit 1
fi

echo "tf-shard-coverage: ok ($(grep -c . "$packages_file") packages, each sharded once)"
