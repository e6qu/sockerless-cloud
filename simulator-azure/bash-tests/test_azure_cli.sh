#!/usr/bin/env bash
#
# Bash-based CLI tests for the Azure simulator.
# Builds the simulator, starts it on a random port, exercises each service
# via "az rest", then prints a pass/fail summary.

PASSES=0
FAILURES=0
SIM_PID=""
BINARY_PATH=""
TMP_DIR=""

cleanup() {
  if [ -n "$SIM_PID" ] && kill -0 "$SIM_PID" 2>/dev/null; then
    kill "$SIM_PID" 2>/dev/null
    wait "$SIM_PID" 2>/dev/null
  fi
  if [ -n "$BINARY_PATH" ] && [ -f "$BINARY_PATH" ]; then
    rm -f "$BINARY_PATH"
  fi
  if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
    rm -rf "$TMP_DIR"
  fi
}
trap cleanup EXIT

# ---- Pre-flight checks ----

for tool in az jq go curl python3; do
  if ! command -v "$tool" &>/dev/null; then
    echo "ERROR: $tool is required but not found in PATH"
    exit 1
  fi
done

# ---- Build simulator ----

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SIM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY_PATH="$SIM_DIR/simulator-azure-bash-test"
TMP_DIR="$(mktemp -d)"

echo "Building simulator..."
(cd "$SIM_DIR" && CGO_ENABLED=0 go build -tags noui -o "$BINARY_PATH" .) || {
  echo "FATAL: failed to build simulator"
  exit 1
}

# ---- Find a free port and start ----

find_free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

PORT=$(find_free_port)
echo "Starting simulator on port $PORT..."

SIM_LISTEN_ADDR=":$PORT" "$BINARY_PATH" &>/dev/null &
SIM_PID=$!

# Wait for health
for i in $(seq 1 50); do
  if curl -sf "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then
    break
  fi
  if [ "$i" -eq 50 ]; then
    echo "FATAL: simulator did not become healthy"
    exit 1
  fi
  sleep 0.1
done
echo "Simulator healthy (pid=$SIM_PID)"

# ---- Constants ----

BASE_URL="http://127.0.0.1:$PORT"
SUB="00000000-0000-0000-0000-000000000001"
RG="bash-test-rg"
AZURE_CONFIG_DIR="$TMP_DIR/azure-config"
export AZURE_CORE_NO_COLOR=1
export AZURE_CLI_DISABLE_CONNECTION_VERIFICATION=1

# ---- Helper functions ----

az_rest() {
  local method="$1" url="$2" body="$3" output="${4:-json}"
  local args=(rest --method "$method" --url "$url" --output "$output")
  if [ -n "$body" ]; then
    args+=(--body "$body")
  fi
  AZURE_CONFIG_DIR="$AZURE_CONFIG_DIR" az "${args[@]}" 2>&1
}

arm_url() {
  local provider="$1" resource="$2" api_version="$3"
  echo "$BASE_URL/subscriptions/$SUB/resourceGroups/$RG/providers/$provider/$resource?api-version=$api_version"
}

rg_url() {
  local name="$1" api_version="$2"
  echo "$BASE_URL/subscriptions/$SUB/resourceGroups/$name?api-version=$api_version"
}

run_test() {
  local name="$1"
  shift
  echo -n "TEST: $name ... "
  # remaining args: method url body output [jq_filter]
  local method="$1" url="$2" body="$3" output_mode="${4:-json}" jq_filter="${5:-}"

  local output
  output=$(az_rest "$method" "$url" "$body" "$output_mode")
  local rc=$?

  if [ $rc -ne 0 ]; then
    echo "FAIL (az rest exit code $rc)"
    echo "  output: $output"
    FAILURES=$((FAILURES + 1))
    return 1
  fi

  # If JSON mode and a jq filter is provided, validate it
  if [ "$output_mode" = "json" ] && [ -n "$jq_filter" ]; then
    if ! echo "$output" | jq -e "$jq_filter" >/dev/null 2>&1; then
      echo "FAIL (jq filter '$jq_filter' did not match)"
      echo "  output: $output"
      FAILURES=$((FAILURES + 1))
      return 1
    fi
  fi

  echo "PASS"
  PASSES=$((PASSES + 1))
  return 0
}

# ============================================================
# Resource Groups
# ============================================================

RG_API="2023-07-01"

echo ""
echo "=== Resource Groups ==="

run_test "resource group create (json)" \
  PUT "$(rg_url "$RG" "$RG_API")" '{"location":"eastus"}' json '.name'

run_test "resource group create (table)" \
  PUT "$(rg_url "${RG}-table" "$RG_API")" '{"location":"eastus"}' table

