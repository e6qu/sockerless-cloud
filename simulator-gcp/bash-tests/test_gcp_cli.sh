#!/usr/bin/env bash
#
# Bash-based CLI tests for the GCP simulator.
# Tests Cloud DNS, Service Usage, Cloud Logging, Cloud Run Jobs, and GCS
# using both gcloud CLI and direct curl calls.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SIM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY_PATH="$SIM_DIR/simulator-gcp"
TMP_DIR="$SCRIPT_DIR/tmp-$$"
GCLOUD_CONFIG_DIR="$TMP_DIR/gcloud-config"

PROJECT="test-project"
LOCATION="us-central1"

PASSED=0
FAILED=0
SIM_PID=""

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

cleanup() {
    if [ -n "$SIM_PID" ] && kill -0 "$SIM_PID" 2>/dev/null; then
        kill "$SIM_PID" 2>/dev/null
        wait "$SIM_PID" 2>/dev/null
    fi
    rm -rf "$TMP_DIR"
    rm -f "$BINARY_PATH"
}
trap cleanup EXIT

pass() {
    PASSED=$((PASSED + 1))
    echo "  PASS"
}

fail() {
    FAILED=$((FAILED + 1))
    echo "  FAIL: $1"
}

# Run a gcloud command with simulator overrides.
run_gcloud() {
    CLOUDSDK_CONFIG="$GCLOUD_CONFIG_DIR" \
    CLOUDSDK_AUTH_ACCESS_TOKEN="fake-gcp-token" \
    CLOUDSDK_CORE_PROJECT="$PROJECT" \
    CLOUDSDK_CORE_DISABLE_PROMPTS="1" \
    CLOUDSDK_API_ENDPOINT_OVERRIDES_DNS="$BASE_URL/" \
    CLOUDSDK_API_ENDPOINT_OVERRIDES_LOGGING="$BASE_URL/" \
    CLOUDSDK_API_ENDPOINT_OVERRIDES_CLOUDFUNCTIONS="$BASE_URL/" \
    CLOUDSDK_API_ENDPOINT_OVERRIDES_SERVICEUSAGE="$BASE_URL/" \
    CLOUDSDK_API_ENDPOINT_OVERRIDES_VPCACCESS="$BASE_URL/" \
    gcloud "$@" 2>&1
}

# Perform an HTTP request against the simulator.
sim_curl() {
    local method="$1"
    local url="$2"
    local body="$3"

    if [ -n "$body" ]; then
        curl -s -X "$method" "$url" \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer fake-gcp-token" \
            -d "$body"
    else
        curl -s -X "$method" "$url" \
            -H "Authorization: Bearer fake-gcp-token"
    fi
}

# sim_curl that also checks HTTP status code via -w.
sim_curl_status() {
    local method="$1"
    local url="$2"
    local body="$3"

    if [ -n "$body" ]; then
        curl -s -o /dev/null -w "%{http_code}" -X "$method" "$url" \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer fake-gcp-token" \
            -d "$body"
    else
        curl -s -o /dev/null -w "%{http_code}" -X "$method" "$url" \
            -H "Authorization: Bearer fake-gcp-token"
    fi
}

# Find a free TCP port.
find_free_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()'
}

