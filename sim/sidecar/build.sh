#!/usr/bin/env bash
# Build the sidecar binaries sim/sidecar embeds, one per architecture the
# container engine may run, with the pinned toolchain and no build id, so a
# rebuild on any host produces the committed bytes byte for byte.
set -euo pipefail
cd "$(dirname "$0")"
for arch in amd64 arm64; do
	GOTOOLCHAIN=go1.26.3 CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
		go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o "bin/sidecar-linux-$arch" ./cmd/sidecar
done
