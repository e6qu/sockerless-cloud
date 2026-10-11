#!/usr/bin/env bash
set -euo pipefail

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

[ "$(uname -s)" = "Linux" ] || fail "real-execution network/NIC test requires Linux"

need_cmd firecracker
need_cmd curl
need_cmd jailer
need_cmd go
need_cmd ip
need_cmd nft
need_cmd ping
need_cmd sudo

[ -e /dev/kvm ] || fail "real-execution capability test requires /dev/kvm"
if ! { [ -r /dev/kvm ] && [ -w /dev/kvm ]; }; then
  sudo test -r /dev/kvm || fail "real-execution capability test requires read access to /dev/kvm"
  sudo test -w /dev/kvm || fail "real-execution capability test requires write access to /dev/kvm"
fi

repo_root="$(git rev-parse --show-toplevel)"
bin_dir="$(mktemp -d)"
trap 'rm -rf "$bin_dir"' EXIT

# host_tests prints a -test.run pattern naming every test declared in a file of
# the package that carries the realexec_host build tag, so a host test added to
# any package below runs here without being named.
host_tests() {
  local dir="$1" names
  names="$(grep -l '^//go:build .*realexec_host' "$dir"/*_test.go \
    | xargs sed -n 's/^func \(Test[A-Za-z0-9_]*\)(t \*testing\.T).*/\1/p' \
    | sort -u | paste -sd '|' -)"
  [ -n "$names" ] || fail "no realexec_host test found in $dir"
  printf '^(%s)$' "$names"
}

# Compile each package's tests as the invoking user, so the build uses the
# user's warm Go cache, and run only the resulting binary as root, which the
# network namespaces, veth pairs and nftables tables the tests create need.
run_host_package() {
  local module="$1" pkg="$2" tags="$3" filter="$4" name
  name="$(printf '%s' "$module/$pkg" | tr '/.' '__')"
  (cd "$repo_root/$module" && go test -c -tags "$tags" -o "$bin_dir/$name.test" "$pkg")
  echo "=== $module/$pkg: real network host tests ==="
  (cd "$repo_root/$module/${pkg#./}" && sudo "PATH=$PATH" "HOME=${HOME:-/root}" \
    "$bin_dir/$name.test" -test.v -test.count=1 ${filter:+-test.run "$filter"})
}

# Every package holding a realexec_host test runs here, so a host test added
# anywhere in the repository runs without being named: the real-execution
# substrate, and each simulator's real-fabric data planes (the AWS NAT gateway
# and security-group host firewall, the Cloud NAT gateway's reserved address,
# the Azure NAT gateway's subnet association).
packages="$(cd "$repo_root" && git ls-files -z '*_test.go' \
  | xargs -0 grep -l '^//go:build .*realexec_host' | xargs -n1 dirname | sort -u)"
[ -n "$packages" ] || fail "no realexec_host test found in the repository"
for dir in $packages; do
  module="$dir"
  until [ -f "$repo_root/$module/go.mod" ]; do
    [ "$module" != . ] || fail "$dir is in no Go module"
    module="$(dirname "$module")"
  done
  pkg="./${dir#"$module"}"
  pkg="${pkg%/}"
  pkg="${pkg/#.\/\//./}"
  run_host_package "$module" "$pkg" 'realexec_host noui' "$(host_tests "$repo_root/$dir")"
done
