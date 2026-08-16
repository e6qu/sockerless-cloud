#!/usr/bin/env bash
# Reconcile a published release against the artifacts its tag promises.
#
# release-please pushes the vX.Y.Z tag when the release pull request merges and
# the Release workflow builds the artifacts afterwards, so between those two
# moments the tag names a release whose assets do not exist yet. A build that
# fails is caught by the job that failed; a build that HANGS is not, and it
# leaves a tagged, published release that looks entirely ordinary while
# carrying part of its contents. This script is the reconciliation: it asserts
# every expected asset and every multi-architecture image index exists for the
# tag, and fails loudly when one does not.
#
# Usage: verify-release-complete.sh <tag>
set -euo pipefail

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "usage: $0 <tag>" >&2
  exit 2
fi

REPO="${GITHUB_REPOSITORY:-e6qu/sockerless-cloud}"
REGISTRY_NAMESPACE="${RELEASE_REGISTRY_NAMESPACE:-ghcr.io/e6qu}"
CLOUDS="aws gcp azure"

# The expected asset set is derived from the same matrix the Release workflow
# builds from, so adding a platform there and forgetting it here is a mismatch
# the check reports rather than a silent gap.
expected_assets() {
  local cloud goos arch
  for cloud in $CLOUDS; do
    for goos in linux darwin; do
      for arch in amd64 arm64; do
        echo "simulator-${cloud}_${TAG}_${goos}_${arch}.tar.gz"
        echo "simulator-${cloud}_${TAG}_${goos}_${arch}.tar.gz.sha256"
      done
    done
  done
  for cloud in $CLOUDS; do
    echo "sockerless-console-${cloud}_${TAG}.tar.gz"
    echo "sockerless-console-${cloud}_${TAG}.tar.gz.sha256"
  done
}

failures=0

echo "▸ reconciling release $TAG in $REPO"

if ! actual=$(gh release view "$TAG" --repo "$REPO" --json assets --jq '.assets[].name' 2>/dev/null); then
  echo "ERROR: no published release found for tag $TAG." >&2
  echo "The tag exists only if release-please pushed it; a missing release means" >&2
  echo "the Release workflow never attached anything to it." >&2
  exit 1
fi

expected_count=0
while IFS= read -r asset; do
  expected_count=$((expected_count + 1))
  if ! printf '%s\n' "$actual" | grep -qxF "$asset"; then
    echo "MISSING asset: $asset" >&2
    failures=$((failures + 1))
  fi
done <<EOF
$(expected_assets)
EOF

actual_count=$(printf '%s\n' "$actual" | grep -c . || true)
echo "  assets: ${actual_count} published, ${expected_count} expected"

# An asset the workflow no longer produces is as much a drift signal as a
# missing one — it means the matrix and the release disagree.
while IFS= read -r asset; do
  [ -n "$asset" ] || continue
  if ! expected_assets | grep -qxF "$asset"; then
    echo "UNEXPECTED asset (not produced by the release matrix): $asset" >&2
    failures=$((failures + 1))
  fi
done <<EOF
$actual
EOF

# Every simulator image must resolve to a real multi-architecture index, not to
# one of the per-architecture manifests the build pushes first. A tag that
# resolves to a single-arch manifest would pull the wrong binary on half the
# hosts that ask for it.
for cloud in $CLOUDS; do
  ref="${REGISTRY_NAMESPACE}/sockerless-simulator-${cloud}:${TAG}"
  if ! media=$(docker buildx imagetools inspect --format '{{.Manifest.MediaType}}' "$ref" 2>/dev/null); then
    echo "MISSING image index: $ref" >&2
    failures=$((failures + 1))
    continue
  fi
  if [ "$media" != "application/vnd.oci.image.index.v1+json" ]; then
    echo "ERROR: $ref is $media, not an OCI image index." >&2
    failures=$((failures + 1))
    continue
  fi
  platforms=$(docker buildx imagetools inspect \
    --format '{{range .Manifest.Manifests}}{{printf "%s/%s\n" .Platform.OS .Platform.Architecture}}{{end}}' \
    "$ref" | sort)
  if [ "$platforms" != "$(printf 'linux/amd64\nlinux/arm64')" ]; then
    echo "ERROR: $ref carries platforms [$(echo "$platforms" | tr '\n' ' ')], want linux/amd64 linux/arm64." >&2
    failures=$((failures + 1))
    continue
  fi
  echo "  index ok: $ref"
done

if [ "$failures" -ne 0 ]; then
  echo >&2
  echo "ERROR: release $TAG is incomplete — ${failures} problem(s) above." >&2
  echo "The tag is published and resolvable, so anything pinning it right now" >&2
  echo "resolves to a partial release. Re-run the Release workflow for this tag" >&2
  echo "and re-run this check before letting any consumer pin it." >&2
  exit 1
fi

echo "verify-release-complete: $TAG is complete (${actual_count} assets, 3 multi-architecture indexes)."
