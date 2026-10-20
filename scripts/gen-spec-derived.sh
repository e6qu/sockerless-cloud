#!/usr/bin/env bash
# Regenerate every simulator table compiled from the vendored specifications.
# scripts/refresh-drifted-specs.sh runs it after re-vendoring, so a refresh
# lands together with the tables it changes.
#
# Usage: scripts/gen-spec-derived.sh
set -euo pipefail

cd "$(dirname "$0")/.."
go run scripts/gen-cloudwatch-cbor-shapes.go
go run scripts/gen-aws-error-message-members.go
go run scripts/generate-sfn-rest-integrations.go
bash scripts/gen-aws-iam-resource-types.sh
