#!/usr/bin/env bash
# install-caddy.sh — install a pinned Caddy release and prove it reads the HTTPS
# gateway's Caddyfile.
#
# Ubuntu 24.04 packages Caddy 2.6.2, which predates the storage_check and
# storage_clean_interval global options make/https-gateway/Caddyfile sets, so
# the distribution package refuses the gateway's configuration; Caddy's own apt
# repository at dl.cloudsmith.io answers CI runners 402 Payment Required. The
# release archive comes from Caddy's GitHub release and is checked against the
# checksum file published with it.

set -euo pipefail

version="${CADDY_VERSION:-2.11.7}"
case "$(uname -m)" in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
	*)
		echo "install-caddy: no Caddy release archive for $(uname -m)" >&2
		exit 1
		;;
esac

sudo=()
if [ "$(id -u)" -ne 0 ]; then
	sudo=(sudo)
fi

repo_root=$(cd "$(dirname "$0")/.." && pwd)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

archive_dir="${CADDY_ARCHIVE_DIR:-$tmpdir}"
mkdir -p "$archive_dir"
archive="caddy_${version}_linux_${arch}.tar.gz"
base="https://github.com/caddyserver/caddy/releases/download/v${version}"
# GitHub release downloads answer 504 for minutes at a time, longer than curl's
# doubling backoff rides out, so CI keeps the archive in CADDY_ARCHIVE_DIR and
# downloads only what that directory lacks. The checksum check runs either way.
for file in "$archive" "caddy_${version}_checksums.txt"; do
	if [ -s "$archive_dir/$file" ]; then
		continue
	fi
	curl --fail --location --show-error --silent \
		--retry 8 --retry-all-errors \
		--connect-timeout 20 --max-time 300 --speed-limit 1024 --speed-time 30 \
		--output "$tmpdir/$file.part" "$base/$file"
	mv "$tmpdir/$file.part" "$archive_dir/$file"
done
(cd "$archive_dir" && grep " $archive\$" "caddy_${version}_checksums.txt" | sha512sum --check --strict -)

tar -xzf "$archive_dir/$archive" -C "$tmpdir" caddy
"${sudo[@]}" install -m 0755 "$tmpdir/caddy" /usr/local/bin/caddy

caddy version
caddy adapt --adapter caddyfile --config "$repo_root/make/https-gateway/Caddyfile" >/dev/null