# Wait for the simulator /health endpoint to return 200.
wait_for_health() {
    local url="$1"
    for i in $(seq 1 50); do
        if curl -s -o /dev/null -w "%{http_code}" "$url" 2>/dev/null | grep -q "200"; then
            return 0
        fi
        sleep 0.1
    done
    return 1
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------

for tool in gcloud curl jq python3 go; do
    if ! command -v "$tool" &>/dev/null; then
        echo "ERROR: $tool is required but not found in PATH"
        exit 1
    fi
done

# ---------------------------------------------------------------------------
# Build simulator
# ---------------------------------------------------------------------------

echo "Building GCP simulator..."
(cd "$SIM_DIR" && CGO_ENABLED=0 go build -tags noui -o "$BINARY_PATH" .)
if [ $? -ne 0 ]; then
    echo "FATAL: Failed to build simulator"
    exit 1
fi

# ---------------------------------------------------------------------------
# Start simulator
# ---------------------------------------------------------------------------

PORT=$(find_free_port)
BASE_URL="http://127.0.0.1:$PORT"

mkdir -p "$GCLOUD_CONFIG_DIR"

SIM_LISTEN_ADDR=":$PORT" "$BINARY_PATH" &
SIM_PID=$!

echo "Waiting for simulator on port $PORT..."
if ! wait_for_health "$BASE_URL/health"; then
    echo "FATAL: Simulator did not become healthy"
    exit 1
fi
echo "Simulator ready (PID $SIM_PID)"
echo ""

# ===========================================================================
# Cloud DNS Tests
# ===========================================================================

echo "=== Cloud DNS ==="

# --- create zone (text) ---
echo "TEST: dns managed-zones create (text)"
output=$(run_gcloud dns managed-zones create bash-test-zone \
    --dns-name=bash-test.example.com. \
    --description="Bash test zone" \
    --visibility=private \
    --networks=)
if [ $? -eq 0 ]; then
    pass
else
    fail "gcloud dns managed-zones create failed: $output"
fi

# --- describe zone (json) ---
echo "TEST: dns managed-zones describe (json)"
output=$(run_gcloud dns managed-zones describe bash-test-zone --format=json)
if [ $? -eq 0 ] && echo "$output" | jq -e '.name == "bash-test-zone"' >/dev/null 2>&1; then
    pass
else
    fail "expected name=bash-test-zone in JSON output: $output"
fi

# --- list zones (text) ---
echo "TEST: dns managed-zones list (text)"
output=$(run_gcloud dns managed-zones list)
if [ $? -eq 0 ] && echo "$output" | grep -q "bash-test-zone"; then
    pass
else
    fail "expected bash-test-zone in list output: $output"
fi

# --- list zones (json) ---
echo "TEST: dns managed-zones list (json)"
output=$(run_gcloud dns managed-zones list --format=json)
if [ $? -eq 0 ] && echo "$output" | jq -e '.[0].name' >/dev/null 2>&1; then
    pass
else
    fail "expected valid JSON array in list output: $output"
fi

# --- create record set via curl (json) ---
echo "TEST: dns record-sets create via curl (json)"
output=$(sim_curl POST "$BASE_URL/dns/v1/projects/$PROJECT/managedZones/bash-test-zone/rrsets" \
    '{"name":"host.bash-test.example.com.","type":"A","ttl":300,"rrdatas":["10.0.0.1"]}')
if echo "$output" | jq -e '.name == "host.bash-test.example.com."' >/dev/null 2>&1; then
    pass
else
    fail "expected name in record-set JSON: $output"
fi

# --- list record sets via curl (json) ---
echo "TEST: dns record-sets list via curl (json)"
output=$(sim_curl GET "$BASE_URL/dns/v1/projects/$PROJECT/managedZones/bash-test-zone/rrsets")
if echo "$output" | jq -e '.rrsets | length > 0' >/dev/null 2>&1; then
    pass
else
    fail "expected non-empty rrsets array: $output"
fi

# --- delete zone (text) ---
echo "TEST: dns managed-zones delete (text)"
output=$(run_gcloud dns managed-zones delete bash-test-zone)
if [ $? -eq 0 ]; then
    pass
else
    fail "gcloud dns managed-zones delete failed: $output"
fi

# --- verify zone is gone ---
echo "TEST: dns managed-zones describe after delete (expect failure)"
output=$(run_gcloud dns managed-zones describe bash-test-zone --format=json)
if [ $? -ne 0 ]; then
    pass
else
    fail "expected describe to fail after deletion"
fi

# ===========================================================================
# Service Usage Tests
# ===========================================================================

echo ""
echo "=== Service Usage ==="

# --- enable service via curl (json) ---
echo "TEST: services enable via curl (json)"
output=$(sim_curl POST "$BASE_URL/v1/projects/$PROJECT/services/compute.googleapis.com:enable" '{}')
http_status=$(sim_curl_status POST "$BASE_URL/v1/projects/$PROJECT/services/storage.googleapis.com:enable" '{}')
if [ "$http_status" -ge 200 ] && [ "$http_status" -lt 300 ]; then
    pass
else
    fail "enable service returned status $http_status"
fi

# --- get service state (json) ---
echo "TEST: services get state via curl (json)"
output=$(sim_curl GET "$BASE_URL/v1/projects/$PROJECT/services/compute.googleapis.com")
if echo "$output" | jq -e '.state == "ENABLED"' >/dev/null 2>&1; then
    pass
else
    fail "expected state=ENABLED: $output"
fi

# --- list services via curl (json) ---
echo "TEST: services list via curl (json)"
output=$(sim_curl GET "$BASE_URL/v1/projects/$PROJECT/services")
if echo "$output" | jq -e '.services | length > 0' >/dev/null 2>&1; then
    pass
else
    fail "expected non-empty services list: $output"
fi

# --- enable service via gcloud (text) ---
echo "TEST: gcloud services enable (text)"
output=$(run_gcloud services enable dns.googleapis.com)
if [ $? -eq 0 ]; then
    pass
else
    fail "gcloud services enable failed: $output"
fi

# --- list services via gcloud (json) ---
echo "TEST: gcloud services list (json)"
output=$(run_gcloud services list --format=json)
if [ $? -eq 0 ] && echo "$output" | jq -e 'length > 0' >/dev/null 2>&1; then
    pass
else
    fail "expected non-empty JSON services list: $output"
fi

# --- disable service via curl (json) ---
echo "TEST: services disable via curl (json)"
sim_curl POST "$BASE_URL/v1/projects/$PROJECT/services/storage.googleapis.com:disable" '{}' >/dev/null
output=$(sim_curl GET "$BASE_URL/v1/projects/$PROJECT/services/storage.googleapis.com")
if echo "$output" | jq -e '.state == "DISABLED"' >/dev/null 2>&1; then
    pass
else
    fail "expected state=DISABLED after disable: $output"
fi

# ===========================================================================
# Cloud Logging Tests (curl only)
# ===========================================================================

echo ""
echo "=== Cloud Logging ==="

# --- write log entries (json) ---
echo "TEST: logging entries:write via curl (json)"
http_status=$(sim_curl_status POST "$BASE_URL/v2/entries:write" "$(cat <<JSONEOF
{
  "logName": "projects/$PROJECT/logs/bash-test-log",
  "resource": {"type": "global"},
  "entries": [
    {"textPayload": "Hello from bash test", "severity": "INFO"},
    {"textPayload": "Warning from bash test", "severity": "WARNING"}
  ]
}
JSONEOF
)")
if [ "$http_status" -ge 200 ] && [ "$http_status" -lt 300 ]; then
    pass
else
    fail "entries:write returned status $http_status"
fi

# --- list log entries (json) ---
echo "TEST: logging entries:list via curl (json)"
output=$(sim_curl POST "$BASE_URL/v2/entries:list" "$(cat <<JSONEOF
{
  "resourceNames": ["projects/$PROJECT"],
  "filter": "bash-test-log"
}
JSONEOF
)")
if echo "$output" | jq -e '.entries | length >= 2' >/dev/null 2>&1; then
    pass
else
    fail "expected at least 2 log entries: $output"
fi

# --- verify log entry content (json) ---
echo "TEST: logging entries contain expected text (json)"
if echo "$output" | jq -e '[.entries[].textPayload] | index("Hello from bash test")' >/dev/null 2>&1; then
    pass
else
    fail "expected 'Hello from bash test' in log entries: $output"
fi

# --- list with filter (json) ---
echo "TEST: logging entries:list with severity filter (json)"
# Write an error entry first
sim_curl POST "$BASE_URL/v2/entries:write" "$(cat <<JSONEOF
{
  "logName": "projects/$PROJECT/logs/bash-filter-log",
  "resource": {"type": "global"},
  "entries": [
    {"textPayload": "ERROR something broke", "severity": "ERROR"},
    {"textPayload": "INFO all good", "severity": "INFO"}
  ]
}
JSONEOF
)" >/dev/null
output=$(sim_curl POST "$BASE_URL/v2/entries:list" "$(cat <<JSONEOF
{
  "resourceNames": ["projects/$PROJECT"],
  "filter": "ERROR"
}
JSONEOF
)")
if echo "$output" | jq -e '.entries | length >= 1' >/dev/null 2>&1; then
    pass
