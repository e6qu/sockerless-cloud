#!/usr/bin/env bash
# Rebuild the committed Amazon RDS AWSAuthenticationPlugin shared objects from
# aws_authentication_plugin.c with their own build.sh, in a scratch copy, and
# fail when any committed binary differs from the rebuild. The simulator embeds
# the committed objects, so a source edit without a rebuild ships stale code.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
plugin_dir="$root/simulator-aws/rds_auth_plugin"

scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

cp "$plugin_dir/build.sh" "$plugin_dir/aws_authentication_plugin.c" "$scratch/"
clang --version | head -n 1
sh "$scratch/build.sh"

mismatched=0
for built in "$scratch"/*.so; do
	name="$(basename "$built")"
	if [ ! -f "$plugin_dir/$name" ]; then
		echo "simulator-aws/rds_auth_plugin/$name: build.sh produces it but it is not committed" >&2
		mismatched=1
	elif ! cmp -s "$built" "$plugin_dir/$name"; then
		echo "simulator-aws/rds_auth_plugin/$name: differs from a rebuild of aws_authentication_plugin.c" >&2
		mismatched=1
	fi
done
for committed in "$plugin_dir"/*.so; do
	name="$(basename "$committed")"
	if [ ! -f "$scratch/$name" ]; then
		echo "simulator-aws/rds_auth_plugin/$name: committed but build.sh does not produce it" >&2
		mismatched=1
	fi
done

if [ "$mismatched" -ne 0 ]; then
	echo "run simulator-aws/rds_auth_plugin/build.sh and commit the rebuilt shared objects" >&2
	exit 1
fi
echo "committed RDS authentication plugin binaries match a rebuild of their source"
