#!/usr/bin/env bash
# install-aws-cli.sh — install the newest AWS CLI v2 over the runner's bundled
# one.
#
# The runner image's aws CLI lags the service models by weeks, so an operation
# AWS published since (AWS KMS Encapsulate, Amazon ECS Express Mode) is an
# invalid choice to it. The CLI tests and the documents that run them install
# the current release instead.

set -euo pipefail

case "$(uname -m)" in
	x86_64) arch=x86_64 ;;
	aarch64) arch=aarch64 ;;
	*)
		echo "install-aws-cli: no AWS CLI v2 archive for $(uname -m)" >&2
		exit 1
		;;
esac

sudo=()
if [ "$(id -u)" -ne 0 ]; then
	sudo=(sudo)
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
curl --fail --location --show-error --silent \
	--retry 5 --retry-all-errors --retry-delay 5 \
	--connect-timeout 20 --max-time 300 \
	"https://awscli.amazonaws.com/awscli-exe-linux-${arch}.zip" -o "$tmpdir/awscliv2.zip"
unzip -q -o "$tmpdir/awscliv2.zip" -d "$tmpdir"
"${sudo[@]}" "$tmpdir/aws/install" --update
hash -r
aws --version
