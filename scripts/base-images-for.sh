#!/usr/bin/env bash
# Prints the container images a package's tests pull, one per line, sorted.
#
#   base-images-for.sh <package directory>...
#
# The scan reads every Go file in the package, tests included: an image a test
# pulls is as often named by the code under test as by the test itself, and the
# reaper's busybox is a production constant. Deriving the set from the source
# rather than restating it in a workflow keeps the two from drifting — a test
# that starts pulling a new image is warmed on its first run, with no list to
# remember to update.
#
# The scan is limited to the ECR Public Gallery, which is where every base
# image these tests pull comes from. A wider host pattern would sweep up the
# registry names the simulators serve themselves — myacr.azurecr.io is a
# coordinate the test points a client at, not somewhere to fetch bytes from,
# and asking a real registry for it would fail.
set -euo pipefail

[ "$#" -gt 0 ] || { echo "base-images-for: at least one directory required" >&2; exit 2; }

for dir in "$@"; do
    [ -d "$dir" ] || { echo "base-images-for: no such directory: $dir" >&2; exit 2; }
done

for dir in "$@"; do
    for file in "$dir"/*.go; do
        [ -e "$file" ] || continue
        grep -hoE 'public\.ecr\.aws/[a-z0-9][a-z0-9/._-]*:[a-zA-Z0-9][a-zA-Z0-9._-]*' "$file" || true
    done
done | sort -u