else
    fail "expected at least 1 entry matching ERROR filter: $output"
fi

# ===========================================================================
# Cloud Run Jobs Tests (curl only)
# ===========================================================================

echo ""
echo "=== Cloud Run Jobs ==="

JOBS_BASE="$BASE_URL/v2/projects/$PROJECT/locations/$LOCATION/jobs"

# --- create job (json) ---
echo "TEST: cloud run jobs create via curl (json)"
output=$(sim_curl POST "$JOBS_BASE?jobId=bash-test-job" "$(cat <<JSONEOF
{
  "template": {
    "taskCount": 1,
    "template": {
      "containers": [{
        "name": "app",
        "image": "alpine:latest",
        "command": ["echo", "hello-from-bash"]
      }],
      "maxRetries": 0,
      "timeout": "10s"
    }
  }
}
JSONEOF
)")
if echo "$output" | jq -e '.name' >/dev/null 2>&1; then
    pass
else
    fail "expected job name in create response: $output"
fi

# --- get job (json) ---
echo "TEST: cloud run jobs get via curl (json)"
output=$(sim_curl GET "$JOBS_BASE/bash-test-job")
if echo "$output" | jq -e '.name | endswith("bash-test-job")' >/dev/null 2>&1; then
    pass
else
    fail "expected job name ending with bash-test-job: $output"
