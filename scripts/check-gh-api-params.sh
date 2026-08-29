#!/usr/bin/env bash
# check-gh-api-params.sh — reject `gh api` query parameters GitHub does not define.
#
# The REST API ignores an unknown query key rather than rejecting it, so a typo
# answers successfully with an unfiltered result. A rename that reached inside a
# URL turned `commits?path=` into `commits?srcpath=`, which made every vendored
# specification compare its pin against the repository's branch tip: permanent
# drift no refresh could clear, reported by a script that exited cleanly.
#
# Usage: scripts/check-gh-api-params.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Parameters this repository has a documented use for. Extend deliberately.
ALLOWED="path per_page page sha since until author state ref"

fail=0
while IFS=: read -r file line url; do
  query="${url#*\?}"
  [ "$query" = "$url" ] && continue
  IFS='&' read -ra pairs <<<"$query"
  for pair in "${pairs[@]}"; do
    key="${pair%%=*}"
    [ -n "$key" ] || continue
    case " $ALLOWED " in
    *" $key "*) ;;
    *)
      echo "$file:$line: unknown GitHub API query parameter '$key' in $url" >&2
      fail=1
      ;;
    esac
  done
done < <(grep -rn 'gh api "' scripts .github/workflows 2>/dev/null |
  sed -E 's/^([^:]+):([0-9]+):.*gh api "([^"]+)".*/\1:\2:\3/' | grep '?')

if [ "$fail" -eq 0 ]; then
  echo "ok    every gh api query parameter is one GitHub defines"
fi
exit "$fail"
