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
cache_dir="$repo_root/.gocache/realexec-root"
mkdir -p "$cache_dir"

cd "$repo_root/realexec"
sudo env "PATH=$PATH" "GOCACHE=$cache_dir" "HOME=${HOME:-/root}" go test -tags realexec_host -v -count=1 ./...

# AWS sim real-fabric data planes: the NAT gateway's CreateNatGateway /
# CreateRoute wiring (real namespace NIC + nftables SNAT) and the security
# group's host-firewall tier (nftables ingress rules on a task's namespace NIC),
# proving both are implemented end-to-end on a real-execution host rather than
# only modeled at the API. The filter selects every real-fabric test in the
# package, so one added there runs here without being named.
echo "=== aws sim: real network data-plane wiring ==="
cd "$repo_root/simulator-aws"
sudo env "PATH=$PATH" "GOCACHE=$cache_dir" "HOME=${HOME:-/root}" go test -tags 'realexec_host noui' -run 'TestEC2Real' -v -count=1 .
