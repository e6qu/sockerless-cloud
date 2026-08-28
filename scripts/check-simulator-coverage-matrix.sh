#!/usr/bin/env bash
# Verifies that the simulator client-surface coverage matrix stays in
# lockstep with the canonical simulator surface tables.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
matrix="$repo_root/specs/SIM_TEST_COVERAGE_MATRIX.md"
parity_matrix="$repo_root/specs/SIM_PARITY_MATRIX.md"
tables_dir="$repo_root/specs/SIM_SURFACE_TABLES"

if [[ ! -f "$matrix" ]]; then
    echo "[sim-coverage-matrix] missing $matrix" >&2
    exit 1
fi
if [[ ! -f "$parity_matrix" ]]; then
    echo "[sim-coverage-matrix] missing $parity_matrix" >&2
    exit 1
fi

tmp_expected="$(mktemp)"
tmp_actual="$(mktemp)"
tmp_dupes="$(mktemp)"
trap 'rm -f "$tmp_expected" "$tmp_actual" "$tmp_dupes"' EXIT

find "$tables_dir" -maxdepth 1 -type f -name '*.md' ! -name README.md \
    -exec basename {} .md \; | sort >"$tmp_expected"

# shellcheck disable=SC2016
matrix_row_re='s/^\| `([^`]+)` \|.*/\1/p'

sed -nE "$matrix_row_re" "$matrix" | sort >"$tmp_actual"

if ! diff -u "$tmp_expected" "$tmp_actual"; then
    echo "[sim-coverage-matrix] matrix rows must exactly match specs/SIM_SURFACE_TABLES/*.md" >&2
    exit 1
fi

sed -nE "$matrix_row_re" "$matrix" | sort | uniq -d >"$tmp_dupes"
if [[ -s "$tmp_dupes" ]]; then
    echo "[sim-coverage-matrix] duplicate matrix rows:" >&2
    cat "$tmp_dupes" >&2
    exit 1
fi

if grep -nE '\b(TBD|TODO|unknown|gap)\b' "$matrix" >&2; then
    echo "[sim-coverage-matrix] unresolved placeholder/gap wording in matrix" >&2
    exit 1
fi

if grep -n '^| `.*tracked' "$matrix" | grep -Ev '(#[0-9]+|BUG-[0-9]+)' >&2; then
    echo "[sim-coverage-matrix] tracked rows must cite a GitHub issue or BUG id" >&2
    exit 1
fi

missing_paths=0
# shellcheck disable=SC2016
path_re='`simulator-[^`]+`|`specs/[^`]+`'
while IFS= read -r tablefile; do
	[[ "$tablefile" == *'$'* ]] && continue
	if [[ ! -e "$repo_root/$tablefile" ]]; then
		echo "[sim-coverage-matrix] evidence path does not exist: $tablefile" >&2
		missing_paths=1
	fi
done < <(grep -oE "$path_re" "$matrix" | tr -d '`' | sort -u)

if [[ "$missing_paths" -ne 0 ]]; then
    exit 1
fi

missing_aws_parity=0
while IFS= read -r surface; do
    if ! grep -Fq "(SIM_SURFACE_TABLES/$surface.md)" "$parity_matrix"; then
        echo "[sim-coverage-matrix] AWS surface missing from parity inventory: $surface" >&2
        missing_aws_parity=1
    fi
done < <(find "$tables_dir" -maxdepth 1 -type f -name 'aws-*.md' \
    -exec basename {} .md \; | sort)

if [[ "$missing_aws_parity" -ne 0 ]]; then
    exit 1
fi

echo "[sim-coverage-matrix] ok"
