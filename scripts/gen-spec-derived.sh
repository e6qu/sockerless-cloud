#!/usr/bin/env bash
# Regenerate every simulator table compiled from the vendored specifications.
# scripts/refresh-drifted-specs.sh runs it after re-vendoring, so a refresh
# lands together with the tables it changes.
#
# Usage: scripts/gen-spec-derived.sh
set -euo pipefail

cd "$(dirname "$0")/.."
go run scripts/gen-cloudwatch-cbor-shapes.go
go run scripts/gen-cloudwatch-cbor-shapes.go pi com.amazonaws.pi pi simulator-aws/pi_cbor_shapes_gen.go
go run scripts/gen-aws-service-quotas.go
go run scripts/gen-cloudwatch-logs-ocsf.go
go run scripts/gen-cloudwatch-logs-cldr.go
go run scripts/gen-aws-error-message-members.go
go run scripts/generate-sfn-rest-integrations.go
bash scripts/gen-aws-iam-resource-types.sh
go run scripts/gen-gcp-audit-methods.go
go run scripts/gen-gcp-iam-catalog.go
