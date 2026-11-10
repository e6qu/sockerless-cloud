#!/usr/bin/env bash
# Vendor public records of what Amazon CloudWatch answered real clients, where
# no AWS document states it:
#
# - LocalStack's CloudWatch snapshot file, whose entries its maintainers
#   record against AWS (`@markers.aws.validated`): a new anomaly detector on a
#   metric without data describes as PENDING_TRAINING, without
#   MetricCharacteristics. The row pins the commit that last touched the file.
# - Grafana community posts that quote CloudWatch's GetMetricData refusals
#   verbatim. Discourse serves each post's Markdown at /raw/<topic>/<post>; the
#   row pins its SHA-256, which scripts/check-spec-freshness.sh compares
#   against the live post.
#
# Usage: scripts/fetch-cloudwatch-captures.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/specs/cloud-api/aws"
SOURCES="$DEST/SOURCES.md"
row() { bash "$ROOT/scripts/spec-sources-row.sh" "$SOURCES" aws "$@"; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

snapshot=tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json
git clone --quiet --filter=blob:none --no-checkout --single-branch https://github.com/localstack/localstack "$work/localstack"
sha="$(git -C "$work/localstack" log -1 --format=%H -- "$snapshot")"
git -C "$work/localstack" show "$sha:$snapshot" | gzip -9 -n >"$DEST/localstack-cloudwatch-snapshot.json.gz"
row localstack-cloudwatch-snapshot.json.gz localstack/localstack "$snapshot" Apache-2.0 "$sha"

# topic/post and what CloudWatch answered in it:
#   105652/1  Unsupported operand type(s) for PERIOD: '[Array[TimeSeries]]'
#   115805/1  Invalid syntax. 'ANOMALY_DETECTION_BAND' is not a valid aggregate function
#   115805/3  Unsupported operand type(s) for ANOMALY_DETECTION_BAND: '[Array[TimeSeries], Scalar]'
#   126448/1  ID 't5xx' not found
for post in 105652/1 115805/1 115805/3 126448/1; do
  name="grafana-community-${post/\//-}.md.gz"
  curl -sSfL --retry 3 -o "$work/post.md" "https://community.grafana.com/raw/$post"
  gzip -9 -n -c "$work/post.md" >"$DEST/$name"
  row "$name" community.grafana.com "raw/$post" Grafana-Community-ToS "sha256$(sha256sum "$work/post.md" | cut -d' ' -f1)"
done
