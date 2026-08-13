#!/usr/bin/env bash
# check-pr-title.sh — the squash-merge commit's title IS the pull request
# title, and release-please versions this repository by parsing main's
# history as Conventional Commits. A title it cannot parse makes the merged
# work invisible to the release pipeline: the parser stops at the first
# malformed title and reports "no user facing commits", so no release pull
# request opens and the work ships in no release. This gate fails the pull
# request instead, while the title is still cheap to fix.
#
# Usage: check-pr-title.sh "<title>"   (CI passes the PR title as $1)
set -euo pipefail

title="${1:?usage: check-pr-title.sh <pull-request-title>}"

# The shape release-please's parser accepts: type, optional (scope), optional
# breaking-change !, then ": " and a non-empty description.
pattern='^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([A-Za-z0-9._/-]+\))?!?: .+'

if [[ "$title" =~ $pattern ]]; then
  echo "check-pr-title: ok — \"$title\""
  exit 0
fi

echo "check-pr-title: the pull request title is not a Conventional Commit:" >&2
echo "  \"$title\"" >&2
echo "The squash merge uses it as the commit title on main, and release-please" >&2
echo "cannot parse it — the merged work would ship in no release. Retitle the" >&2
echo "pull request like: feat(azure): serve the site diagnostics family" >&2
exit 1