run_test "resource group get (json)" \
  GET "$(rg_url "$RG" "$RG_API")" "" json '.name'

run_test "resource group get (table)" \
  GET "$(rg_url "$RG" "$RG_API")" "" table

run_test "resource group delete (json)" \
  DELETE "$(rg_url "${RG}-table" "$RG_API")" "" json

# ============================================================
# Container Apps Jobs
# ============================================================

ACA_API="2024-03-01"

echo ""
echo "=== Container Apps Jobs ==="

ACA_JOB_URL="$(arm_url "Microsoft.App" "jobs/bash-test-job" "$ACA_API")"
ACA_JOB_BODY='{
  "location": "eastus",
  "properties": {
    "environmentId": "",
    "configuration": {
      "replicaTimeout": 30,
      "triggerType": "Manual",
      "manualTriggerConfig": { "parallelism": 1, "replicaCompletionCount": 1 }
    },
    "template": {
      "containers": [{
        "name": "app",
        "image": "alpine:latest",
        "command": ["echo", "hello-bash-test"]
      }]
    }
  }
}'

run_test "container apps job create (json)" \
  PUT "$ACA_JOB_URL" "$ACA_JOB_BODY" json '.name'

run_test "container apps job create (table)" \
  PUT "$(arm_url "Microsoft.App" "jobs/bash-test-job-tbl" "$ACA_API")" "$ACA_JOB_BODY" table

run_test "container apps job get (json)" \
  GET "$ACA_JOB_URL" "" json '.name'

run_test "container apps job get (table)" \
  GET "$ACA_JOB_URL" "" table

ACA_JOB_START_URL="$(arm_url "Microsoft.App" "jobs/bash-test-job/start" "$ACA_API")"
run_test "container apps job start (json)" \
  POST "$ACA_JOB_START_URL" "" json '.name'

# Give execution a moment to register
sleep 1

ACA_JOB_EXECS_URL="$(arm_url "Microsoft.App" "jobs/bash-test-job/executions" "$ACA_API")"
run_test "container apps job list executions (json)" \
  GET "$ACA_JOB_EXECS_URL" "" json '.value'

run_test "container apps job list executions (table)" \
  GET "$ACA_JOB_EXECS_URL" "" table

run_test "container apps job delete (json)" \
  DELETE "$ACA_JOB_URL" "" json

run_test "container apps job delete table variant (json)" \
  DELETE "$(arm_url "Microsoft.App" "jobs/bash-test-job-tbl" "$ACA_API")" "" json

# ============================================================
# Azure Functions (Web Apps / Sites)
# ============================================================

FUNC_API="2023-12-01"

echo ""
echo "=== Azure Functions (Sites) ==="

# Create an app service plan first (needed by function apps)
ASP_URL="$(arm_url "Microsoft.Web" "serverfarms/bash-test-plan" "$FUNC_API")"
az_rest PUT "$ASP_URL" '{"location":"eastus","sku":{"name":"Y1","tier":"Dynamic"}}' json >/dev/null 2>&1

FUNC_URL="$(arm_url "Microsoft.Web" "sites/bash-test-funcapp" "$FUNC_API")"
FUNC_BODY='{
  "location": "eastus",
  "kind": "functionapp",
  "properties": {
    "serverFarmId": "/subscriptions/'"$SUB"'/resourceGroups/'"$RG"'/providers/Microsoft.Web/serverfarms/bash-test-plan",
    "siteConfig": {
      "appSettings": [
        {"name": "FUNCTIONS_EXTENSION_VERSION", "value": "~4"},
        {"name": "FUNCTIONS_WORKER_RUNTIME", "value": "node"}
      ]
    }
  }
}'

run_test "function app create (json)" \
  PUT "$FUNC_URL" "$FUNC_BODY" json '.name'

run_test "function app create (table)" \
  PUT "$(arm_url "Microsoft.Web" "sites/bash-test-funcapp-tbl" "$FUNC_API")" "$FUNC_BODY" table

run_test "function app get (json)" \
  GET "$FUNC_URL" "" json '.name'

run_test "function app get (table)" \
  GET "$FUNC_URL" "" table

# List sites in resource group
SITES_LIST_URL="$BASE_URL/subscriptions/$SUB/resourceGroups/$RG/providers/Microsoft.Web/sites?api-version=$FUNC_API"
run_test "function app list sites (json)" \
  GET "$SITES_LIST_URL" "" json '.value'

run_test "function app list sites (table)" \
  GET "$SITES_LIST_URL" "" table

run_test "function app delete (json)" \
  DELETE "$FUNC_URL" "" json

