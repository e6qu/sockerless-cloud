#!/usr/bin/env bash
# Puts a set of base images on the host without asking a registry twice for the
# same bytes.
#
# The registries these images come from cap anonymous pulls by data volume, and
# that cap is not a transient failure: retrying inside a job does not recover
# it, and moving between Docker Hub and the ECR Public Gallery does not either,
# because both throttle the same runners. A cached tarball is the only thing
# that removes the request.
#
#   warm-base-images.sh <tarball> <image>...
#
# With the tarball present the images are loaded from it and no registry is
# contacted. Without it each image is pulled — with the same widening backoff a
# throttled registry deserves — and the set is saved for the next run.
set -euo pipefail

tarball="${1:?tarball path required}"
shift
images=("$@")
[ "${#images[@]}" -gt 0 ] || { echo "warm-base-images: at least one image required" >&2; exit 2; }

if [ -f "$tarball" ]; then
    echo "warm-base-images: loading $(basename "$tarball")"
    docker load --input "$tarball"
    # A tarball that turns out not to hold every image asked for is a stale
    # cache, not a reason to proceed short: fall through and fetch the rest.
    missing=0
    for image in "${images[@]}"; do
        docker image inspect "$image" >/dev/null 2>&1 || missing=1
    done
    [ "$missing" -eq 0 ] && exit 0
    echo "warm-base-images: the cached set is missing an image; fetching" >&2
fi

for image in "${images[@]}"; do
    if docker image inspect "$image" >/dev/null 2>&1; then
        continue
    fi
    fetched=0
    for attempt in 1 2 3 4 5; do
        if docker pull "$image"; then
            fetched=1
            break
        fi
        delay=$((attempt * 15))
        echo "warm-base-images: $image attempt $attempt failed; retrying in ${delay}s" >&2
        sleep "$delay"
    done
    if [ "$fetched" -ne 1 ]; then
        echo "warm-base-images: could not fetch $image after 5 attempts" >&2
        exit 1
    fi
done

mkdir -p "$(dirname "$tarball")"
docker save --output "$tarball" "${images[@]}"
echo "warm-base-images: saved ${#images[@]} image(s) to $(basename "$tarball")"
