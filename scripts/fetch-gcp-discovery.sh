#!/usr/bin/env bash
# Vendor (or refresh) one Google API Discovery document into specs/cloud-api/gcp/.
#
# Discovery documents are the machine-readable source google.golang.org/api
# clients are generated from. They are served live (not versioned in git), and
# Google can serve several revisions concurrently. The fetch probes the
# endpoint repeatedly and records the newest document it observes.
#
# Two upstream locations exist, selected explicitly (no fallback): most
# services serve their own document at
# https://<host>/$discovery/rest?version=<v>; a few (e.g. compute) are only
# on the central discovery index at
# https://www.googleapis.com/discovery/v1/apis/<name>/<version>/rest, and a
# few newer ones (e.g. run v2) are only per-host.
#
# Because the revisions are served concurrently, the newest document is not
# necessarily the one this machine's edge returns: the freshness gate has
# repeatedly failed on a hosted runner for a document a local fetch answers with
# the pinned revision, however many times it probes. That gate captures what it
# sampled (SPEC_FRESHNESS_CAPTURE_DIR, uploaded as the gcp-discovery-drift
# artifact), and GCP_DISCOVERY_FROM vendors that file instead of fetching — so
# the refresh records the revision the runner will keep seeing rather than the
# one this edge happens to serve. Prefer it whenever a run captured a document.
#
# Usage:
#   scripts/fetch-gcp-discovery.sh <service-host> <version> [<local-name>] [central]
#   scripts/fetch-gcp-discovery.sh storage.googleapis.com v1
#   scripts/fetch-gcp-discovery.sh run.googleapis.com v2 cloudrun
#   scripts/fetch-gcp-discovery.sh compute.googleapis.com v1 compute central
#   GCP_DISCOVERY_FROM=<captured.json[.gz]> \
#     scripts/fetch-gcp-discovery.sh sqladmin.googleapis.com v1
set -euo pipefail

if [ $# -lt 2 ] || [ $# -gt 4 ]; then
  echo "usage: $0 <service-host> <version> [<local-name>] [central]" >&2
  exit 2
fi

HOST="$1"
VERSION="$2"
NAME="${3:-${HOST%%.googleapis.com}}"
SOURCE="${4:-perhost}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/gcp"
DEST="$DEST_DIR/${NAME}-${VERSION}.discovery.json.gz"
SOURCES="$DEST_DIR/SOURCES.md"
mkdir -p "$DEST_DIR"

case "$SOURCE" in
  perhost)
    URL="https://$HOST/\$discovery/rest?version=$VERSION"
    UPSTREAM_HOST="$HOST"
    UPSTREAM_PATH="\$discovery/rest?version=$VERSION"
    ;;
  central)
    SVC="${HOST%%.googleapis.com}"
    URL="https://www.googleapis.com/discovery/v1/apis/$SVC/$VERSION/rest"
    UPSTREAM_HOST="www.googleapis.com"
    UPSTREAM_PATH="discovery/v1/apis/$SVC/$VERSION/rest"
    ;;
  *)
    echo "error: source must be 'perhost' or 'central', got '$SOURCE'" >&2
    exit 2
    ;;
esac

FROM="${GCP_DISCOVERY_FROM:-}"
PROBES="${GCP_DISCOVERY_PROBES:-3}"
if ! [[ "$PROBES" =~ ^[1-9][0-9]*$ ]]; then
  echo "error: GCP_DISCOVERY_PROBES must be a positive integer, got $PROBES" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT
best=""
best_revision=""

if [ -n "$FROM" ]; then
  [ -f "$FROM" ] || { echo "error: GCP_DISCOVERY_FROM file '$FROM' does not exist" >&2; exit 1; }
  best="$tmpdir/captured.json"
  # The captured document is gzipped when it comes from the drift artifact and
  # plain when it does not; both are accepted, neither is guessed at silently.
  if gzip -dc "$FROM" >"$best" 2>/dev/null; then :; else cp "$FROM" "$best"; fi
  if ! jq -e '.discoveryVersion and .revision and (.resources or .methods)' "$best" >/dev/null 2>&1; then
    echo "error: '$FROM' is not a Discovery document" >&2
    exit 1
  fi
  best_revision="$(jq -r .revision "$best")"
  if ! [[ "$best_revision" =~ ^[0-9]+$ ]]; then
    echo "error: '$FROM' carries invalid revision $best_revision" >&2
    exit 1
  fi
  echo "Vendoring captured $FROM (revision $best_revision) instead of fetching $URL"
  PROBES=0
fi

[ "$PROBES" -gt 0 ] && echo "Fetching $URL ($PROBES probes)"
for ((probe = 1; probe <= PROBES; probe++)); do
  candidate="$tmpdir/probe-$probe.json"
  if ! curl -fsSL -H 'Cache-Control: no-cache' -o "$candidate" "$URL"; then
    echo "warning: probe $probe failed" >&2
    continue
  fi
  if ! jq -e '.discoveryVersion and .revision and (.resources or .methods)' "$candidate" >/dev/null 2>&1; then
    echo "warning: probe $probe did not return a Discovery document" >&2
    continue
  fi
  revision="$(jq -r .revision "$candidate")"
  if ! [[ "$revision" =~ ^[0-9]+$ ]]; then
    echo "warning: probe $probe returned invalid revision $revision" >&2
    continue
  fi
  if [ -z "$best_revision" ] || [ "$revision" -gt "$best_revision" ]; then
    best="$candidate"
    best_revision="$revision"
  fi
done
if [ -z "$best" ]; then
  echo "error: no probe returned a valid Discovery document" >&2
  exit 1
fi
REVISION="$best_revision"

# Refuse to move a pin backwards. Because Google serves several revisions at
# once, a probe can answer with an older document than the one already
# vendored, and writing it would silently regress the pin — the freshness gate
# would then fail against the newer revision another edge still serves, naming
# a drift this script had just created. Set GCP_DISCOVERY_ALLOW_ROLLBACK=1 to
# vendor an older revision deliberately.
if [ -f "$DEST" ]; then
  pinned="$(gzip -dc "$DEST" | jq -r .revision 2>/dev/null || echo "")"
  if [[ "$pinned" =~ ^[0-9]+$ ]] && [ "$REVISION" -lt "$pinned" ]; then
    if [ "${GCP_DISCOVERY_ALLOW_ROLLBACK:-}" = "1" ]; then
      echo "warning: vendoring revision $REVISION over pinned $pinned (rollback allowed)" >&2
    else
      echo "error: $(basename "$DEST") is pinned at revision $pinned and this fetch saw only $REVISION." >&2
      echo "Google serves several revisions concurrently, so this edge is behind rather than the pin" >&2
      echo "being stale. Vendor the document the freshness gate captured instead:" >&2
      echo "  GCP_DISCOVERY_FROM=<captured.json.gz> $0 $*" >&2
      echo "or set GCP_DISCOVERY_ALLOW_ROLLBACK=1 to move the pin backwards deliberately." >&2
      exit 1
    fi
  fi
fi

gzip -9 -n -c "$best" > "$DEST"

bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "gcp" \
  "${NAME}-${VERSION}.discovery.json.gz" "$UPSTREAM_HOST" "$UPSTREAM_PATH" \
  "Apache-2.0" "revision $REVISION"

echo "Vendored $DEST (revision $REVISION, $(wc -c < "$DEST" | tr -d ' ') bytes gzipped)"
