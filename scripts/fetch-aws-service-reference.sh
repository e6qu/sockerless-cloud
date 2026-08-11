#!/usr/bin/env bash
# Vendor (or refresh) one AWS Service Reference document into
# specs/cloud-api/aws/service-reference/.
#
# AWS publishes the Service Authorization Reference — which IAM actions each
# service defines, and which resource types each action authorizes against — as
# machine-readable JSON at servicereference.us-east-1.amazonaws.com. It is the
# authoritative answer to "what ARN does a caller's request name?", which is
# what the simulator's call-time IAM gate has to derive: an action authorized
# against the wrong resource, or against a literal "*" when the service defines
# a resource type, denies every resource-scoped grant written for it.
#
# The Smithy models vendored alongside these do not carry that information for
# most services (only Amazon ECS and AWS Lambda ship the aws.iam#iamAction
# trait), so the two vendored corpora are complementary, not redundant.
#
# Usage:
#   scripts/fetch-aws-service-reference.sh <service> [<service>...]
#   scripts/fetch-aws-service-reference.sh logs ecs codebuild
#
# <service> is the IAM service prefix (the "service" key in the published
# index), which is also the filename stem. The index's per-service `modified`
# timestamp is recorded as the pin in
# specs/cloud-api/aws/SERVICE_REFERENCE_SOURCES.md so drift is detectable
# without re-downloading every document.
set -euo pipefail

if [ $# -lt 1 ]; then
  echo "usage: $0 <service> [<service>...]" >&2
  exit 2
fi

HOST="servicereference.us-east-1.amazonaws.com"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST_DIR="$ROOT/specs/cloud-api/aws/service-reference"
SOURCES="$ROOT/specs/cloud-api/aws/SERVICE_REFERENCE_SOURCES.md"
mkdir -p "$DEST_DIR"

index="$(mktemp)"
tmp="$(mktemp)"
trap 'rm -f "$index" "$tmp"' EXIT

echo "Fetching service index from $HOST"
curl -fsSL -o "$index" "https://$HOST/"
if ! jq -e 'type == "array" and length > 0' "$index" >/dev/null 2>&1; then
  echo "error: service index is not a non-empty array" >&2
  exit 1
fi

for SERVICE in "$@"; do
  url="$(jq -er --arg s "$SERVICE" '.[] | select(.service == $s) | .url' "$index")"
  modified="$(jq -er --arg s "$SERVICE" '.[] | select(.service == $s) | .modified' "$index")"
  if [ -z "$url" ] || [ -z "$modified" ]; then
    echo "error: $SERVICE is not in the published service index" >&2
    exit 1
  fi

  echo "Fetching $SERVICE (modified $modified)"
  curl -fsSL -o "$tmp" "$url"

  # A Service Reference document names its service and carries the Actions
  # array the gate reads; anything else is a fetch that silently succeeded
  # against the wrong resource.
  if ! jq -e --arg s "$SERVICE" '.Name == $s and (.Actions | type == "array") and (.Actions | length > 0)' "$tmp" >/dev/null 2>&1; then
    echo "error: downloaded file is not the $SERVICE Service Reference document" >&2
    exit 1
  fi

  gzip -9 -n -c "$tmp" > "$DEST_DIR/${SERVICE}.servicereference.json.gz"

  bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" "aws" \
    "service-reference/${SERVICE}.servicereference.json.gz" "$HOST" \
    "v1/${SERVICE}/${SERVICE}.json" "AWS Service Reference (public service authorization data)" \
    "modified${modified}"

  echo "Vendored $DEST_DIR/${SERVICE}.servicereference.json.gz ($(wc -c < "$DEST_DIR/${SERVICE}.servicereference.json.gz" | tr -d ' ') bytes gzipped)"
done
