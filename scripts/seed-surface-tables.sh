#!/usr/bin/env bash
# Generate per-service surface-table stubs from registered HandleFunc
# patterns. Each stub lists what the sim already handles (✓ rows); ✗
# rows for missing ops + sdk-test / tf-test columns are filled in by
# subsequent per-surface PRs.
#
# Usage: bash scripts/seed-surface-tables.sh
#
# Idempotent — re-running overwrites the stubs with fresh extracts but
# preserves the hand-written sections inside the `<!-- HAND-WRITTEN BEGIN -->`
# / `<!-- HAND-WRITTEN END -->` block.

set -euo pipefail

# Filename glob order follows LC_COLLATE, and en_US.UTF-8 (common macOS
# default) sorts `file_serverless.go` before `file.go` while C.UTF-8 (hosted
# runners) sorts it after. Pin C collation so the generated row order is
# identical on every operator machine and in CI.
export LC_ALL=C

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${SEED_SURFACE_TABLES_OUT_DIR:-$REPO_ROOT/specs/SIM_SURFACE_TABLES}"
MATRIX="$REPO_ROOT/specs/SIM_TEST_COVERAGE_MATRIX.md"
mkdir -p "$OUT_DIR"

# Tables written by hand. The seeder leaves these alone.
PRESERVED_TABLES=(
  aws-s3-bucket-subresources
  azure-kv-data-plane
)

is_preserved() {
  local name="$1"
  for p in "${PRESERVED_TABLES[@]}"; do
    [[ "$name" == "$p" ]] && return 0
  done
  return 1
}

matrix_cell() {
  local table="$1"
  local column="$2"
  local value
  value="$(awk -v table="$table" -v column="$column" -F'|' '
    $2 ~ "`" table "`" {
      gsub(/^ +| +$/, "", $3)
      gsub(/^ +| +$/, "", $4)
      gsub(/^ +| +$/, "", $5)
      if (column == "sdk") print $3
      if (column == "tf") print $5
      exit
    }
  ' "$MATRIX")"
  case "$value" in
    direct) printf "✓ (direct; see coverage matrix)" ;;
    "not applicable") printf "n/a (not exposed by provider; see coverage matrix)" ;;
    tracked*) printf "✗ (%s; see coverage matrix)" "$value" ;;
    *) printf "✗ (coverage matrix row missing)" ;;
  esac
}

# Infra / runtime files that don't represent a sim service surface.
# Listed inline below in the [[ =~ ]] check; this comment block keeps
# the catalog readable: main, dashboard, metadata, streaming, awschunked,
# aws_identity, auth, authorization, managedidentity, oauth2, operations,
# quota, serviceusage, ui_embed, ui_noembed, lambda_runtime, ssm_proto,
# logfilter, cloudwatch_metrics, kql.

