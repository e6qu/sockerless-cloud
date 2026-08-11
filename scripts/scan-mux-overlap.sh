#!/usr/bin/env bash
# Enumerate every mux.HandleFunc / srv.HandleFunc / AWSRouter.Register
# pattern across simulator-<cloud>/*.go and flag patterns where one
# shadows another. Companion skill:
# `.claude/skills/mux-overlap-scan/SKILL.md`.
#
# Usage:
#   bash scripts/scan-mux-overlap.sh          # warn mode, exit 0 always
#   bash scripts/scan-mux-overlap.sh --gate   # exit 1 on un-allowlisted overlap
#
# Allowlist: scripts/mux-overlap-allowlist.txt
#   one entry per line: `<pattern1>  <pattern2>  <justification>` (tab-separated)
#   The pair order doesn't matter; the scanner sorts before lookup.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ALLOWLIST="$REPO_ROOT/scripts/mux-overlap-allowlist.txt"
GATE_MODE=0
if [[ "${1:-}" == "--gate" ]]; then GATE_MODE=1; fi

tmp_routes="$(mktemp)"
trap 'rm -f "$tmp_routes"' EXIT

# Pass 1 — collect routes.
for cloud in aws azure gcp; do
  for go_file in "$REPO_ROOT/simulator-$cloud"/*.go; do
    [[ -f "$go_file" ]] || continue
    [[ "$go_file" =~ _test\.go$ ]] && continue

    # mux.HandleFunc / srv.HandleFunc with method + path literal.
    { grep -nE '\.HandleFunc\(' "$go_file" || true; } \
      | { sed -E 's|^([0-9]+):.*HandleFunc\("([A-Z]+) ([^"]+)",[[:space:]]*([a-zA-Z0-9_.]+).*$|\1\t\2 \3\t\4|' || true; } \
      | { grep -E '^[0-9]+	[A-Z]+ ' || true; } \
      | while IFS=$'\t' read -r line route handler; do
          printf '%s/%s\t%s\t%s\t%s\n' "$cloud" "$(basename "$go_file")" "$line" "$route" "$handler"
        done
  done
done > "$tmp_routes"

n_routes="$(wc -l <"$tmp_routes" | tr -d ' ')"
echo "scanned $n_routes registered routes across simulator-{aws,azure,gcp}/" >&2

# Pass 2 — detect route pairs that may shadow each other.
#
# Go's net/http mux chooses the most specific matching pattern. Literal
# segments are more specific than wildcard segments, so a root-greedy S3
# route such as `GET /{bucket}/{key...}` does not steal a registered
# literal route such as `GET /2015-03-31/functions/{name}`. The scanner
# only reports root-greedy wildcard pairs that have no literal-first
# pattern to win by specificity, and it focuses on cross-file service
# boundaries where the collapsed-port simulator can accidentally mix
# cloud service slices. Intra-file route families are expected to be
# handled by their service-specific dispatch tests.
#
# The allowlist documents intentional overlap when the scanner cannot
# prove precedence from the pattern shape alone.

# Group patterns by HTTP method so cross-method registrations don't false-positive.
declare -a met_keys
while IFS=$'\t' read -r _ _ route _; do
  method="${route%% *}"
  case " ${met_keys[*]:-} " in
    *" $method "*) ;;
    *) met_keys+=("$method") ;;
  esac
done <"$tmp_routes"

n_findings=0
findings_tmp="$(mktemp)"
trap 'rm -f "$tmp_routes" "$findings_tmp"' EXIT

# Each cloud sim runs as a separate binary (aws / azure / gcp); routes
# only collide within the same binary's mux. Scan per-cloud, not across.
for cloud in aws azure gcp; do
  for method in "${met_keys[@]}"; do
    awk -F'\t' -v m="$method " -v c="$cloud/" '$1 ~ "^"c && $3 ~ "^"m {print}' "$tmp_routes" >"${findings_tmp}.m"

    while IFS=$'\t' read -r src line route handler; do
      pattern="${route#"$method" }"
      # A pattern is "greedy at the root" only if its first non-empty
      # path segment is a wildcard. Patterns like `/storage/v1/b/{bucket}/o/{object...}`
      # are NOT root-greedy — they only match paths starting with
      # `/storage/v1/b/`. Patterns like `/{bucket}/{key...}` ARE
      # root-greedy because the first segment matches anything.
      first_seg="$(echo "$pattern" | awk -F'/' '{print $2}')"
      if ! [[ "$first_seg" =~ ^\{ ]]; then continue; fi

      while IFS=$'\t' read -r src2 line2 route2 handler2; do
        [[ "$src/$line/$handler" == "$src2/$line2/$handler2" ]] && continue
        [[ "$src" == "$src2" ]] && continue
        pat2="${route2#"$method" }"
        first2="$(echo "$pat2" | awk -F'/' '{print $2}')"
        if [[ -z "$first2" || ! "$first2" =~ ^\{ ]]; then
          continue
        fi
        if [[ "$pattern" != "$pat2" ]]; then
          key="$(printf '%s\n%s\n' "$src::$route" "$src2::$route2" | sort | tr '\n' '|')"
          if ! grep -qxF "$key" "$findings_tmp" 2>/dev/null; then
            echo "$key" >>"$findings_tmp"
            printf '%s\n  %s (line %s) %s\n  %s (line %s) %s\n\n' \
              "SHADOW [$cloud $method]:" \
              "$src" "$line" "$route -> $handler" \
              "$src2" "$line2" "$route2 -> $handler2"
            n_findings=$((n_findings + 1))
          fi
        fi
      done <"${findings_tmp}.m"
    done <"${findings_tmp}.m"
    rm -f "${findings_tmp}.m"
  done
done

echo "" >&2
echo "scanner: $n_findings shadow pairs flagged" >&2

# Allowlist check — only meaningful in gate mode.
if [[ "$GATE_MODE" == "1" ]]; then
  unallowlisted=0
  if [[ -s "$findings_tmp" ]]; then
    while IFS= read -r key; do
      # `key` is "<src1>::<route1>|<src2>::<route2>|" — try matching
      # each ordering against the allowlist.
      a="${key%%|*}"; rest="${key#*|}"; b="${rest%%|*}"
      if [[ -f "$ALLOWLIST" ]]; then
        if grep -qE "^${a//\//\\/}	|	${b//\//\\/}" "$ALLOWLIST" 2>/dev/null || \
           grep -qE "^${b//\//\\/}	|	${a//\//\\/}" "$ALLOWLIST" 2>/dev/null; then
          continue
        fi
      fi
      unallowlisted=$((unallowlisted + 1))
    done <"$findings_tmp"
  fi
  if [[ "$unallowlisted" -gt 0 ]]; then
    echo "scanner: $unallowlisted un-allowlisted shadow pairs — see specs above + .claude/skills/mux-overlap-scan/SKILL.md" >&2
    exit 1
  fi
  echo "scanner: gate clean" >&2
fi

exit 0
