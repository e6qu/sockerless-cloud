#!/usr/bin/env bash
# Check drift between the vendored cloud API specs (specs/cloud-api/) and
# their upstreams. Definite drift exits non-zero.
#
# AWS + Azure pins are upstream commit SHAs: drift means the upstream file
# changed in a commit after the pin. GCP pins are Discovery "revision"
# fields: drift means the live document's revision moved.
#
# Usage: scripts/check-spec-freshness.sh [aws|gcp|azure]   (default: all)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CLOUDS="${1:-aws gcp azure}"
fail=0

check_repo_pinned() {
  # SOURCES.md rows: | `file` | `repo` | `path` | lic | `sha` | time |
  local sources="$1"
  [ -f "$sources" ] || return 0
  while IFS='|' read -r _ file repo path _lic pin _; do
    file="$(echo "$file" | tr -d ' \`')"
    repo="$(echo "$repo" | tr -d ' \`')"
    path="$(echo "$path" | tr -d ' \`')"
    pin="$(echo "$pin" | tr -d ' \`')"
    case "$pin" in revision* | modified*) continue ;; esac
    latest="$(gh api "repos/$repo/commits?path=$path&per_page=1" --jq '.[0].sha' 2>/dev/null || echo "")"
    if [ -z "$latest" ]; then
      echo "?     $file: cannot query upstream ($repo $path)"
      fail=1
    elif [ "$latest" = "$pin" ]; then
      echo "ok    $file"
    else
      echo "DRIFT $file: pinned ${pin:0:12}, upstream tip ${latest:0:12} ($repo $path)"
      fail=1
    fi
  done < <(grep '^| `' "$sources")
}

# AWS Service Reference documents are served live rather than out of a git
# repository, and the published index carries a per-service `modified`
# timestamp. One index fetch settles every vendored document, so drift is
# detected without re-downloading them.
check_aws_service_reference() {
  local sources="$ROOT/specs/cloud-api/aws/SERVICE_REFERENCE_SOURCES.md"
  local capture_dir="${SPEC_FRESHNESS_CAPTURE_DIR:-}"
  local index service latest pinned url
  [ -f "$sources" ] || return 0
  [ -n "$capture_dir" ] && mkdir -p "$capture_dir"
  index="$(mktemp)"
  if ! curl -fsSL -H 'Cache-Control: no-cache' -o "$index" "https://servicereference.us-east-1.amazonaws.com/" 2>/dev/null; then
    echo "?     service-reference: cannot fetch the published service index"
    rm -f "$index"
    fail=1
    return 0
  fi
  while IFS='|' read -r _ file _host path _lic pin _; do
    file="$(echo "$file" | tr -d ' \`')"
    path="$(echo "$path" | tr -d ' \`')"
    pin="$(echo "$pin" | tr -d ' \`')"
    case "$pin" in modified*) ;; *) continue ;; esac
    # path is v1/<service>/<service>.json
    service="${path#v1/}"
    service="${service%%/*}"
    latest="$(jq -er --arg s "$service" '.[] | select(.service == $s) | .modified' "$index" 2>/dev/null || echo "")"
    pinned="${pin#modified}"
    if [ -z "$latest" ]; then
      echo "?     $file: $service is not in the published service index"
      fail=1
    elif [ "$pinned" -lt "$latest" ]; then
      echo "DRIFT $file: pinned $pinned, upstream modified $latest"
      # Capture the newer document, as the other clouds do, so the refresh does
      # not depend on the authoring machine's edge serving what the runner saw.
      if [ -n "$capture_dir" ]; then
        url="$(jq -er --arg s "$service" '.[] | select(.service == $s) | .url' "$index")"
        if curl -fsSL -o "$index.doc" "$url" 2>/dev/null; then
          gzip -9 -n -c "$index.doc" >"$capture_dir/$(basename "$file")"
          rm -f "$index.doc"
          echo "      captured modified $latest in $capture_dir/$(basename "$file")"
        fi
      fi
      fail=1
    else
      echo "ok    $file"
    fi
  done < <(grep '^| `' "$sources")
  rm -f "$index"
}

check_gcp() {
  local sources="$ROOT/specs/cloud-api/gcp/SOURCES.md"
  local probes="${SPEC_FRESHNESS_GCP_PROBES:-3}"
  local capture_dir="${SPEC_FRESHNESS_CAPTURE_DIR:-}"
  local probe_dir
  local probe
  [ -f "$sources" ] || return 0
  if ! [[ "$probes" =~ ^[1-9][0-9]*$ ]]; then
    echo "SPEC_FRESHNESS_GCP_PROBES must be a positive integer, got $probes" >&2
    return 2
  fi
  probe_dir="$(mktemp -d)"
  if [ -n "$capture_dir" ]; then
    mkdir -p "$capture_dir"
  fi
  while IFS='|' read -r _ file host path _lic pin _; do
    file="$(echo "$file" | tr -d ' \`')"
    host="$(echo "$host" | tr -d ' \`')"
    path="$(echo "$path" | tr -d ' \`')"
    pin="$(echo "$pin" | tr -d ' \`')"
    url="https://$host/${path//\\/}"
    revisions=()
    newest_file=""
    newest_revision=""
    for ((probe = 1; probe <= probes; probe++)); do
      candidate="$probe_dir/${file%.gz}-$probe.json"
      if ! curl -fsSL -H 'Cache-Control: no-cache' -o "$candidate" "$url" 2>/dev/null; then
        continue
      fi
      live="$(jq -er '.revision | select(type == "string" or type == "number") | tostring' "$candidate" 2>/dev/null || true)"
      if [[ "$live" =~ ^[0-9]+$ ]]; then
        revisions+=("$live")
        if [ -z "$newest_revision" ] || [ "$live" -gt "$newest_revision" ]; then
          newest_revision="$live"
          newest_file="$candidate"
        fi
      fi
    done
    if [ "${#revisions[@]}" -eq 0 ]; then
      echo "?     $file: no valid Discovery revision from $probes probes of $url"
      fail=1
      continue
    fi
    newest="$(printf '%s\n' "${revisions[@]}" | sort -n | tail -1)"
    pinned="${pin#revision}"
    if ! [[ "$pinned" =~ ^[0-9]+$ ]]; then
      echo "?     $file: invalid recorded pin $pin"
      fail=1
    elif [ "$pinned" -lt "$newest" ]; then
      echo "DRIFT $file: pinned $pin, newest sampled revision $newest (${#revisions[@]}/$probes probes succeeded)"
      if [ -n "$capture_dir" ] && [ -n "$newest_file" ]; then
        gzip -9 -n -c "$newest_file" >"$capture_dir/$file"
        echo "      captured revision $newest in $capture_dir/$file"
      fi
      fail=1
    else
      echo "ok    $file (pinned $pinned, newest sampled $newest; ${#revisions[@]}/$probes probes succeeded)"
    fi
  done < <(grep '^| `' "$sources")
  rm -rf "$probe_dir"
}

for cloud in $CLOUDS; do
  echo "== $cloud"
  case "$cloud" in
    aws)
      check_repo_pinned "$ROOT/specs/cloud-api/aws/SOURCES.md"
      check_aws_service_reference
      ;;
    azure) check_repo_pinned "$ROOT/specs/cloud-api/azure/SOURCES.md" ;;
    gcp) check_gcp ;;
    *)
      echo "unknown cloud: $cloud" >&2
      exit 2
      ;;
  esac
done

exit "$fail"
