#!/usr/bin/env bash
# refresh-drifted-specs.sh — re-vendor every specification whose pin is behind
# upstream, by driving the same per-document fetchers a human would.
#
# scripts/check-spec-freshness.sh reports drift and captures what it sampled;
# this turns that report into vendored files. It is what the scheduled freshness
# run needs in order to put a refresh somewhere: the run fails on main, which
# belongs to no branch, and the captured artifact it uploads expires in a week.
#
# Every row it acts on is one the freshness check reported as DRIFT, and each is
# refreshed through the fetcher that owns that corpus — fetch-aws-spec.sh,
# fetch-aws-service-reference.sh, fetch-azure-spec.sh, fetch-gcp-discovery.sh —
# so the pin and the timestamp in SOURCES.md are recorded exactly as they are
# for a hand-run refresh. Nothing here writes a pin itself.
#
# A Google Discovery document is re-vendored from the capture when the freshness
# run made one, because Google serves several revisions concurrently and the
# edge this runs on may not be the edge that saw the newer one. That is the same
# reason GCP_DISCOVERY_FROM exists.
#
# Usage:
#   scripts/refresh-drifted-specs.sh [aws|gcp|azure]...   (default: all three)
#
# Environment:
#   SPEC_FRESHNESS_CAPTURE_DIR   where the freshness check writes, and where
#                                this reads captured Google documents from.
#
# Exit status is 0 when every drifted row was refreshed, 1 when any row could
# not be, and 2 on a usage error. A run that finds no drift is a success that
# changes nothing.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLOUDS=("$@")
if [ ${#CLOUDS[@]} -eq 0 ]; then
  CLOUDS=(aws gcp azure)
fi
CAPTURE_DIR="${SPEC_FRESHNESS_CAPTURE_DIR:-}"

failed=0
refreshed=0

# sources_row prints the SOURCES.md row for a vendored file, or nothing.
sources_row() {
  local sources="$1" want="$2"
  while IFS='|' read -r _ file rest; do
    if [ "$(echo "$file" | tr -d ' `')" = "$want" ]; then
      printf '%s|%s\n' "$file" "$rest"
      return 0
    fi
  done < <(grep '^| `' "$sources" 2>/dev/null || true)
  return 1
}

field() { echo "$1" | cut -d'|' -f"$2" | tr -d ' `'; }

refresh_aws() {
  local file="$1" row upstream model
  if row="$(sources_row "$ROOT/specs/cloud-api/aws/SOURCES.md" "$file")"; then
    upstream="$(field "$row" 3)"
    model="$(basename "$upstream" .json)"
    echo "refresh aws model $model ($file)"
    bash "$ROOT/scripts/fetch-aws-spec.sh" "$model"
    return
  fi
  if row="$(sources_row "$ROOT/specs/cloud-api/aws/SERVICE_REFERENCE_SOURCES.md" "$file")"; then
    upstream="$(field "$row" 3)" # v1/<service>/<service>.json
    model="${upstream#v1/}"
    model="${model%%/*}"
    echo "refresh aws service reference $model ($file)"
    bash "$ROOT/scripts/fetch-aws-service-reference.sh" "$model"
    return
  fi
  echo "no AWS source row for $file" >&2
  failed=1
}

refresh_azure() {
  local file="$1" row upstream name
  if ! row="$(sources_row "$ROOT/specs/cloud-api/azure/SOURCES.md" "$file")"; then
    echo "no Azure source row for $file" >&2
    failed=1
    return
  fi
  upstream="$(field "$row" 3)"
  name="${file%.swagger.json.gz}"
  echo "refresh azure spec $name ($upstream)"
  bash "$ROOT/scripts/fetch-azure-spec.sh" "$upstream" "$name"
}

refresh_gcp() {
  local file="$1" row host srcpath version name central captured
  if ! row="$(sources_row "$ROOT/specs/cloud-api/gcp/SOURCES.md" "$file")"; then
    echo "no Google source row for $file" >&2
    failed=1
    return
  fi
  host="$(field "$row" 2)"
  srcpath="$(field "$row" 3)"
  # `$discovery/rest?version=v2` for a per-host document, and
  # `discovery/v1/apis/<name>/<version>/rest` for one only the central index
  # serves. fetch-gcp-discovery.sh selects between them explicitly.
  central=""
  case "$srcpath" in
  *discovery/v1/apis/*)
    central="central"
    version="${srcpath%/rest}"
    version="${version##*/}"
    ;;
  *)
    version="${srcpath##*version=}"
    ;;
  esac
  # `run-v2.discovery.json.gz` → local name `run`, version `v2`. Both come
  # from the file rather than the source path: a central-index path names the
  # service too (`discovery/v1/apis/compute/v1/rest`), and reading the version
  # from there left the name empty for Compute Engine, so the fetcher fell back
  # to the host's first label and asked www.googleapis.com for `apis/www/v1`.
  # That 404 aborted the whole Google sweep, every night.
  name="${file%.discovery.json.gz}"
  version="${name##*-}"
  name="${name%-"$version"}"
  if [ -z "$name" ] || [ -z "$version" ]; then
    echo "cannot read a service name and version out of $file" >&2
    failed=1
    return
  fi
  captured=""
  if [ -n "$CAPTURE_DIR" ] && [ -f "$CAPTURE_DIR/$file" ]; then
    captured="$CAPTURE_DIR/$file"
  fi
  echo "refresh google discovery $host $version (${captured:-live fetch})"
  if [ -n "$captured" ]; then
    GCP_DISCOVERY_FROM="$captured" bash "$ROOT/scripts/fetch-gcp-discovery.sh" "$host" "$version" "$name" ${central:+"$central"}
  else
    bash "$ROOT/scripts/fetch-gcp-discovery.sh" "$host" "$version" "$name" ${central:+"$central"}
  fi
}

for cloud in "${CLOUDS[@]}"; do
  case "$cloud" in
  aws | gcp | azure) ;;
  *)
    echo "unknown cloud: $cloud" >&2
    exit 2
    ;;
  esac
  report="$(mktemp)"
  # The check exits non-zero exactly when it found drift, which is the case
  # this exists to act on.
  bash "$ROOT/scripts/check-spec-freshness.sh" "$cloud" >"$report" 2>&1 || true
  while read -r line; do
    file="${line#DRIFT }"
    file="${file%%:*}"
    [ -n "$file" ] || continue
    refreshed=$((refreshed + 1))
    case "$cloud" in
    aws) refresh_aws "$file" ;;
    azure) refresh_azure "$file" ;;
    gcp) refresh_gcp "$file" ;;
    esac
  done < <(grep '^DRIFT ' "$report" || true)
  # A row the check could not evaluate at all is not drift, but it is not a
  # clean run either: the refresh cannot know whether that document moved.
  if grep -q '^?     ' "$report"; then
    echo "the freshness check could not reach every $cloud upstream:" >&2
    grep '^?     ' "$report" >&2
    failed=1
  fi
  rm -f "$report"
done

echo "refresh-drifted-specs: refreshed $refreshed vendored specification(s)"
exit "$failed"
