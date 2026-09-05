#!/usr/bin/env bash
# check-casefold-slice.sh — forbid the case-fold-then-index slice-bounds class.
#
# strings.ToLower/ToUpper (and bytes.*) are Unicode-aware and change the BYTE
# length of non-ASCII / invalid-UTF-8 input (each bad byte expands to a 3-byte
# U+FFFD). Computing a string index against the folded copy and then slicing the
# ORIGINAL panics with "slice bounds out of range" — a class fuzzing has found
# repeatedly (cwIndexKeyword, Cosmos, Azure Tags, DynamoDB).
#
# This blocks the inline form `(strings|bytes).Index*( ... .To{Lower,Upper}( ...`.
# Use the byte-length-preserving helpers instead:
#   sim.CaseInsensitiveIndex(s, sub)   — index valid in the original s
#   sim.ASCIIFold(s) / sim.ASCIIFoldUpper(s)
#
# bash + zsh portable; shellcheck-clean.
set -u

cd "$(git rev-parse --show-toplevel)" || exit 2

pattern='(strings|bytes)\.(Last)?Index([A-Za-z]*)?\([^)]*\.To(Lower|Upper)\('

# The Go source this repository owns. The filter is anchored so a match under
# specs/, testdata/ or a vendored tree is not reported, and it names the
# directories this repository actually has: the sockerless monorepo's
# simulators/, backends/, agent/ and core/ are not among them, and a filter
# naming those matched nothing here.
readonly SCAN_DIRS='sim|simulator-aws|simulator-azure|simulator-gcp|realexec|testutil|ui-auth'

# A gate that scans nothing passes for the wrong reason. Prove the scan set is
# non-empty before trusting a clean result.
scanned="$(git grep -lE '^package ' -- '*.go' 2>/dev/null | grep -cE "^($SCAN_DIRS)/" || true)"
if [ "${scanned:-0}" -eq 0 ]; then
  echo "check-casefold-slice: no Go files under ($SCAN_DIRS) — the scan set is empty," >&2
  echo "so a clean result would prove nothing. Fix the scan set." >&2
  exit 2
fi

hits="$(git grep -nE "$pattern" -- '*.go' 2>/dev/null \
  | grep -vE '_test\.go:' \
  | grep -E "^($SCAN_DIRS)/" || true)"

if [ -n "$hits" ]; then
  echo "Forbidden case-fold-then-index pattern (slice-bounds panic class):"
  echo "$hits"
  echo
  echo "strings/bytes.ToLower/ToUpper change byte length on non-ASCII / invalid"
  echo "UTF-8, so an index from the folded copy can be out of range in the"
  echo "original. Use sim.CaseInsensitiveIndex(s, sub) or sim.ASCIIFold(s) /"
  echo "sim.ASCIIFoldUpper(s) (byte-length preserving) for any fold whose index"
  echo "slices the original string."
  exit 1
fi
exit 0
