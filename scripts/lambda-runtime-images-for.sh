#!/usr/bin/env bash
# Prints the AWS Lambda runtime images a suite pulls, one per line, sorted.
#
#   lambda-runtime-images-for.sh <directory>...
#
# The runtime table in the AWS simulator maps some thirty identifiers to base
# images, and a plain source scan of the module reads all thirty — a set no run
# fetches, since an image arrives only when a test invokes a function on that
# runtime. This resolves the other way round: it reads the identifiers the
# sources under the given directories actually name, and prints the images the
# table maps them to. A suite that starts exercising a new runtime is warmed on
# its first run, and the ones nothing invokes stay unfetched.
set -euo pipefail

[ "$#" -gt 0 ] || { echo "lambda-runtime-images-for: at least one directory required" >&2; exit 2; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
table="$root/simulator-aws/lambda_runtime.go"
[ -f "$table" ] || { echo "lambda-runtime-images-for: no runtime table at $table" >&2; exit 2; }

for dir in "$@"; do
    [ -d "$dir" ] || { echo "lambda-runtime-images-for: no such directory: $dir" >&2; exit 2; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The table's own mapping, one `identifier<TAB>image` per line: each
# `case "a", "b":` takes the image its `return` names. Reading it here rather
# than restating it keeps the two from drifting.
awk '
    /^\tcase "/ {
        n = split($0, parts, "\"")
        pending_n = 0
        for (i = 2; i < n; i += 2) pending[++pending_n] = parts[i]
        next
    }
    /^\t\treturn "public\.ecr\.aws\// {
        split($0, r, "\"")
        for (i = 1; i <= pending_n; i++) print pending[i] "\t" r[2]
        pending_n = 0
    }
' "$table" | sort -u > "$work/table"

[ -s "$work/table" ] || { echo "lambda-runtime-images-for: read no runtimes from $table" >&2; exit 1; }

# Every identifier, quoted as a client sends it, as a literal to search for.
sed 's/\t.*//; s/.*/"&"/' "$work/table" | sort -u > "$work/patterns"

# Only what a suite names. The module's own sources are where the table and the
# API's validation list live, so they name every identifier there is — reading
# them back would resolve the whole table and cache the thirty again. An image
# is fetched when a test invokes a function on that runtime, and a test that
# does names the runtime it asks for.
find "$@" -type f \( -name '*_test.go' -o -name '*.tf' -o -name '*.tftpl' \) -print0 |
    xargs -0 grep -hoF -f "$work/patterns" |
    tr -d '"' | sort -u > "$work/named"

join -t "$(printf '\t')" "$work/named" "$work/table" | cut -f2- | sort -u