fi

# --- get job (text — raw curl output) ---
echo "TEST: cloud run jobs get via curl (text)"
http_status=$(sim_curl_status GET "$JOBS_BASE/bash-test-job")
if [ "$http_status" = "200" ]; then
    pass
else
    fail "expected HTTP 200 for get job, got $http_status"
fi

# --- run job (json) ---
echo "TEST: cloud run jobs run via curl (json)"
output=$(sim_curl POST "$JOBS_BASE/bash-test-job:run" "")
exec_name=""
if echo "$output" | jq -e '.response.name' >/dev/null 2>&1; then
    exec_name=$(echo "$output" | jq -r '.response.name')
    pass
else
    fail "expected execution name in run response: $output"
fi

# --- get execution (json) ---
if [ -n "$exec_name" ]; then
    echo "TEST: cloud run executions get via curl (json)"
    # Wait for execution to complete
    sleep 3
    output=$(sim_curl GET "$BASE_URL/v2/$exec_name")
    if echo "$output" | jq -e '.name' >/dev/null 2>&1; then
        pass
    else
        fail "expected execution name in get response: $output"
    fi

    echo "TEST: cloud run execution completed successfully (json)"
    succeeded=$(echo "$output" | jq -r '.succeededCount // 0')
    if [ "$succeeded" -ge 1 ]; then
        pass
    else
        fail "expected succeededCount >= 1: $output"
    fi
fi

# --- create second job for deletion test ---
echo "TEST: cloud run jobs create for delete test (json)"
output=$(sim_curl POST "$JOBS_BASE?jobId=bash-delete-job" "$(cat <<JSONEOF
{
  "template": {
    "taskCount": 1,
    "template": {
      "containers": [{
        "name": "app",
        "image": "alpine:latest",
        "command": ["echo", "bye"]
      }],
      "maxRetries": 0,
      "timeout": "10s"
    }
  }
}
JSONEOF
)")
if echo "$output" | jq -e '.name' >/dev/null 2>&1; then
    pass