run_test "function app delete table variant (json)" \
  DELETE "$(arm_url "Microsoft.Web" "sites/bash-test-funcapp-tbl" "$FUNC_API")" "" json

# ============================================================
# Storage Accounts
# ============================================================

STORAGE_API="2023-05-01"

echo ""
echo "=== Storage Accounts ==="

STORAGE_URL="$(arm_url "Microsoft.Storage" "storageAccounts/bashtestsa" "$STORAGE_API")"
STORAGE_BODY='{
  "location": "eastus",
  "kind": "StorageV2",
  "sku": { "name": "Standard_LRS" },
  "properties": {}
}'

run_test "storage account create (json)" \
  PUT "$STORAGE_URL" "$STORAGE_BODY" json '.name'

run_test "storage account create (table)" \
  PUT "$(arm_url "Microsoft.Storage" "storageAccounts/bashtestsatbl" "$STORAGE_API")" "$STORAGE_BODY" table

run_test "storage account get (json)" \
  GET "$STORAGE_URL" "" json '.name'

run_test "storage account get (table)" \
  GET "$STORAGE_URL" "" table

run_test "storage account delete (json)" \
  DELETE "$STORAGE_URL" "" json

run_test "storage account delete table variant (json)" \
  DELETE "$(arm_url "Microsoft.Storage" "storageAccounts/bashtestsatbl" "$STORAGE_API")" "" json

# ============================================================
# Monitor (Log Analytics Workspaces)
# ============================================================

MONITOR_API="2022-10-01"

echo ""
echo "=== Monitor (Log Analytics) ==="

MONITOR_URL="$(arm_url "Microsoft.OperationalInsights" "workspaces/bash-test-ws" "$MONITOR_API")"
MONITOR_BODY='{"location":"eastus","properties":{"retentionInDays":30}}'

run_test "monitor workspace create (json)" \
  PUT "$MONITOR_URL" "$MONITOR_BODY" json '.name'

run_test "monitor workspace create (table)" \
  PUT "$(arm_url "Microsoft.OperationalInsights" "workspaces/bash-test-ws-tbl" "$MONITOR_API")" "$MONITOR_BODY" table

run_test "monitor workspace get (json)" \
  GET "$MONITOR_URL" "" json '.name'

run_test "monitor workspace get (table)" \
  GET "$MONITOR_URL" "" table

# KQL query
QUERY_URL="$BASE_URL/v1/workspaces/default/query"
QUERY_BODY='{"query": "ContainerAppConsoleLogs_CL | take 10"}'

run_test "monitor kql query (json)" \
  POST "$QUERY_URL" "$QUERY_BODY" json '.tables'

run_test "monitor kql query (table)" \
  POST "$QUERY_URL" "$QUERY_BODY" table

run_test "monitor workspace delete (json)" \
  DELETE "$MONITOR_URL" "" json

run_test "monitor workspace delete table variant (json)" \
  DELETE "$(arm_url "Microsoft.OperationalInsights" "workspaces/bash-test-ws-tbl" "$MONITOR_API")" "" json

# ============================================================
# App Service Plans (Server Farms)
# ============================================================

ASP_API="2023-12-01"

echo ""
echo "=== App Service Plans ==="

ASP_TEST_URL="$(arm_url "Microsoft.Web" "serverfarms/bash-test-asp" "$ASP_API")"
ASP_BODY='{"location":"eastus","sku":{"name":"B1","tier":"Basic"}}'

run_test "app service plan create (json)" \
  PUT "$ASP_TEST_URL" "$ASP_BODY" json '.name'

run_test "app service plan create (table)" \
  PUT "$(arm_url "Microsoft.Web" "serverfarms/bash-test-asp-tbl" "$ASP_API")" "$ASP_BODY" table

run_test "app service plan get (json)" \
  GET "$ASP_TEST_URL" "" json '.name'

run_test "app service plan get (table)" \
  GET "$ASP_TEST_URL" "" table

run_test "app service plan delete (json)" \
  DELETE "$ASP_TEST_URL" "" json

run_test "app service plan delete table variant (json)" \
  DELETE "$(arm_url "Microsoft.Web" "serverfarms/bash-test-asp-tbl" "$ASP_API")" "" json

# Clean up the plan created for function app tests
az_rest DELETE "$ASP_URL" "" json >/dev/null 2>&1

# Clean up the base resource group
az_rest DELETE "$(rg_url "$RG" "$RG_API")" "" json >/dev/null 2>&1

# ============================================================
# Summary
# ============================================================

echo ""
echo "========================================"
echo "PASSED: $PASSES, FAILED: $FAILURES"
echo "========================================"

if [ "$FAILURES" -gt 0 ]; then
  exit 1
fi
exit 0
