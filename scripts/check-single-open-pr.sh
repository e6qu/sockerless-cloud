#!/usr/bin/env bash
# Enforce the one-PR rule: at most one PR may be open in the project at a time.
# There should be just one PR in progress at a time — put all work in the
# already-open PR; never open a new one while one exists.
#
# Used by the pre-commit hook and by CI from the same source:
#   - Locally without gh / auth / network it skips (the CI gate is authoritative).
#   - In CI a token is always present, so a query failure is a hard error and
#     >1 open PR fails the job.
set -euo pipefail

have_token=""
[ -n "${GH_TOKEN:-}${GITHUB_TOKEN:-}" ] && have_token="yes"

if ! command -v gh >/dev/null 2>&1; then
  echo "check-single-open-pr: gh CLI not installed; skipping (CI enforces this)."
  exit 0
fi

if ! open_json=$(gh pr list --state open --limit 100 --json number,title,isDraft 2>/dev/null); then
  if [ -n "$have_token" ]; then
    echo "ERROR: could not query open PRs despite a token being present." >&2
    exit 1
  fi
  echo "check-single-open-pr: could not query open PRs (no auth / offline); skipping (CI enforces this)."
  exit 0
fi

count=$(printf '%s' "$open_json" | jq 'length')

if [ "${count:-0}" -gt 1 ]; then
  echo "ERROR: ${count} PRs are open in the project." >&2
  printf '%s\n' "$open_json" | jq -r '.[] | "  #\(.number) \(.title)\(if .isDraft then " (draft)" else "" end)"' >&2
  echo >&2
  echo "There should be just one PR in progress at a time. Put all work in the" >&2
  echo "already-open PR; never open a new one while one exists." >&2
  echo "If more than one is already open, CONSOLIDATE all their work into a" >&2
  echo "single PR (merge the branches together) — do not open yet another and do" >&2
  echo "not otherwise evade this rule." >&2
  echo "Closing a PR WITHOUT merging it means that work is ABANDONED and DELETED" >&2
  echo "for good — it is never a way to park or shelve work for later, so never" >&2
  echo "close a PR (without merging) to dodge this check." >&2
  exit 1
fi

echo "check-single-open-pr: ${count:-0} open PR — OK."