else
    fail "create for delete test failed: $output"
fi

# --- delete job (json) ---
echo "TEST: cloud run jobs delete via curl (json)"
http_status=$(sim_curl_status DELETE "$JOBS_BASE/bash-delete-job")
if [ "$http_status" -ge 200 ] && [ "$http_status" -lt 300 ]; then
    pass
else
    fail "delete job returned status $http_status"
fi

# --- verify deleted job is gone ---
echo "TEST: cloud run jobs get after delete (expect 404)"
http_status=$(sim_curl_status GET "$JOBS_BASE/bash-delete-job")
if [ "$http_status" = "404" ]; then
    pass
else
    fail "expected 404 for deleted job, got $http_status"
fi

# --- cleanup remaining job ---
sim_curl DELETE "$JOBS_BASE/bash-test-job" >/dev/null 2>&1

# ===========================================================================
# GCS Tests (curl only)
# ===========================================================================

echo ""
echo "=== GCS (Cloud Storage) ==="

GCS_BASE="$BASE_URL/storage/v1"

# --- create bucket (json) ---
echo "TEST: gcs create bucket via curl (json)"
output=$(sim_curl POST "$GCS_BASE/b?project=$PROJECT" \
    '{"name":"bash-test-bucket"}')
if echo "$output" | jq -e '.name == "bash-test-bucket"' >/dev/null 2>&1; then
    pass
else
    fail "expected bucket name in create response: $output"
fi

# --- create second bucket (json) ---
echo "TEST: gcs create second bucket via curl (json)"
output=$(sim_curl POST "$GCS_BASE/b?project=$PROJECT" \
    '{"name":"bash-test-bucket-2"}')
if echo "$output" | jq -e '.name == "bash-test-bucket-2"' >/dev/null 2>&1; then
    pass
else
    fail "expected bucket name in create response: $output"
fi

# --- get bucket (json) ---
echo "TEST: gcs get bucket via curl (json)"
output=$(sim_curl GET "$GCS_BASE/b/bash-test-bucket")
if echo "$output" | jq -e '.name == "bash-test-bucket"' >/dev/null 2>&1; then
    pass
else
    fail "expected bucket name in get response: $output"
fi

# --- get bucket (text — HTTP status check) ---
echo "TEST: gcs get bucket via curl (text)"
http_status=$(sim_curl_status GET "$GCS_BASE/b/bash-test-bucket")
if [ "$http_status" = "200" ]; then
    pass
else
    fail "expected HTTP 200 for get bucket, got $http_status"
fi

# --- list buckets (json) ---
echo "TEST: gcs list buckets via curl (json)"
output=$(sim_curl GET "$GCS_BASE/b?project=$PROJECT")
if echo "$output" | jq -e '.items | length >= 2' >/dev/null 2>&1; then
    pass
else
    fail "expected at least 2 buckets: $output"
fi

# --- delete bucket (json) ---
echo "TEST: gcs delete bucket via curl (json)"
http_status=$(sim_curl_status DELETE "$GCS_BASE/b/bash-test-bucket-2")
if [ "$http_status" -ge 200 ] && [ "$http_status" -lt 300 ]; then
    pass
else
    fail "delete bucket returned status $http_status"
fi

# --- verify deleted bucket is gone ---
echo "TEST: gcs get bucket after delete (expect 404)"
http_status=$(sim_curl_status GET "$GCS_BASE/b/bash-test-bucket-2")
if [ "$http_status" = "404" ]; then
    pass
else
    fail "expected 404 for deleted bucket, got $http_status"
fi

# --- cleanup remaining bucket ---
sim_curl DELETE "$GCS_BASE/b/bash-test-bucket" >/dev/null 2>&1

# ===========================================================================
# Summary
# ===========================================================================

echo ""
echo "========================================"
echo "PASSED: $PASSED, FAILED: $FAILED"
echo "========================================"

if [ "$FAILED" -gt 0 ]; then
    exit 1
fi
exit 0
