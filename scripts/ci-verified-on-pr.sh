#!/usr/bin/env bash
# ci-verified-on-pr.sh — decides whether a push to main needs CI at all.
#
# Branch protection requires a pull request to be up to date with main before
# it merges, so a squash merge commits exactly the tree the pull request's head
# carried. When CI already passed on that head, running every job again on main
# repeats about seventy-five jobs on an identical tree, and a burst of merges
# queued enough of those runs to starve every repository's runners. This finds
# the pull request the commit came from and reports `verified=true` only when
# its head has the same tree and the CI workflow concluded success on that head;
# the other jobs then skip, and the run's verdict is the pull request's. A merge
# made before its CI finished, a push without a pull request, or any lookup that
# fails reports `verified=false`, and the full suite runs.
#
# Usage: ci-verified-on-pr.sh   (reads GITHUB_EVENT_NAME, GITHUB_REF, GITHUB_SHA,
# GITHUB_REPOSITORY, GITHUB_OUTPUT; needs GH_TOKEN with contents, actions and
# pull-requests read)
set -euo pipefail

verdict() {
  echo "verified=$1" >>"$GITHUB_OUTPUT"
  echo "ci-verified-on-pr: verified=$1 — $2"
  exit 0
}

if [ "${GITHUB_EVENT_NAME}" != push ] || [ "${GITHUB_REF}" != refs/heads/main ]; then
  verdict false "not a push to main (tag pushes keep full CI)"
fi

tree() { gh api "repos/${GITHUB_REPOSITORY}/git/commits/$1" --jq .tree.sha; }

pr=$(gh api "repos/${GITHUB_REPOSITORY}/commits/${GITHUB_SHA}/pulls" \
  --jq '[.[] | select(.merged_at != null and .base.ref == "main")][0] | "\(.number) \(.head.sha)"' 2>/dev/null) ||
  verdict false "could not look up the pull request for ${GITHUB_SHA}"
if [ -z "$pr" ] || [ "$pr" = "null null" ]; then
  verdict false "${GITHUB_SHA} came from no merged pull request"
fi
number=${pr% *}
head=${pr#* }

[ "$(tree "$GITHUB_SHA")" = "$(tree "$head")" ] ||
  verdict false "#${number}'s head ${head} has a different tree than ${GITHUB_SHA}"

run=$(gh api "repos/${GITHUB_REPOSITORY}/actions/runs?head_sha=${head}&event=pull_request&status=completed&per_page=100" \
  --jq '[.workflow_runs[] | select(.name == "CI") | select(.conclusion == "success")][0].html_url // empty') ||
  verdict false "could not list #${number}'s CI runs"
[ -n "$run" ] || verdict false "#${number}'s head ${head} has no successful CI run"

verdict true "the same tree passed CI on #${number}: ${run}"
