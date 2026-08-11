#!/usr/bin/env bash
# check-behavioral-coverage.sh — enforce classification of AWS simulator
# background-evaluator / listener / dispatch patterns.
#
# Every persistent background loop or listener added to simulator-aws/*.go
# must be recorded in specs/AWS_BEHAVIORAL_PATTERNS.md with an allowed
# classification and a canonical SDK behavioral test.
#
# Usage:
#   scripts/check-behavioral-coverage.sh          # check staged changes
#   scripts/check-behavioral-coverage.sh --ref HEAD^  # check between ref and HEAD
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="$repo_root/specs/AWS_BEHAVIORAL_PATTERNS.md"
allowed_classes=(background-evaluator listener dispatch audit)

ref="${1:-}"
if [[ "$ref" == "--ref" && -n "${2:-}" ]]; then
    staged_range="$2..HEAD"
else
    staged_range="--cached"
fi

if [[ ! -f "$registry" ]]; then
    echo "[behavioral-coverage] missing registry: $registry" >&2
    exit 1
fi

class_re="$(printf '%s|' "${allowed_classes[@]}" | sed 's/|$//')"
tmp_rows="$(mktemp)"
trap 'rm -f "$tmp_rows"' EXIT

# Parse the markdown table: skip header and separator lines, extract columns.
awk '
    /^\| *`[^`]+` *\|/ {
        gsub(/^\|[[:space:]]*/, "")
        gsub(/[[:space:]]*\|$/, "")
        split($0, cols, "|")
        for (i in cols) gsub(/^[[:space:]]+|[[:space:]]+$/, "", cols[i])
        # cols[1] = pattern (backtick quoted), cols[2] = class, cols[3] = source, cols[4] = test
        if (cols[1] ~ /^`[^`]+`$/ && cols[2] != "" && cols[3] != "" && cols[4] != "") {
            gsub(/^`|`$/, "", cols[1])
            gsub(/^`|`$/, "", cols[3])
            gsub(/^`|`$/, "", cols[4])
            print cols[1] "\t" cols[2] "\t" cols[3] "\t" cols[4]
        }
    }
' "$registry" >"$tmp_rows"

fail=0

# 1. Validate registry rows: classification allowed, source/test files exist.
while IFS=$'\t' read -r pattern class source test; do
    if ! echo "$class" | grep -qxE "($class_re)"; then
        echo "[behavioral-coverage] FAIL: pattern '$pattern' has invalid classification '$class' (allowed: ${allowed_classes[*]})" >&2
        fail=1
        continue
    fi
    if [[ "$class" != "audit" ]]; then
        if [[ ! -f "$repo_root/$source" ]]; then
            echo "[behavioral-coverage] FAIL: pattern '$pattern' source file missing: $source" >&2
            fail=1
        fi
    else
        if [[ ! -e "$repo_root/$source" ]]; then
            echo "[behavioral-coverage] FAIL: pattern '$pattern' audit source path missing: $source" >&2
            fail=1
        fi
    fi
    if [[ ! -f "$repo_root/$test" ]]; then
        echo "[behavioral-coverage] FAIL: pattern '$pattern' test file missing: $test" >&2
        fail=1
    fi
done <"$tmp_rows"

# 2. No duplicate pattern names.
dupes=$(awk -F'\t' '{print $1}' "$tmp_rows" | sort | uniq -d || true)
if [[ -n "$dupes" ]]; then
    echo "[behavioral-coverage] FAIL: duplicate pattern names in registry:" >&2
    echo "$dupes" >&2
    fail=1
fi

# 3. Every sdk-tests/*behavioral*_test.go file must be registered as a test.
for bf in "$repo_root"/simulator-aws/sdk-tests/*behavioral*_test.go; do
    [[ -f "$bf" ]] || continue
    rel="${bf#"$repo_root"/}"
    if ! awk -F'\t' -v t="$rel" '$4 == t' "$tmp_rows" | grep -q .; then
        echo "[behavioral-coverage] FAIL: behavioral test file not registered in $registry: $rel" >&2
        fail=1
    fi
done

# 4. Detect newly-added persistent background loops / listeners in simulator-aws/*.go
#    and require them to be registered (or listed on tests-exempt.txt / behavioral-exempt.txt).
#    Detection: a top-level `func start*` whose body (next 20 lines) contains
#    `go func` plus either a ticker/timer (`time.NewTicker`, `time.NewTimer`,
#    `time.Tick`) or a long-running listener (`net.Listen`, `net.ListenPacket`,
#    `.Serve(`).
changed_go=$(git diff --name-only "$staged_range" 2>/dev/null \
    | grep -E '^simulator-aws/[^/]+\.go$' \
    | grep -vE '(_test\.go|/docs/|/README\.md)$' \
    || true)

exempt_file="$repo_root/simulator-aws/behavioral-exempt.txt"

is_exempt() {
    local pattern="$1"
    [[ -f "$exempt_file" ]] && grep -qxF "$pattern" "$exempt_file"
}

is_registered_source() {
    local file="$1"
        local rel="${file#"$repo_root"/}"
    awk -F'\t' -v s="$rel" '$3 == s' "$tmp_rows" | grep -q .
}

if [[ -n "$changed_go" ]]; then
    for f in $changed_go; do
        abs="$repo_root/$f"
        # Extract each `func start...` block up to the first blank line or closing brace at column 0.
        awk '
            /^func start[A-Za-z0-9_]+\s*\(/ { inblock=1; buf=""; }
            inblock {
                buf = buf $0 "\n"
                if (/^\}/) { print buf; inblock=0; buf=""; next }
                if (/^$/) { inblock=0; buf=""; }
            }
        ' "$abs" | awk -v f="$f" -v RS='' '
            /go func\(\)/ && (/time\.NewTicker|time\.NewTimer|time\.Tick/ || /net\.Listen|net\.ListenPacket|\.Serve\(/) {
                print f
                exit
            }
        '
    done | sort -u >"$tmp_rows.patterns" || true

    while IFS= read -r src; do
        [[ -n "$src" ]] || continue
        pattern_name=$(basename "$src" .go | tr '_' '-')
        if ! is_registered_source "$repo_root/$src" && ! is_exempt "$pattern_name"; then
            echo "[behavioral-coverage] FAIL: newly-added background pattern '$pattern_name' ($src) is not registered in $registry" >&2
            echo "  Add a row with classification and behavioral test, or list '$pattern_name' in $exempt_file if it is internal/inert." >&2
            fail=1
        fi
    done <"$tmp_rows.patterns"
    rm -f "$tmp_rows.patterns"
fi

if [[ "$fail" -ne 0 ]]; then
    exit 1
fi

echo "[behavioral-coverage] ok"
