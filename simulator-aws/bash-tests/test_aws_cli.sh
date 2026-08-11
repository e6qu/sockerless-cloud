#!/usr/bin/env bash
#
# Bash-based CLI tests for the AWS simulator.
#
# Prerequisites:
#   - Go toolchain (to build the simulator)
#   - aws CLI v2
#   - jq
#   - curl
#
# Usage:
#   cd simulator-aws/bash-tests
#   ./test_aws_cli.sh
#
# The script builds the simulator binary, starts it on a random free port,
# runs all tests, and prints a pass/fail summary. Exit code is 1 if any
# test failed, 0 otherwise.

set -uo pipefail

PASSES=0
FAILURES=0
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SIM_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$SIM_DIR/simulator-aws-bash-test"
TMP_DIR="$(mktemp -d)"
SIM_PID=""

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
cleanup() {
    if [ -n "$SIM_PID" ] && kill -0 "$SIM_PID" 2>/dev/null; then
        kill "$SIM_PID" 2>/dev/null
        wait "$SIM_PID" 2>/dev/null
    fi
    rm -f "$BINARY"
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Prereq checks
# ---------------------------------------------------------------------------
for tool in aws jq curl go; do
    if ! command -v "$tool" &>/dev/null; then
        echo "ERROR: $tool is required but not found in PATH"
        exit 1
    fi
done

# ---------------------------------------------------------------------------
# Build simulator
# ---------------------------------------------------------------------------
echo "Building simulator..."
(cd "$SIM_DIR" && CGO_ENABLED=0 go build -tags noui -o "$BINARY" .) || {
    echo "FATAL: failed to build simulator"
    exit 1
}

# ---------------------------------------------------------------------------
# Find a free port and start the simulator
# ---------------------------------------------------------------------------
find_free_port() {
    python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

PORT=$(find_free_port)
BASE_URL="http://127.0.0.1:${PORT}"

echo "Starting simulator on port $PORT..."
SIM_LISTEN_ADDR=":${PORT}" "$BINARY" &>/dev/null &
SIM_PID=$!

# Wait for health endpoint
echo "Waiting for simulator to become healthy..."
for i in $(seq 1 50); do
    if curl -sf "${BASE_URL}/health" >/dev/null 2>&1; then
        echo "Simulator ready."
        break
    fi
    if [ "$i" -eq 50 ]; then
        echo "FATAL: simulator did not become healthy"
        exit 1
    fi
    sleep 0.1
done

# ---------------------------------------------------------------------------
# AWS CLI helpers
# ---------------------------------------------------------------------------
aws_cli() {
    AWS_ENDPOINT_URL="$BASE_URL" \
    AWS_ACCESS_KEY_ID=test \
    AWS_SECRET_ACCESS_KEY=test \
    AWS_DEFAULT_REGION=us-east-1 \
    AWS_PAGER="" \
    aws "$@"
}

aws_s3_cli() {
    AWS_ENDPOINT_URL="${BASE_URL}/s3" \
    AWS_ACCESS_KEY_ID=test \
    AWS_SECRET_ACCESS_KEY=test \
    AWS_DEFAULT_REGION=us-east-1 \
    AWS_PAGER="" \
    aws "$@"
}

# ---------------------------------------------------------------------------
# Test helpers
# ---------------------------------------------------------------------------

# run_test NAME COMMAND...
# Runs a command, expects exit code 0 and non-empty output.
run_test() {
    local name="$1"; shift
    echo -n "TEST: $name ... "
    local output
    output=$("$@" 2>&1)
    local rc=$?
    if [ $rc -ne 0 ]; then
        echo "FAIL (exit code $rc)"
        echo "  output: $output"
        FAILURES=$((FAILURES + 1))
        return 1
    fi
    echo "PASS"
    PASSES=$((PASSES + 1))
    # Stash output for caller
    TEST_OUTPUT="$output"
    return 0
}

# run_test_json NAME JQ_EXPR COMMAND...
# Runs a command, expects exit code 0, valid JSON, and jq expression to be truthy.
run_test_json() {
    local name="$1"; shift
    local jq_expr="$1"; shift
    echo -n "TEST: $name ... "
    local output
    output=$("$@" 2>&1)
    local rc=$?
    if [ $rc -ne 0 ]; then
        echo "FAIL (exit code $rc)"
        echo "  output: $output"
        FAILURES=$((FAILURES + 1))
        return 1
    fi
    if ! echo "$output" | jq -e "$jq_expr" >/dev/null 2>&1; then
        echo "FAIL (jq check failed: $jq_expr)"
        echo "  output: $output"
        FAILURES=$((FAILURES + 1))
        return 1
    fi
    echo "PASS"
    PASSES=$((PASSES + 1))
    TEST_OUTPUT="$output"
    return 0
}

# ===========================================================================
# STS Tests
# ===========================================================================
echo ""
echo "=== STS ==="

run_test "sts get-caller-identity (text)" \
    aws_cli sts get-caller-identity --output text

run_test_json "sts get-caller-identity (json)" '.Account' \
    aws_cli sts get-caller-identity --output json

# ===========================================================================
# IAM Tests
# ===========================================================================
echo ""
echo "=== IAM ==="

ASSUME_ROLE_POLICY='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

run_test "iam create-role (text)" \
    aws_cli iam create-role \
        --role-name bash-test-role \
        --assume-role-policy-document "$ASSUME_ROLE_POLICY" \
        --output text

run_test_json "iam create-role (json)" '.Role.RoleName' \
    aws_cli iam create-role \
        --role-name bash-test-role-json \
        --assume-role-policy-document "$ASSUME_ROLE_POLICY" \
        --output json

run_test "iam get-role (text)" \
    aws_cli iam get-role --role-name bash-test-role --output text

run_test_json "iam get-role (json)" '.Role.RoleName' \
    aws_cli iam get-role --role-name bash-test-role-json --output json

run_test "iam delete-role (text)" \
    aws_cli iam delete-role --role-name bash-test-role --output text

run_test "iam delete-role-json" \
    aws_cli iam delete-role --role-name bash-test-role-json --output text

# ===========================================================================
# CloudWatch Logs Tests
# ===========================================================================
echo ""
echo "=== CloudWatch Logs ==="

run_test "logs create-log-group (text)" \
    aws_cli logs create-log-group --log-group-name /bash/test-group --output text

run_test "logs create-log-group (json)" \
    aws_cli logs create-log-group --log-group-name /bash/test-group-json --output json

run_test "logs describe-log-groups (text)" \
    aws_cli logs describe-log-groups --log-group-name-prefix /bash/test-group --output text

run_test_json "logs describe-log-groups (json)" '.logGroups | length > 0' \
    aws_cli logs describe-log-groups --log-group-name-prefix /bash/test-group --output json

run_test "logs create-log-stream (text)" \
    aws_cli logs create-log-stream \
        --log-group-name /bash/test-group \
        --log-stream-name test-stream \
        --output text

run_test "logs create-log-stream (json)" \
    aws_cli logs create-log-stream \
        --log-group-name /bash/test-group-json \
        --log-stream-name test-stream-json \
        --output json

NOW_MS=$(python3 -c 'import time; print(int(time.time()*1000))')

run_test "logs put-log-events (text)" \
    aws_cli logs put-log-events \
        --log-group-name /bash/test-group \
        --log-stream-name test-stream \
        --log-events "[{\"timestamp\":${NOW_MS},\"message\":\"hello from bash text\"}]" \
        --output text

run_test_json "logs put-log-events (json)" '.nextSequenceToken' \
    aws_cli logs put-log-events \
        --log-group-name /bash/test-group-json \
        --log-stream-name test-stream-json \
        --log-events "[{\"timestamp\":${NOW_MS},\"message\":\"hello from bash json\"}]" \
        --output json

run_test "logs get-log-events (text)" \
    aws_cli logs get-log-events \
        --log-group-name /bash/test-group \
        --log-stream-name test-stream \
        --output text

run_test_json "logs get-log-events (json)" '.events | length > 0' \
    aws_cli logs get-log-events \
        --log-group-name /bash/test-group-json \
        --log-stream-name test-stream-json \
        --output json

run_test "logs delete-log-group (text)" \
    aws_cli logs delete-log-group --log-group-name /bash/test-group --output text

run_test "logs delete-log-group (json)" \
    aws_cli logs delete-log-group --log-group-name /bash/test-group-json --output json

# ===========================================================================
# ECS Tests
# ===========================================================================
echo ""
echo "=== ECS ==="

# -- Cluster --
run_test "ecs create-cluster (text)" \
    aws_cli ecs create-cluster --cluster-name bash-cluster --output text

run_test_json "ecs create-cluster (json)" '.cluster.clusterName' \
    aws_cli ecs create-cluster --cluster-name bash-cluster-json --output json

run_test "ecs describe-clusters (text)" \
    aws_cli ecs describe-clusters --clusters bash-cluster --output text

run_test_json "ecs describe-clusters (json)" '.clusters[0].clusterName' \
    aws_cli ecs describe-clusters --clusters bash-cluster-json --output json

# -- Task Definition --
CONTAINER_DEFS='[{"name":"app","image":"alpine:latest","command":["echo","hello"],"logConfiguration":{"logDriver":"awslogs","options":{"awslogs-group":"/ecs/bash-test","awslogs-stream-prefix":"ecs"}}}]'

run_test "ecs register-task-definition (text)" \
    aws_cli ecs register-task-definition \
        --family bash-task \
        --requires-compatibilities FARGATE \
        --network-mode awsvpc \
        --cpu 256 --memory 512 \
        --container-definitions "$CONTAINER_DEFS" \
        --output text

run_test_json "ecs register-task-definition (json)" '.taskDefinition.taskDefinitionArn' \
    aws_cli ecs register-task-definition \
        --family bash-task-json \
        --requires-compatibilities FARGATE \
        --network-mode awsvpc \
        --cpu 256 --memory 512 \
        --container-definitions "$CONTAINER_DEFS" \
        --output json

# Capture the task definition ARN for run-task
TD_ARN=$(echo "$TEST_OUTPUT" | jq -r '.taskDefinition.taskDefinitionArn')

# -- Run Task --
run_test "ecs run-task (text)" \
    aws_cli ecs run-task \
        --cluster bash-cluster \
        --task-definition bash-task \
        --launch-type FARGATE \
        --count 1 \
        --network-configuration 'awsvpcConfiguration={subnets=[subnet-0123456789abcdef0]}' \
        --output text

run_test_json "ecs run-task (json)" '.tasks[0].taskArn' \
    aws_cli ecs run-task \
        --cluster bash-cluster-json \
        --task-definition "$TD_ARN" \
        --launch-type FARGATE \
        --count 1 \
        --network-configuration 'awsvpcConfiguration={subnets=[subnet-0123456789abcdef0]}' \
        --output json

TASK_ARN=$(echo "$TEST_OUTPUT" | jq -r '.tasks[0].taskArn')

# -- Describe Tasks --
run_test "ecs describe-tasks (text)" \
    aws_cli ecs describe-tasks \
        --cluster bash-cluster-json \
        --tasks "$TASK_ARN" \
        --output text

run_test_json "ecs describe-tasks (json)" '.tasks[0].lastStatus' \
    aws_cli ecs describe-tasks \
        --cluster bash-cluster-json \
        --tasks "$TASK_ARN" \
        --output json

# -- List Tasks --
run_test "ecs list-tasks (text)" \
    aws_cli ecs list-tasks --cluster bash-cluster-json --output text

run_test_json "ecs list-tasks (json)" '.taskArns' \
    aws_cli ecs list-tasks --cluster bash-cluster-json --output json

# -- Stop Task --
run_test "ecs stop-task (text)" \
    aws_cli ecs stop-task \
        --cluster bash-cluster-json \
        --task "$TASK_ARN" \
        --output text

# Run another task to stop in JSON mode
run_test_json "ecs run-task for stop (json)" '.tasks[0].taskArn' \
    aws_cli ecs run-task \
        --cluster bash-cluster-json \
        --task-definition "$TD_ARN" \
        --launch-type FARGATE \
        --count 1 \
        --network-configuration 'awsvpcConfiguration={subnets=[subnet-0123456789abcdef0]}' \
        --output json

TASK_ARN2=$(echo "$TEST_OUTPUT" | jq -r '.tasks[0].taskArn')

run_test_json "ecs stop-task (json)" '.task.lastStatus' \
    aws_cli ecs stop-task \
        --cluster bash-cluster-json \
        --task "$TASK_ARN2" \
        --output json

# -- Deregister Task Definition --
run_test "ecs deregister-task-definition (text)" \
    aws_cli ecs deregister-task-definition \
        --task-definition bash-task:1 \
        --output text

run_test_json "ecs deregister-task-definition (json)" '.taskDefinition.status' \
    aws_cli ecs deregister-task-definition \
        --task-definition "$TD_ARN" \
        --output json

# -- Delete Cluster --
run_test "ecs delete-cluster (text)" \
    aws_cli ecs delete-cluster --cluster bash-cluster --output text

run_test_json "ecs delete-cluster (json)" '.cluster.clusterName' \
    aws_cli ecs delete-cluster --cluster bash-cluster-json --output json

# ===========================================================================
# Lambda Tests
# ===========================================================================
echo ""
echo "=== Lambda ==="

# Create a dummy zip for Lambda
LAMBDA_ZIP="$TMP_DIR/lambda.zip"
echo 'exports.handler = async () => ({ statusCode: 200, body: "hello" });' > "$TMP_DIR/index.js"
(cd "$TMP_DIR" && zip -q lambda.zip index.js)

run_test "lambda create-function (text)" \
    aws_cli lambda create-function \
        --function-name bash-test-func \
        --runtime nodejs18.x \
        --role arn:aws:iam::123456789012:role/test-role \
        --handler index.handler \
        --zip-file "fileb://$LAMBDA_ZIP" \
        --output text

run_test_json "lambda create-function (json)" '.FunctionName' \
    aws_cli lambda create-function \
        --function-name bash-test-func-json \
        --runtime nodejs18.x \
        --role arn:aws:iam::123456789012:role/test-role \
        --handler index.handler \
        --zip-file "fileb://$LAMBDA_ZIP" \
        --output json

run_test "lambda get-function (text)" \
    aws_cli lambda get-function --function-name bash-test-func --output text

run_test_json "lambda get-function (json)" '.Configuration.FunctionName' \
    aws_cli lambda get-function --function-name bash-test-func-json --output json

run_test "lambda list-functions (text)" \
    aws_cli lambda list-functions --output text

run_test_json "lambda list-functions (json)" '.Functions | length > 0' \
    aws_cli lambda list-functions --output json

INVOKE_OUT="$TMP_DIR/invoke-out.json"

run_test "lambda invoke (text)" \
    aws_cli lambda invoke \
        --function-name bash-test-func \
        "$TMP_DIR/invoke-out-text.json" \
        --output text

run_test_json "lambda invoke (json)" '.StatusCode' \
    aws_cli lambda invoke \
        --function-name bash-test-func-json \
        "$INVOKE_OUT" \
        --output json

run_test "lambda delete-function (text)" \
    aws_cli lambda delete-function --function-name bash-test-func --output text

run_test "lambda delete-function (json)" \
    aws_cli lambda delete-function --function-name bash-test-func-json --output json

# ===========================================================================
# S3 Tests
# ===========================================================================
echo ""
echo "=== S3 ==="

run_test "s3api create-bucket (text)" \
    aws_s3_cli s3api create-bucket --bucket bash-test-bucket --output text

run_test_json "s3api create-bucket (json)" '.Location' \
    aws_s3_cli s3api create-bucket --bucket bash-test-bucket-json --output json

run_test "s3api head-bucket (text)" \
    aws_s3_cli s3api head-bucket --bucket bash-test-bucket --output text

run_test "s3api head-bucket (json)" \
    aws_s3_cli s3api head-bucket --bucket bash-test-bucket-json --output json

# Create temp file for upload
echo "hello from bash s3 test" > "$TMP_DIR/s3upload.txt"

run_test "s3api put-object (text)" \
    aws_s3_cli s3api put-object \
        --bucket bash-test-bucket \
        --key test.txt \
        --body "$TMP_DIR/s3upload.txt" \
        --output text

run_test_json "s3api put-object (json)" '.ETag' \
    aws_s3_cli s3api put-object \
        --bucket bash-test-bucket-json \
        --key test.txt \
        --body "$TMP_DIR/s3upload.txt" \
        --output json

run_test "s3api get-object (text)" \
    aws_s3_cli s3api get-object \
        --bucket bash-test-bucket \
        --key test.txt \
        "$TMP_DIR/s3download-text.txt" \
        --output text

run_test_json "s3api get-object (json)" '.ContentLength' \
    aws_s3_cli s3api get-object \
        --bucket bash-test-bucket-json \
        --key test.txt \
        "$TMP_DIR/s3download-json.txt" \
        --output json

run_test "s3api delete-object (text)" \
    aws_s3_cli s3api delete-object \
        --bucket bash-test-bucket \
        --key test.txt \
        --output text

run_test "s3api delete-object (json)" \
    aws_s3_cli s3api delete-object \
        --bucket bash-test-bucket-json \
        --key test.txt \
        --output json

run_test "s3api delete-bucket (text)" \
    aws_s3_cli s3api delete-bucket --bucket bash-test-bucket --output text

run_test "s3api delete-bucket (json)" \
    aws_s3_cli s3api delete-bucket --bucket bash-test-bucket-json --output json

# ===========================================================================
# Summary
# ===========================================================================
echo ""
echo "==========================================="
echo "PASSED: $PASSES, FAILED: $FAILURES"
echo "==========================================="

if [ "$FAILURES" -gt 0 ]; then
    exit 1
fi
exit 0
