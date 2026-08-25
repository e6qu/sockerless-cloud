#!/usr/bin/env bash
# check-behavioral-coverage.sh — enforce classification of AWS simulator
# background-evaluator / listener / dispatch patterns.
#
# Every persistent background loop or listener added to simulator-aws/*.go
# must be recorded in specs/AWS_BEHAVIORAL_PATTERNS.md with an allowed
# classification and a canonical SDK behavioral test.
#
# Usage:
#   scripts/check-behavioral-coverage.sh   # checks the whole tree
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
registry="$repo_root/specs/AWS_BEHAVIORAL_PATTERNS.md"
allowed_classes=(background-evaluator listener dispatch audit)

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

# 4. Detect persistent background loops / listeners in simulator-aws/*.go and
#    require the file that hosts them to be registered as a pattern source (or
#    the pattern listed in behavioral-exempt.txt). The scan covers the whole
#    tree, not a diff range: the registry promises that EVERY persistent loop
#    in simulator-aws/*.go is classified, so the detector must keep matching a
#    worker after any refactor of its launch shape, not only in the commit
#    that introduced it.
#
#    A top-level function counts as launching a persistent worker when its
#    body contains either launch shape:
#      - a raw `go func()` goroutine, or
#      - a handoff to the server-owned background-worker lifecycle,
#        `StartBackground(func(ctx context.Context) {...})`,
#    combined with a persistence marker in the same body: a ticker/timer
#    (`time.NewTicker`, `time.NewTimer`, `time.Tick`) or a long-running
#    listener (`net.Listen`, `net.ListenPacket`, `.Serve(`). Bodies without a
#    marker (e.g. a one-shot per-resource lifecycle handed to StartBackground)
#    are transient, not persistent, and stay out of the registry.
scanned_go=""
for f in "$repo_root"/simulator-aws/*.go; do
    [[ -f "$f" ]] || continue
    [[ "$f" == *_test.go ]] && continue
    scanned_go+="${f#"$repo_root"/}"$'\n'
done

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

if [[ -n "$scanned_go" ]]; then
    for f in $scanned_go; do
        abs="$repo_root/$f"
        # Walk top-level function blocks (a `func ` at column 0 through the
        # closing `}` at column 0) and report the file when any block launches
        # a persistent worker in either shape described above.
        awk -v f="$f" '
            /^func / { inblock=1; buf=""; }
            inblock {
                buf = buf $0 "\n"
                if (/^\}/) {
                    launches = (buf ~ /go func\(\)/ || buf ~ /StartBackground\(/)
                    persists = (buf ~ /time\.NewTicker|time\.NewTimer|time\.Tick/ \
                        || buf ~ /net\.Listen|net\.ListenPacket|\.Serve\(/)
                    if (launches && persists) { print f; exit }
                    inblock=0; buf="";
                }
            }
        ' "$abs"
    done | sort -u >"$tmp_rows.patterns" || true

    while IFS= read -r src; do
        [[ -n "$src" ]] || continue
        pattern_name=$(basename "$src" .go | tr '_' '-')
        if ! is_registered_source "$repo_root/$src" && ! is_exempt "$pattern_name"; then
            echo "[behavioral-coverage] FAIL: background pattern '$pattern_name' ($src) is not registered in $registry" >&2
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
