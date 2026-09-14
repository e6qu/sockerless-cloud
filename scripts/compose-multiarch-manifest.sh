#!/usr/bin/env bash
# Compose and verify one simulator image's multi-architecture manifest list from
# its two per-architecture images. publish-container-images.yml (commit tags) and
# release.yml (version tags) composed it identically inline; both call this now.
#
# `docker buildx imagetools create` reads the per-architecture images' blobs from
# GHCR's blob storage, and one TCP reset there failed 0.32.2's release manifest
# job (`read: connection reset by peer`) — which skipped the release-complete
# check — although both per-architecture images were whole. Composing is
# idempotent: the same two digests always yield the same index. So a failure that
# is a broken connection is composed again, at most three times; anything else —
# a missing per-architecture image, an authorization refusal, a wrong media type —
# fails at once, exactly as before.
#
# Usage: scripts/compose-multiarch-manifest.sh <tag>
set -euo pipefail

tag="${1:?usage: compose-multiarch-manifest.sh <tag>}"
amd64_ref="${tag}-amd64"
arm64_ref="${tag}-arm64"
echo "▸ assembling ${tag} from ${amd64_ref} + ${arm64_ref}"

attempts=3
for attempt in $(seq 1 "$attempts"); do
  if output=$(docker buildx imagetools create --tag "$tag" "$amd64_ref" "$arm64_ref" 2>&1); then
    printf '%s\n' "$output"
    break
  fi
  printf '%s\n' "$output" >&2
  case "$output" in
    *"connection reset by peer"* | *"unexpected EOF"* | *"TLS handshake timeout"* | *"i/o timeout"* | *"502 Bad Gateway"* | *"503 Service Unavailable"* | *"504 Gateway Timeout"*)
      if [ "$attempt" -lt "$attempts" ]; then
        echo "compose-multiarch-manifest: the registry connection broke (attempt ${attempt} of ${attempts}); composing again" >&2
        sleep $((attempt * 10))
        continue
      fi
      echo "compose-multiarch-manifest: the registry connection broke ${attempts} times; giving up" >&2
      ;;
  esac
  exit 1
done

for ref in "$amd64_ref" "$arm64_ref"; do
  media_type=$(docker buildx imagetools inspect --format '{{.Manifest.MediaType}}' "$ref")
  test "$media_type" = "application/vnd.oci.image.manifest.v1+json"
done

media_type=$(docker buildx imagetools inspect --format '{{.Manifest.MediaType}}' "$tag")
test "$media_type" = "application/vnd.oci.image.index.v1+json"
platforms=$(docker buildx imagetools inspect \
  --format '{{range .Manifest.Manifests}}{{printf "%s/%s\n" .Platform.OS .Platform.Architecture}}{{end}}' \
  "$tag" | sort)
test "$platforms" = "$(printf 'linux/amd64\nlinux/arm64')"
echo "▸ ${tag}: index of linux/amd64 + linux/arm64"
