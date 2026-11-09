#!/usr/bin/env bash
# Prints the Cloud SQL engine images a suite pulls, one per line, sorted.
#
#   cloudsql-version-images-for.sh <directory>...
#
# The version table in the Google Cloud simulator maps every databaseVersion
# with a community release to that release's image, and a plain source scan of
# the module reads all of them — a set no run fetches, since an engine image
# arrives only when a test starts an instance on that version. This resolves
# the other way round, as lambda-runtime-images-for.sh does: it reads the
# versions the tests and Terraform stacks under the given directories name,
# adds the version instances.insert defaults to, and prints the images the
# table maps them to.
set -euo pipefail

[ "$#" -gt 0 ] || { echo "cloudsql-version-images-for: at least one directory required" >&2; exit 2; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
table="$root/simulator-gcp/sqladmin_versions.go"
[ -f "$table" ] || { echo "cloudsql-version-images-for: no version table at $table" >&2; exit 2; }

for dir in "$@"; do
    [ -d "$dir" ] || { echo "cloudsql-version-images-for: no such directory: $dir" >&2; exit 2; }
done

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The table's rows, one `version<TAB>image` per line.
sed -nE 's/^\t\{"([A-Z0-9_]+)", "[A-Z0-9_]+", "([^"]+)"\},$/\1\t\2/p' "$table" | sort -u > "$work/table"
[ -s "$work/table" ] || { echo "cloudsql-version-images-for: read no versions from $table" >&2; exit 1; }

default="$(sed -nE 's/^const sqlDefaultDatabaseVersion = "([A-Z0-9_]+)"$/\1/p' "$table")"
[ -n "$default" ] || { echo "cloudsql-version-images-for: no default version in $table" >&2; exit 1; }

cut -f1 "$work/table" > "$work/patterns"

# Only what a test or a stack names: the module's own sources name every
# version there is.
{
    echo "$default"
    find "$@" -type f \( -name '*_test.go' -o -name '*.tf' -o -name '*.tftpl' \) -print0 |
        xargs -0 grep -howeF -f "$work/patterns" || true
} | sort -u > "$work/named"

join -t "$(printf '\t')" "$work/named" "$work/table" | cut -f2- | sort -u
