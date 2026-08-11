#!/usr/bin/env bash
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
workflow_dir="${1:-$root/.github/workflows}"
maximum=15
failed=0

finish_job() {
	if [[ -n "$current_job" && $has_timeout -eq 0 && $reusable -eq 0 ]]; then
		echo "$workflow: job '$current_job' has no timeout-minutes limit" >&2
		failed=1
	fi
}

for workflow in "$workflow_dir"/*.yml "$workflow_dir"/*.yaml; do
	[[ -f "$workflow" ]] || continue
	if grep -Eq '(^|[[:space:]])du([[:space:]]+[^[:space:]]+)*[[:space:]]+/([[:space:]]|$)' "$workflow"; then
		echo "$workflow: recursively scanning the whole runner volume can consume the job timeout" >&2
		failed=1
	fi
	in_jobs=0
	current_job=""
	has_timeout=0
	reusable=0
	while IFS= read -r line || [[ -n "$line" ]]; do
		if [[ "$line" =~ ^jobs:[[:space:]]*$ ]]; then
			in_jobs=1
			continue
		fi
		if [[ $in_jobs -eq 1 && "$line" =~ ^[^[:space:]#] ]]; then
			finish_job
			in_jobs=0
			current_job=""
			continue
		fi
		if [[ $in_jobs -eq 1 && "$line" =~ ^[[:space:]][[:space:]]([a-zA-Z0-9_-]+):[[:space:]]*$ ]]; then
			finish_job
			current_job="${BASH_REMATCH[1]}"
			has_timeout=0
			reusable=0
			continue
		fi
		if [[ -n "$current_job" && "$line" =~ ^[[:space:]]{4}uses:[[:space:]] ]]; then
			reusable=1
		fi
		if [[ -n "$current_job" && "$line" =~ ^[[:space:]]{4}timeout-minutes:[[:space:]]*(.+)[[:space:]]*$ ]]; then
			has_timeout=1
			value="${BASH_REMATCH[1]}"
			if [[ "$value" =~ ^[0-9]+$ ]]; then
				if ((value < 1 || value > maximum)); then
					echo "$workflow: job '$current_job' timeout-minutes=$value exceeds $maximum" >&2
					failed=1
				fi
			elif [[ "$value" != "\${{ matrix.timeout_minutes }}" ]]; then
				echo "$workflow: job '$current_job' uses an unverifiable timeout-minutes value: $value" >&2
				failed=1
			fi
		fi
		if [[ "$line" =~ timeout_minutes:[[:space:]]*([0-9]+)[[:space:]]*$ ]]; then
			matrix_timeout="${BASH_REMATCH[1]}"
			if ((matrix_timeout < 1 || matrix_timeout > maximum)); then
				echo "$workflow: matrix timeout_minutes=$matrix_timeout exceeds $maximum" >&2
				failed=1
			fi
		fi
	done <"$workflow"
	finish_job
done

if ((failed != 0)); then
	exit 1
fi

echo "all GitHub Actions jobs have a verifiable timeout of at most ${maximum} minutes and avoid whole-volume scans"