# Map cloud:file → canonical surface-table file.
table_for_file() {
  case "$1:$2" in
    aws:s3|aws:s3_subresources) echo "aws-s3" ;;
    aws:ecs|aws:ecs_service) echo "aws-ecs" ;;
    aws:application_autoscaling) echo "aws-application-autoscaling" ;;
    aws:lambda|aws:lambda_subresources) echo "aws-lambda" ;;
    aws:cloudfront|aws:cloudfront_functions|aws:cloudfront_keys|aws:cloudfront_policies) echo "aws-cloudfront" ;;
    aws:amplify|aws:amplify_domains) echo "aws-amplify" ;;
    aws:iam|aws:iam_slr_oidc) echo "aws-iam" ;;
    aws:apigatewayv2*) echo "aws-apigatewayv2" ;;
    aws:apigateway*) echo "aws-apigateway" ;;
    aws:cloudwatch*) echo "aws-cloudwatch" ;;
    aws:codebuild*) echo "aws-codebuild" ;;
    aws:ec2*) echo "aws-ec2" ;;
    aws:ecr*) echo "aws-ecr" ;;
    aws:ecs*) echo "aws-ecs" ;;
    aws:elasticache*) echo "aws-elasticache" ;;
    aws:elbv2*) echo "aws-elbv2" ;;
    aws:eventbridge*) echo "aws-eventbridge" ;;
    aws:glue*) echo "aws-glue" ;;
    aws:iam*) echo "aws-iam" ;;
    aws:kinesis*) echo "aws-kinesis" ;;
    aws:kms*) echo "aws-kms" ;;
    aws:lambda*) echo "aws-lambda" ;;
    aws:rds*) echo "aws-rds" ;;
    aws:sns*) echo "aws-sns" ;;
    aws:ssm*) echo "aws-ssm_parameters" ;;
    azure:apim*) echo "azure-apim" ;;
    azure:blob|azure:files|azure:storage_dataplane) echo "azure-storage" ;;
    azure:containerapps|azure:containerapps_apps|azure:containerappsenv) echo "azure-containerapps" ;;
    azure:servicebus|azure:servicebus_dataplane) echo "azure-servicebus" ;;
    azure:insights|azure:monitor) echo "azure-monitor" ;;
    azure:subscription|azure:subscription_alias) echo "azure-subscription" ;;
    azure:cosmos*) echo "azure-cosmos" ;;
    azure:entra*) echo "azure-entra" ;;
    azure:eventgrid*) echo "azure-eventgrid" ;;
    azure:logicapps*) echo "azure-logicapps" ;;
    azure:postgres*) echo "azure-postgresql-flexible-server" ;;
    azure:resourcesarm) echo "azure-resources" ;;
    azure:storagearm) echo "azure-storage" ;;
    azure:web*) echo "azure-functions" ;;
    gcp:cloudrun|gcp:cloudrunjobs|gcp:cloudrunservices) echo "gcp-cloudrun" ;;
    gcp:cloudrun*) echo "gcp-cloudrun" ;;
    gcp:compute_more*) echo "gcp-compute" ;;
    gcp:logging_admin) echo "gcp-logging" ;;
    gcp:token_signing) echo "gcp-iam" ;;
    *) echo "$1-$2" ;;
  esac
}

# Pass 1 — collect all (table, source-file, line, method, path, handler) rows.
tmp_rows="$(mktemp)"
trap 'rm -f "$tmp_rows"' EXIT

