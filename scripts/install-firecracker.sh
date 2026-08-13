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

# The binaries live inside the Firecracker CI cache the workflow already
# restores, so a cache hit installs without any network. One GitHub releases
# CDN incident failed every Firecracker-dependent job at this download in the
# same evening — twice, once straight through the bounded retries — and the
# binaries are immutable per version, which is exactly what a cache is for.
cache_dir="${FIRECRACKER_BIN_CACHE:-$HOME/.cache/sockerless/firecracker-ci}/bin-${version}-${arch}"
if [ -x "$cache_dir/firecracker" ] && [ -x "$cache_dir/jailer" ]; then
  sudo install -m 0755 "$cache_dir/firecracker" /usr/local/bin/firecracker
  sudo install -m 0755 "$cache_dir/jailer" /usr/local/bin/jailer
  firecracker --version
  exit 0
fi

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

archive="$tmpdir/firecracker-${version}-${arch}.tgz"
url="https://github.com/firecracker-microvm/firecracker/releases/download/${version}/firecracker-${version}-${arch}.tgz"

# Bounded retries: the GitHub releases CDN answers transient 503s, and one
# such blip once failed three CI jobs at their install step simultaneously.
curl --fail --location --show-error --silent \
  --retry 5 --retry-all-errors --retry-delay 5 \
  --connect-timeout 20 --max-time 300 \
  --output "$archive" "$url"
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

# Populate the cache so the dedicated Firecracker job's cache save carries the
# binaries to every restore-only sibling job.
mkdir -p "$cache_dir"
install -m 0755 "$firecracker_bin" "$cache_dir/firecracker"
install -m 0755 "$jailer_bin" "$cache_dir/jailer"

firecracker --version
