#!/usr/bin/env bash
# Ratchet gate for runtime spec-shape validation.
#
# The simulators, when run with SOCKERLESS_SPEC_VALIDATE=<report.jsonl> and
# SOCKERLESS_SPEC_DIR=specs/cloud-api/<cloud>, append one JSON line per
# observed divergence between a response and the vendored cloud API spec.
# This script dedupes the report into stable keys (array indices collapsed)
# and fails when a key is NOT in the cloud's allowlist
# (simulator-<cloud>/spec-violation-allowlist.txt). Every allowlist entry
# carries a BUG ID — the list only shrinks; new violations fail CI.
#
# Usage: scripts/check-spec-violations.sh <cloud> <report.jsonl>
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <cloud> <report.jsonl>" >&2
  exit 2
fi

CLOUD="$1"
REPORT="$2"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALLOWLIST="$ROOT/simulator-$CLOUD/spec-violation-allowlist.txt"

# The report file is the validator's run-marker: SetSpecValidator opens it with
# O_CREATE the instant validation arms (before any request), so the file EXISTS
# whenever the validator actually ran — even on a clean run with zero violations.
# Therefore:
#   - file ABSENT  => the validator never armed (sim built without it, wrong
#                     SOCKERLESS_SPEC_DIR/SOCKERLESS_SPEC_VALIDATE, or the sim
#                     never started). A vacuous "exit 0" here would be a false
#                     negative — the gate validated nothing. Fail loud.
#   - file present, EMPTY => validator ran, observed zero divergences. Clean pass.
if [ ! -f "$REPORT" ]; then
  echo "check-spec-violations: $CLOUD: FAIL — report file '$REPORT' is absent." >&2
  echo "The spec validator never ran: it creates this file the moment it arms," >&2
  echo "so an absent file means the sim was built/started without validation" >&2
  echo "(check SOCKERLESS_SPEC_VALIDATE / SOCKERLESS_SPEC_DIR and that the sim" >&2
  echo "binary includes the spec validator). Refusing to pass a gate that" >&2
  echo "validated nothing." >&2
  exit 1
fi

if [ ! -s "$REPORT" ]; then
  echo "check-spec-violations: $CLOUD: validator ran; no violations recorded"
  exit 0
fi

seen="$(mktemp)"
allowed="$(mktemp)"
trap 'rm -f "$seen" "$allowed"' EXIT

# Stable key: op<TAB>kind<TAB>field with [N] collapsed to [].
jq -r '[.op, .kind, (.field | gsub("\\[[0-9]+\\]"; "[]"))] | join("\t")' "$REPORT" | sort -u > "$seen"

if [ -f "$ALLOWLIST" ]; then
  # Strip comments (#...) and blank lines.
  sed -e 's/[[:space:]]*#.*$//' -e '/^[[:space:]]*$/d' "$ALLOWLIST" | sort -u > "$allowed"
else
  : > "$allowed"
fi

new="$(comm -23 "$seen" "$allowed")"
if [ -n "$new" ]; then
  count="$(printf '%s\n' "$new" | wc -l | tr -d ' ')"
  echo "check-spec-violations: $CLOUD: $count NEW spec violation(s) not in $ALLOWLIST:" >&2
  printf '%s\n' "$new" >&2
  echo "" >&2
  echo "Each is a real fidelity divergence from specs/cloud-api/$CLOUD/." >&2
  echo "Fix the simulator, or file a BUG and add the key with its BUG ID to the allowlist." >&2
  exit 1
fi

gone="$(comm -13 "$seen" "$allowed" | wc -l | tr -d ' ')"
total="$(wc -l < "$seen" | tr -d ' ')"
echo "check-spec-violations: $CLOUD: $total violation key(s), all allowlisted ($gone allowlisted key(s) not observed this run)"
