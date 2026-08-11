#!/usr/bin/env bash
# Generic build-and-start script for Playwright end-to-end tests.
# Works for backends and simulators.
#
# Required env vars:
#   SERVER_PORT  — port to listen on
#   HEALTH_URL   — full URL for the health endpoint (e.g. http://localhost:19210/internal/v1/healthz)
#
# Optional env vars:
#   SERVER_BIN     — explicit immutable Go binary coordinate
#   SERVER_PACKAGE — repository-relative Go package directory to build
#   SERVER_NAME    — binary name produced by SERVER_PACKAGE
#   SIMULATOR_PACKAGE      — repository-relative simulator Go package
#   SIMULATOR_NAME         — simulator binary name
#   SIMULATOR_PORT         — simulator HTTP port
#   SIMULATOR_GRPC_PORT    — simulator Cloud Logging gRPC port (GCP only)
#   SIMULATOR_SETUP        — real cloud resources to provision for this backend
#   SERVER_HELPER_PACKAGE  — repository-relative module containing a required helper
#   SERVER_HELPER_COMMAND  — Go package to build inside SERVER_HELPER_PACKAGE
#   SERVER_HELPER_NAME     — helper binary name
#   SERVER_HELPER_ENV      — backend environment variable receiving the helper path
#   SERVER_HELPER_GOOS     — helper target operating system (optional)
#   SERVER_HELPER_GOARCH   — helper target architecture (optional)
#
# SERVER_PACKAGE and SERVER_NAME are required when SERVER_BIN is absent.
set -euo pipefail

if [[ -z "${SERVER_PORT:-}" || -z "${HEALTH_URL:-}" ]]; then
  echo "ERROR: SERVER_PORT and HEALTH_URL must be set" >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
simulator_pid=""
server_pid=""
simulator_tmp=""
helper_tmp=""

cleanup() {
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  if [[ -n "$simulator_pid" ]]; then
    kill "$simulator_pid" 2>/dev/null || true
    wait "$simulator_pid" 2>/dev/null || true
  fi
  if [[ -n "$simulator_tmp" ]]; then
    rm -rf "$simulator_tmp"
  fi
  if [[ -n "$helper_tmp" ]]; then
    rm -rf "$helper_tmp"
  fi
}
trap cleanup EXIT

wait_for_url() {
  local url="$1"
  local label="$2"
  local pid="$3"
  local attempt

  for ((attempt = 0; attempt < 300; attempt++)); do
    if curl --fail --silent "$url" >/dev/null 2>&1; then
      return 0
    fi
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "ERROR: $label process exited unexpectedly" >&2
      return 1
    fi
    sleep 0.1
  done

  echo "ERROR: $label did not become healthy within 30s" >&2
  return 1
}

if [[ -n "${SIMULATOR_PACKAGE:-}" ]]; then
  if [[ -z "${SIMULATOR_NAME:-}" || -z "${SIMULATOR_PORT:-}" ]]; then
    echo "ERROR: SIMULATOR_NAME and SIMULATOR_PORT are required with SIMULATOR_PACKAGE" >&2
    exit 1
  fi

  simulator_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sockerless-e2e.XXXXXX")"
  (
    cd "$repo_root/$SIMULATOR_PACKAGE"
    env CGO_ENABLED=0 go build -tags noui -o "$simulator_tmp/$SIMULATOR_NAME" .
  )

  simulator_url="http://127.0.0.1:${SIMULATOR_PORT}"
  if [[ -n "${SIMULATOR_GRPC_PORT:-}" ]]; then
    SIM_LISTEN_ADDR=":${SIMULATOR_PORT}" \
      SIM_GCP_GRPC_PORT="${SIMULATOR_GRPC_PORT}" \
      SIM_RUNTIME=process \
      SIM_LOG_LEVEL=warn \
      "$simulator_tmp/$SIMULATOR_NAME" &
  else
    SIM_LISTEN_ADDR=":${SIMULATOR_PORT}" \
      SIM_RUNTIME=process \
      SIM_LOG_LEVEL=warn \
      "$simulator_tmp/$SIMULATOR_NAME" &
  fi
  simulator_pid=$!
  wait_for_url "$simulator_url/health" "$SIMULATOR_NAME" "$simulator_pid"

  if [[ -n "${SIMULATOR_SETUP:-}" ]]; then
    SIMULATOR_URL="$simulator_url" \
      SIMULATOR_SETUP="$SIMULATOR_SETUP" \
      bash "$repo_root/ui/packages/core/e2e/setup-cloud.sh"
  fi
fi

if [[ -n "${SERVER_HELPER_PACKAGE:-}" ]]; then
  if [[ -z "${SERVER_HELPER_COMMAND:-}" || -z "${SERVER_HELPER_NAME:-}" || -z "${SERVER_HELPER_ENV:-}" ]]; then
    echo "ERROR: SERVER_HELPER_COMMAND, SERVER_HELPER_NAME, and SERVER_HELPER_ENV are required with SERVER_HELPER_PACKAGE" >&2
    exit 1
  fi
  helper_tmp="$(mktemp -d "${TMPDIR:-/tmp}/sockerless-helper.XXXXXX")"
  (
    cd "$repo_root/$SERVER_HELPER_PACKAGE"
    env \
      \
      CGO_ENABLED=0 \
      GOOS="${SERVER_HELPER_GOOS:-$(go env GOOS)}" \
      GOARCH="${SERVER_HELPER_GOARCH:-$(go env GOARCH)}" \
      go build -o "$helper_tmp/$SERVER_HELPER_NAME" "$SERVER_HELPER_COMMAND"
  )
  export "$SERVER_HELPER_ENV=$helper_tmp/$SERVER_HELPER_NAME"
fi

if [[ -z "${SERVER_BIN:-}" ]]; then
  if [[ -z "${SERVER_PACKAGE:-}" || -z "${SERVER_NAME:-}" ]]; then
    echo "ERROR: SERVER_PACKAGE and SERVER_NAME must be set when SERVER_BIN is absent" >&2
    exit 1
  fi
  bun run build
  make -C "$repo_root/$SERVER_PACKAGE" build
  SERVER_BIN="$repo_root/$SERVER_PACKAGE/$SERVER_NAME"
fi

if [[ "${SIM_MODE:-}" == "1" ]]; then
  SIM_LISTEN_ADDR=":${SERVER_PORT}" "$SERVER_BIN" &
else
  "$SERVER_BIN" -addr ":${SERVER_PORT}" &
fi
server_pid=$!
wait_for_url "$HEALTH_URL" "$SERVER_NAME" "$server_pid"

echo "Server PID=$server_pid on :$SERVER_PORT"
wait "$server_pid"
