#!/usr/bin/env bash
set -euo pipefail

version="${FIRECRACKER_VERSION:-v1.15.1}"
arch="$(uname -m)"

case "$arch" in
  x86_64|aarch64) ;;
  *)
    echo "ERROR: Firecracker release binaries are expected for x86_64 or aarch64; got $arch" >&2
    exit 1
    ;;
esac

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

archive="$tmpdir/firecracker-${version}-${arch}.tgz"
url="https://github.com/firecracker-microvm/firecracker/releases/download/${version}/firecracker-${version}-${arch}.tgz"

curl -fsSLo "$archive" "$url"
tar -xzf "$archive" -C "$tmpdir"

release_dir="$tmpdir/release-${version}-${arch}"
firecracker_bin="$release_dir/firecracker-${version}-${arch}"
jailer_bin="$release_dir/jailer-${version}-${arch}"

if [ ! -x "$firecracker_bin" ]; then
  echo "ERROR: downloaded Firecracker archive did not contain executable $firecracker_bin" >&2
  exit 1
fi
if [ ! -x "$jailer_bin" ]; then
  echo "ERROR: downloaded Firecracker archive did not contain executable $jailer_bin" >&2
  exit 1
fi

sudo install -m 0755 "$firecracker_bin" /usr/local/bin/firecracker
sudo install -m 0755 "$jailer_bin" /usr/local/bin/jailer

firecracker --version
