#!/usr/bin/env bash
# Single source of truth for "the current branch is rebased on top of
# origin/main": origin/main's tip is an ancestor of HEAD, history is linear (no
# merge commits since origin/main), you don't work directly on main, and (at
# push time) local main is in sync. A branch that has fallen behind must be
# rebased before committing / pushing / merging.
#
# One script, two callers: the rebased-on-main pre-push hook and the
# rebased-on-main CI job. It compares against FETCH_HEAD (the freshly fetched
# origin/main tip) rather than the remote-tracking ref, which is reliable under
# CI's narrow-refspec checkouts. Best-effort offline at push time (skips on a
# failed fetch); authoritative in CI (CI=1 → a fetch failure / behind /
# non-linear branch is a hard error). Mirror remotes
# (PRE_COMMIT_REMOTE_NAME != origin) receive origin/main verbatim and are exempt.
set -euo pipefail

remote_name="${PRE_COMMIT_REMOTE_NAME:-origin}"
[ "$remote_name" != "origin" ] && exit 0

git rev-parse --git-dir >/dev/null 2>&1 || exit 0

network_required=""
[ -n "${CI:-}" ] && network_required="yes"

# Never work directly on main. A detached CI checkout reports "HEAD", so this
# only fires for a real local `main` checkout.
branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
if [ "$branch" = "main" ]; then
  echo "ERROR: do not commit/push directly on main — create a branch first." >&2
  exit 1
fi

# Fetch the latest origin/main into FETCH_HEAD.
if ! git fetch -q origin main 2>/dev/null; then
  if [ -n "$network_required" ]; then
    echo "ERROR: could not fetch origin/main to verify the branch is rebased." >&2
    exit 1
  fi
  echo "check-rebased-on-main: could not fetch origin/main (offline?); skipping (CI enforces this)."
  exit 0
fi

# Push-time hygiene: local main must match origin/main. Skipped in CI, which
# has no working main branch.
if [ -z "${CI:-}" ]; then
  local_main=$(git rev-parse --verify --quiet main 2>/dev/null || echo "")
  fetched=$(git rev-parse FETCH_HEAD 2>/dev/null || echo "")
  if [ -n "$local_main" ] && [ -n "$fetched" ] && [ "$local_main" != "$fetched" ]; then
    echo "ERROR: local main ($local_main) differs from origin/main ($fetched)." >&2
    echo "Sync first: git checkout main && git pull origin main" >&2
    exit 1
  fi
fi

# HEAD is on top of origin/main iff origin/main's tip is an ancestor of HEAD.
if ! git merge-base --is-ancestor FETCH_HEAD HEAD; then
  behind=$(git rev-list --count "HEAD..FETCH_HEAD" 2>/dev/null || echo "?")
  echo "ERROR: this branch is NOT rebased on top of origin/main" >&2
  echo "(${behind} commit(s) are on origin/main but not in this branch)." >&2
  echo "Rebase before committing/pushing/merging:" >&2
  echo "  git fetch origin main && git rebase origin/main" >&2
  exit 1
fi

# Linear history: no merge commits since origin/main.
merges=$(git rev-list --merges "FETCH_HEAD..HEAD" 2>/dev/null | wc -l | tr -d ' ')
if [ "${merges:-0}" -gt 0 ]; then
  echo "ERROR: branch has ${merges} merge commit(s) since origin/main — history must be linear." >&2
  echo "Rebase instead of merging: git rebase origin/main" >&2
  exit 1
fi

echo "check-rebased-on-main: branch rebased on origin/main, linear — OK."
