#!/usr/bin/env bash
# Tests for which rows refresh-drifted-specs.sh acts on.
#
# The freshness run that reports drift captures the document it saw, and this
# script turns that report into vendored files. Its own re-check is a second
# sample from another runner: it can miss a row the run captured, and find a
# row the run called current. The rows it acts on therefore have to be the
# union of both, which is what these cases hold it to.
#
# The fixture replaces the freshness check and the per-corpus fetchers with
# recorders, so a case observes exactly which rows were refreshed and through
# which fetcher, without reaching any upstream.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/scripts" "$fixture/specs/cloud-api/gcp" "$fixture/specs/cloud-api/aws" "$fixture/specs/cloud-api/azure"
cp "$root/scripts/refresh-drifted-specs.sh" "$fixture/scripts/"

cat >"$fixture/specs/cloud-api/gcp/SOURCES.md" <<'EOF'
# Vendored Google Cloud Discovery documents

| file | host | path | licence | pin | vendored |
| --- | --- | --- | --- | --- | --- |
| `eventarc-v1.discovery.json.gz` | `eventarc.googleapis.com` | `$discovery/rest?version=v1` | Apache-2.0 | `revision 20260907` | 2026-09-11T11:06:47Z |
| `iamcredentials-v1.discovery.json.gz` | `iamcredentials.googleapis.com` | `$discovery/rest?version=v1` | Apache-2.0 | `revision 20260903` | 2026-09-11T11:06:47Z |
EOF
: >"$fixture/specs/cloud-api/aws/SOURCES.md"
: >"$fixture/specs/cloud-api/azure/SOURCES.md"

# The freshness check the script re-runs: it reports whatever a case puts in
# REFRESH_TEST_REPORT, standing in for this runner's own sample.
cat >"$fixture/scripts/check-spec-freshness.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = gcp ] && [ -n "${REFRESH_TEST_REPORT:-}" ]; then
  cat "$REFRESH_TEST_REPORT"
  exit 1
fi
exit 0
EOF

cat >"$fixture/scripts/fetch-gcp-discovery.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s %s %s\n' "$1" "$2" "${GCP_DISCOVERY_FROM:-live}" >>"$REFRESH_TEST_FETCHES"
EOF
chmod +x "$fixture/scripts/check-spec-freshness.sh" "$fixture/scripts/fetch-gcp-discovery.sh"

run_case() {
  local name=$1 report=$2 captures=$3
  local case_dir="$fixture/$name"
  mkdir -p "$case_dir"
  printf '%s' "$report" >"$case_dir/report"
  : >"$case_dir/fetches"
  local capture_dir=''
  if [ -n "$captures" ]; then
    capture_dir="$case_dir/captures"
    mkdir -p "$capture_dir"
    for captured in $captures; do
      printf 'captured document\n' | gzip -9 -n -c >"$capture_dir/$captured"
    done
  fi
  REFRESH_TEST_REPORT="$case_dir/report" \
    REFRESH_TEST_FETCHES="$case_dir/fetches" \
    SPEC_FRESHNESS_CAPTURE_DIR="$capture_dir" \
    bash "$fixture/scripts/refresh-drifted-specs.sh" gcp >"$case_dir/output" 2>&1
  cat "$case_dir/fetches"
}

expect_fetches() {
  local name=$1 expected=$2 actual=$3
  if [ "$(printf '%s\n' "$actual" | sort)" != "$(printf '%s\n' "$expected" | sort)" ]; then
    printf '%s: refreshed\n%s\nwanted\n%s\n' "$name" "$actual" "$expected" >&2
    exit 1
  fi
}

# A row this sample reports is refreshed, as it always was.
fetches=$(run_case sampled 'DRIFT eventarc-v1.discovery.json.gz: pinned revision 20260907, newest sampled revision 20260911
' '')
expect_fetches 'a sampled row' 'eventarc.googleapis.com v1 live' "$fetches"

# A row the freshness run captured is refreshed even when this sample calls
# every document current -- the case that left eventarc stale.
fetches=$(run_case captured '' 'eventarc-v1.discovery.json.gz')
expect_fetches 'a captured row this sample missed' \
  "eventarc.googleapis.com v1 $fixture/captured/captures/eventarc-v1.discovery.json.gz" "$fetches"

# Both sources naming the same row refresh it once.
fetches=$(run_case both 'DRIFT eventarc-v1.discovery.json.gz: pinned revision 20260907, newest sampled revision 20260911
' 'eventarc-v1.discovery.json.gz')
expect_fetches 'a row both name' \
  "eventarc.googleapis.com v1 $fixture/both/captures/eventarc-v1.discovery.json.gz" "$fetches"

# Each row is refreshed, whichever source named it.
fetches=$(run_case union 'DRIFT iamcredentials-v1.discovery.json.gz: pinned revision 20260903, newest sampled revision 20260918
' 'eventarc-v1.discovery.json.gz')
expect_fetches 'the union of both sources' \
  "iamcredentials.googleapis.com v1 live
eventarc.googleapis.com v1 $fixture/union/captures/eventarc-v1.discovery.json.gz" "$fetches"

# A capture belonging to no row of this cloud is not vendored through its
# fetchers.
fetches=$(run_case foreign '' 'glue.smithy.json.gz')
expect_fetches 'a capture from another corpus' '' "$fetches"

echo 'refresh-drifted-specs row selection passed.'
