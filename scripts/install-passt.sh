#!/usr/bin/env bash
# install-passt.sh — install a pinned passt package, which provides pasta(1).
#
# The browser suites run Chromium in a network namespace of its own through
# pasta (ui/packages/core/e2e/isolated-chromium.sh). The package comes straight
# from the Ubuntu 24.04 archive pool and is checked against the SHA-256 the
# archive's signed package index lists for it, so the install consults no
# package index and no mirror's freshness.

set -euo pipefail

version=0.0~git20240220.1e6f92b-1
case "$(uname -m)" in
	x86_64)
		arch=amd64
		pool=http://archive.ubuntu.com/ubuntu/pool/universe/p/passt
		sha256=de07c44dcae84b58107cdf47e58f7d6ca2fdd466b193a10aea8a7115cf97bead
		;;
	aarch64)
		arch=arm64
		pool=http://ports.ubuntu.com/ubuntu-ports/pool/universe/p/passt
		sha256=5aadbb0812a14865eaf5c76e4babc058d9650f0258706b1d8f20560c43b9943c
		;;
	*)
		echo "install-passt: no passt package for $(uname -m)" >&2
		exit 1
		;;
esac

sudo=()
if [ "$(id -u)" -ne 0 ]; then
	sudo=(sudo)
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

deb="passt_${version}_${arch}.deb"
curl --fail --location --show-error --silent \
	--retry 5 --retry-all-errors --retry-delay 5 \
	--connect-timeout 20 --max-time 300 \
	--output "$tmpdir/$deb" "$pool/$deb"
echo "$sha256  $tmpdir/$deb" | sha256sum --check --strict -
"${sudo[@]}" dpkg -i "$tmpdir/$deb"

pasta --version | head -n 1
