#!/usr/bin/env bash
# Rebuild the committed network sidecar binaries from sim/sidecar/cmd/sidecar
# with sim/sidecar/build.sh, in a scratch copy, and fail when any committed
# binary differs from the rebuild. The framework embeds the committed
# binaries, so a source edit without a rebuild ships stale code.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
sidecar_dir="$root/sim/sidecar"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
cp -r "$sidecar_dir/bin" "$scratch/committed"
rm -f "$sidecar_dir/bin/"*
bash "$sidecar_dir/build.sh"

mismatched=0
for built in "$sidecar_dir"/bin/*; do
	name="$(basename "$built")"
	if [ ! -f "$scratch/committed/$name" ]; then
		echo "sim/sidecar/bin/$name: build.sh produces it but it is not committed" >&2
		mismatched=1
	elif ! cmp -s "$built" "$scratch/committed/$name"; then
		echo "sim/sidecar/bin/$name: differs from a rebuild of cmd/sidecar" >&2
		mismatched=1
	fi
done
for committed in "$scratch"/committed/*; do
	name="$(basename "$committed")"
	if [ ! -f "$sidecar_dir/bin/$name" ]; then
		echo "sim/sidecar/bin/$name: committed but build.sh does not produce it" >&2
		mismatched=1
	fi
done

if [ "$mismatched" -ne 0 ]; then
	cp "$scratch"/committed/* "$sidecar_dir/bin/"
	echo "run sim/sidecar/build.sh and commit the rebuilt binaries" >&2
	exit 1
fi
