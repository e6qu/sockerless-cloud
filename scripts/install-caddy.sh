#!/usr/bin/env bash
# install-caddy.sh — install Caddy from Caddy's own apt repository and prove it
# reads the HTTPS gateway's Caddyfile.
#
# Ubuntu 24.04 packages Caddy 2.6.2, which predates the storage_check and
# storage_clean_interval global options make/https-gateway/Caddyfile sets, so
# the distribution package refuses the gateway's configuration. The repository
# Caddy publishes at dl.cloudsmith.io carries the current release.
#
# The jobs run caddy themselves; the package's caddy.service is not wanted, and
# starting it once held "Setting up caddy" for three minutes. A policy-rc.d
# that answers 101 tells the package scripts not to start services.

set -euo pipefail

sudo=()
if [ "$(id -u)" -ne 0 ]; then
	sudo=(sudo)
fi

repo_root=$(cd "$(dirname "$0")/.." && pwd)
keyring=/usr/share/keyrings/caddy-stable-archive-keyring.gpg
sources=/etc/apt/sources.list.d/caddy-stable.list
apt_options=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30)

printf '#!/bin/sh\nexit 101\n' | "${sudo[@]}" tee /usr/sbin/policy-rc.d >/dev/null
"${sudo[@]}" chmod 0755 /usr/sbin/policy-rc.d

curl -sSLf --retry 3 https://dl.cloudsmith.io/public/caddy/stable/gpg.key |
	"${sudo[@]}" gpg --batch --yes --dearmor -o "$keyring"
curl -sSLf --retry 3 https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt |
	"${sudo[@]}" tee "$sources" >/dev/null
"${sudo[@]}" chmod o+r "$keyring" "$sources"

# Refresh only Caddy's list: the job needs nothing else from the package
# mirrors, and a degraded Ubuntu mirror must not fail it.
"${sudo[@]}" apt-get "${apt_options[@]}" update \
	-o Dir::Etc::sourcelist="$sources" -o Dir::Etc::sourceparts=- -o APT::Get::List-Cleanup=0
"${sudo[@]}" apt-get "${apt_options[@]}" install -y caddy

caddy version
caddy adapt --adapter caddyfile --config "$repo_root/make/https-gateway/Caddyfile" >/dev/null
