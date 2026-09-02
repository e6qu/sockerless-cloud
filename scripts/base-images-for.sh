#!/usr/bin/env bash
# Prints the container images a suite pulls, one per line, sorted.
#
#   base-images-for.sh <directory>...
#
# The scan reads the sources under each directory, tests included: an image a
# test pulls is as often named by the code under test as by the test itself,
# and the reaper's busybox is a production constant. Deriving the set from the
# source rather than restating it in a workflow keeps the two from drifting — a
# test that starts pulling a new image is warmed on its first run, with no list
# to remember to update.
#
# It descends, and it reads more than Go. A Terraform suite keeps one Go file
# per stack in its own subdirectory and names the workload image in the stack's
# HCL, so a flat Go-only scan sees neither: it misses the Amazon ECS pause
# image and the alpine workloads, which are the pulls that failed those jobs.
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

# The AWS Lambda runtime table is the one source that names images nothing
# necessarily fetches: it maps some thirty runtime identifiers to base images,
# and one arrives only when a suite invokes a function on that runtime. Reading
# it literally would cache all thirty, so it is resolved the other way round —
# from the identifiers the suites name — by lambda-runtime-images-for.sh.
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

{
    find "$@" -type f \( \
            -name '*.go' -o -name '*.tf' -o -name '*.tftpl' -o -name '*.hcl' -o \
            -name '*.sh' -o -name '*.yaml' -o -name '*.yml' -o -name '*.json' -o \
            -name 'Dockerfile*' \
        \) ! -path '*simulator-aws/lambda_runtime.go' -print0 |
        xargs -0 grep -hoE 'public\.ecr\.aws/[a-z0-9][a-z0-9/._-]*:[a-zA-Z0-9][a-zA-Z0-9._-]*'

    for dir in "$@"; do
        case "$dir" in
            *simulator-aws | *simulator-aws/*) bash "$here/lambda-runtime-images-for.sh" "$dir" ;;
        esac
    done
} | sort -u