for cloud in aws azure gcp; do
  for go_file in "$REPO_ROOT/simulator-$cloud"/*.go; do
    [[ -f "$go_file" ]] || continue
    [[ "$go_file" =~ _test\.go$ ]] && continue
    base="$(basename "$go_file" .go)"
    if [[ "$base" =~ ^(main|dashboard|metadata|streaming|awschunked|aws_identity|auth|authorization|managedidentity|oauth2|operations|quota|serviceusage|ui_embed|ui_noembed|lambda_runtime|ssm_proto|logfilter|cloudwatch_metrics|kql|arm_lro)$ ]]; then
      continue
    fi
    has_handle="$(grep -Ec 'HandleFunc\(|\.Register\("|\.RegisterVersioned\(' "$go_file" || true)"
    [[ "$has_handle" == "0" ]] && continue
    table_name="$(table_for_file "$cloud" "$base")"
    is_preserved "$table_name" && continue

    # REST-style routes: mux.HandleFunc("METHOD /pattern", handler).
    { grep -nE '\.HandleFunc\(' "$go_file" || true; } \
      | { sed -E 's/^([0-9]+):.*HandleFunc\("([A-Z]+) ([^"]+)",[[:space:]]*([a-zA-Z0-9_.]+).*$/\1\t\2 \3\t\4/' || true; } \
      | { grep -E '^[0-9]+	[A-Z]+ ' || true; } \
      | while IFS=$'\t' read -r line route handler; do
          printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$table_name" "$cloud" "$base" "$line" "$route" "$handler"
        done

    # AWS awsJson1.1 / awsQuery actions:
    #   r.Register("Service.Action", handler)
    #   r.RegisterVersioned("YYYY-MM-DD", "Action", handler)
    { grep -nE '\.Register\("' "$go_file" || true; } \
      | { sed -E 's/^([0-9]+):.*\.Register\("([^"]+)",[[:space:]]*([a-zA-Z0-9_.]+).*$/\1\t\2\t\3/' || true; } \
      | { grep -E '^[0-9]+	[A-Za-z]' || true; } \
      | while IFS=$'\t' read -r line action handler; do
          printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$table_name" "$cloud" "$base" "$line" "Action $action" "$handler"
        done

    { grep -nE '\.RegisterVersioned\(' "$go_file" || true; } \
      | { sed -E 's/^([0-9]+):.*\.RegisterVersioned\([^,]+,[[:space:]]*"([^"]+)",[[:space:]]*([a-zA-Z0-9_.]+).*$/\1\t\2\t\3/' || true; } \
      | { grep -E '^[0-9]+	[A-Za-z]' || true; } \
      | while IFS=$'\t' read -r line action handler; do
          printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$table_name" "$cloud" "$base" "$line" "Action $action" "$handler"
        done
  done
done > "$tmp_rows"

# Pass 2 — for each table, emit a stub with all collected rows.
tables=()
while IFS= read -r t; do
  tables+=("$t")
done < <(cut -f1 "$tmp_rows" | sort -u)

for table_name in "${tables[@]}"; do
  is_preserved "$table_name" && continue
  out_md="$OUT_DIR/$table_name.md"

  # Preserve existing hand-written block, if present.
  hand_block=""
  if [[ -f "$out_md" ]]; then
    hand_block="$(awk '/<!-- HAND-WRITTEN BEGIN -->/,/<!-- HAND-WRITTEN END -->/' "$out_md" || true)"
  fi
  if [[ -z "$hand_block" ]]; then
    hand_block=$'<!-- HAND-WRITTEN BEGIN -->\n<!-- HAND-WRITTEN END -->'
  fi

  # First source file for this table — used in the header path hint.
  first_file="$(awk -v t="$table_name" -F'\t' '$1==t {print $2"/"$3".go"; exit}' "$tmp_rows")"

  {
    echo "# Sim surface — $table_name"
    echo
    echo "Surface registered in \`simulator-$first_file\` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by \`scripts/seed-surface-tables.sh\` from \`mux.HandleFunc(...)\` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them."
    echo
    echo "## Status legend"
    echo
    echo "- ✓ — implemented + tested"
    echo "- ✗ — missing (paired with an open BUG or issue; never silent)"
    echo "- 501 — stubbed NotImplemented (wire-visible gap)"
    echo "- n/a — no meaningful client/provider surface for this op"
    echo
    echo "## Implemented ops (extracted from HandleFunc registrations)"
    echo
    echo "| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |"
    echo "|---|---|---|---|---|---|"
    sdk_cell="$(matrix_cell "$table_name" sdk)"
    tf_cell="$(matrix_cell "$table_name" tf)"
    awk -v t="$table_name" -v sdk="$sdk_cell" -v tf="$tf_cell" -F'\t' '$1==t {printf "| `%s` | ✓ `simulator-%s/%s.go:%s::%s` | %s | %s | n/a | |\n", $5, $2, $3, $4, $6, sdk, tf}' "$tmp_rows"
    echo
    echo "## Coverage status"
    echo
    echo "- Row-level SDK/Terraform cells summarize the maintained coverage matrix in \`specs/SIM_TEST_COVERAGE_MATRIX.md\`; detailed client files and client-family \`n/a\` decisions live there."
    echo "- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit."
    echo
    echo "$hand_block"
  } > "$out_md"
  n="$(awk -v t="$table_name" -F'\t' '$1==t' "$tmp_rows" | wc -l | tr -d ' ')"
  echo "seeded: $out_md ($n ops)"
done

# Rewrite README index.
{
  echo "# Sim surface tables"
  echo
  echo "Per-service canonical-operation enumerations for every sim surface. Each table lists implemented ops (✓ rows) extracted by the seeder + ✗ rows for missing ops added by subsequent PRs. The companion skill \`.claude/skills/surface-table-completeness/SKILL.md\` enforces \"no silent ✗ rows\" — every gap is paired with an open BUG or issue."
  echo
  echo "The row-level SDK/Terraform cells summarize the maintained coverage index in [\`../SIM_TEST_COVERAGE_MATRIX.md\`](../SIM_TEST_COVERAGE_MATRIX.md). Use that matrix for the exact SDK, CLI, and Terraform evidence files and for client-family \`n/a\` decisions."
  echo
  echo "Re-run \`bash scripts/seed-surface-tables.sh\` after adding new \`HandleFunc\` registrations to refresh the implemented-ops sections (hand-written sections inside \`<!-- HAND-WRITTEN BEGIN/END -->\` are preserved)."
  echo
  echo "## Index"
  echo
  for f in "$OUT_DIR"/*.md; do
    name="$(basename "$f" .md)"
    [[ "$name" == "README" ]] && continue
    echo "- [\`$name\`]($name.md)"
  done
} > "$OUT_DIR/README.md"
echo "index: $OUT_DIR/README.md"
