#!/usr/bin/env bash
# ci-apt-install.sh — install only the packages the runner does not already
# have, and touch the package index only if there is something to install.
#
# The runner image already ships most of what the jobs ask for: of the seven
# packages the real-execution jobs install, curl, e2fsprogs, iproute2,
# iputils-ping and procps are present out of the box. Running `apt-get update`
# regardless means a degraded Ubuntu mirror fails a job that needed nothing
# from it — which is exactly what happened to two jobs of one run, each after
# `scripts/ci-apt-update.sh` had spent ten minutes on three honest retries.
#
# So: ask dpkg what is missing, and stop there when the answer is nothing. When
# something is missing the index refresh and the install run as before, and a
# genuinely unavailable package still fails loudly — the point is not to
# tolerate a broken mirror, it is not to consult one with no question to ask.

set -euo pipefail

if [ "$#" -eq 0 ]; then
	echo "usage: ci-apt-install.sh <package>..." >&2
	exit 2
fi

missing=()
for package in "$@"; do
	if ! dpkg-query --show --showformat='${db:Status-Status}\n' "$package" 2>/dev/null | grep -qx installed; then
		missing+=("$package")
	fi
done

if [ "${#missing[@]}" -eq 0 ]; then
	echo "ci-apt-install: already installed: $*"
	exit 0
fi

echo "ci-apt-install: installing ${missing[*]} (already present: $(($# - ${#missing[@]})) of $#)"

root="$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)"
bash "$root/ci-apt-update.sh"
sudo apt-get -o Acquire::Retries=3 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 \
	install -y "${missing[@]}"
