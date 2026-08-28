#!/usr/bin/env bash
# run-fuzz.sh — exploratory fuzzing across modules.
#
# Runs every Go fuzz target found in the requested modules for a fixed duration.
# Targets execute in bounded parallel batches so the nightly jobs finish within
# their 15-minute limit without multiplying Go's own fuzz-worker concurrency.
set -u

seconds="${FUZZTIME_SECONDS:-60}"
target_concurrency="${FUZZ_TARGET_CONCURRENCY:-4}"
fuzz_parallel="${FUZZ_PARALLEL:-1}"
# Which slice of the discovered targets to run, so a group too large for the
# job's time budget can be split across matrix entries. 1/1 runs everything.
shard_index="${FUZZ_SHARD_INDEX:-1}"
shard_total="${FUZZ_SHARD_TOTAL:-1}"
artifact_dir=".fuzz-artifacts"
work_dir="$(mktemp -d)"
task_file="$work_dir/tasks"
exit_status=0

trap 'rm -rf "$work_dir"' EXIT
rm -rf "$artifact_dir"

for value_name in seconds target_concurrency fuzz_parallel shard_index shard_total; do
	value="${!value_name}"
	if [[ ! "$value" =~ ^[1-9][0-9]*$ ]]; then
		echo "$value_name must be a positive integer, got: $value" >&2
		exit 2
	fi
done

if ((shard_index > shard_total)); then
	echo "shard_index must not exceed shard_total, got: $shard_index of $shard_total" >&2
	exit 2
fi

collect_new_crashers() {
	local crasher destination
	while IFS= read -r crasher; do
		[[ -n "$crasher" ]] || continue
		destination="$artifact_dir/$crasher"
		mkdir -p "$(dirname "$destination")"
		cp "$crasher" "$destination"
	done < <(git ls-files --others --exclude-standard -- '*/testdata/fuzz/*/*')
}

discover_targets() {
	local dir file package_dir module_dir relative function_name
	for dir in "$@"; do
		if [[ ! -f "$dir/go.mod" ]]; then
			echo "required fuzz module has no go.mod: $dir" >&2
			exit_status=1
			continue
		fi
		while IFS= read -r file; do
			[[ -n "$file" ]] || continue
			package_dir="$(dirname "$file")"
			module_dir="$package_dir"
			while [[ "$module_dir" != "$dir" && ! -f "$module_dir/go.mod" ]]; do
				module_dir="$(dirname "$module_dir")"
			done
			[[ "$module_dir" == "$dir" ]] || continue
			relative="."
			if [[ "$package_dir" != "$dir" ]]; then
				relative="./${package_dir#"$dir"/}"
			fi
			while IFS= read -r function_name; do
				[[ -n "$function_name" ]] || continue
				printf '%s\t%s\t%s\n' "$dir" "$relative" "$function_name" >>"$task_file"
			done < <(grep -oE '^func Fuzz[A-Za-z0-9_]+' "$file" | sed 's/^func //')
		done < <(grep -rl '^func Fuzz' "$dir" --include='*_test.go' 2>/dev/null || true)
	done
}

run_target() {
	local dir="$1" relative="$2" function_name="$3" log_file="$4"
	{
		echo "=== [$dir] $relative $function_name (${seconds}s) ==="
		cd "$dir" || return
		CGO_ENABLED=0 go test -tags=noui -run='^$' -fuzz="^${function_name}\$" -fuzztime="${seconds}s" -parallel="$fuzz_parallel" "$relative"
	} >"$log_file" 2>&1
}

# Go's fuzz coordinator suppresses the -fuzztime deadline error by comparing
# it against its worker context's error — but the deadline context closes its
# Done channel before cancellation propagates to that child context, so a
# coordinator that wins the race reports the engine's own stop as the test
# failure: a FAIL block whose only diagnostic is a bare
# "context deadline exceeded", stamped with an elapsed time at or past the
# -fuzztime budget, with no failing input written. golang.org/issue/72104
# tracks Go's own test_fuzz_fuzztime flaking on the same signature. That shape
# — and only that shape — is the engine racing its own shutdown, not a
# crasher; requalify it as a pass. Any other failure (a written failing input,
# a panic, a second FAIL block, an early deadline) still fails the run.
is_fuzztime_shutdown_race() {
	local log_file="$1" package_dir="$2" elapsed
	[[ "$(grep -c '^--- FAIL: ' "$log_file")" == 1 ]] || return 1
	grep -q '^--- FAIL: Fuzz' "$log_file" || return 1
	grep -qx '    context deadline exceeded' "$log_file" || return 1
	if grep -qE 'panic:|race detected|Failing input written to' "$log_file"; then
		return 1
	fi
	elapsed="$(sed -nE 's/^--- FAIL: Fuzz[A-Za-z0-9_]+ \(([0-9]+\.[0-9]+)s\)$/\1/p' "$log_file")"
	[[ -n "$elapsed" ]] || return 1
	awk -v elapsed="$elapsed" -v budget="$seconds" \
		'BEGIN { exit !(elapsed + 0 >= budget + 0) }' || return 1
	[[ -z "$(git ls-files --others --exclude-standard -- "$package_dir/testdata/fuzz" 2>/dev/null)" ]] || return 1
}

wait_batch() {
	local index pid label log_file package_dir
	for ((index = 0; index < ${#batch_pids[@]}; index += 1)); do
		pid="${batch_pids[$index]}"
		label="${batch_labels[$index]}"
		log_file="${batch_logs[$index]}"
		package_dir="${batch_package_dirs[$index]}"
		if ! wait "$pid"; then
			if is_fuzztime_shutdown_race "$log_file" "$package_dir"; then
				echo "~~~ fuzztime shutdown race (engine stopped itself at the -fuzztime boundary, no crasher): $label"
			else
				echo "!!! FUZZ TARGET FAILED: $label" >&2
				exit_status=1
			fi
		fi
		cat "$log_file"
	done
	batch_pids=()
	batch_labels=()
	batch_logs=()
	batch_package_dirs=()
}

# Compile each package's test binary once, serially, before any fuzzing starts.
#
# Without this the first batch is `target_concurrency` cold `go test -fuzz`
# invocations against the same package, each compiling it from scratch at the
# same time, so the build peaks at N simultaneous copies of the largest module.
# The aws group is the biggest (25 targets against one package) and is the one
# that died: three nightlies and a manual re-run in a row were killed about
# seven minutes in, the runner reporting a shutdown signal, before a single
# target had reported. A warm cache makes those parallel invocations near-free.
#
# This also makes a build failure say so, instead of surfacing as 25 fuzz
# targets failing at once.
prebuild_packages() {
	local dir relative
	while IFS=$'\t' read -r dir relative; do
		[[ -n "$relative" ]] || continue
		echo "=== building test binary for [$dir] $relative ==="
		if ! (cd "$dir" && CGO_ENABLED=0 go test -tags=noui -c -o /dev/null "$relative"); then
			echo "!!! FAILED TO BUILD: $dir $relative" >&2
			exit_status=1
		fi
	done < <(cut -f1,2 "$task_file" | sort -u)
}

# Keep only this shard's targets. Deal them round-robin rather than in
# contiguous blocks so each shard gets a mix of packages: the targets are
# discovered in file order, and slicing by block would hand one shard every
# target of the slowest package.
#
# `idx` rather than `index`: index() is a built-in awk function, so -v index=1
# is a syntax error. awk then writes nothing, the shard file is empty, and the
# job fuzzes no targets at all and still exits 0 — a green run that tested
# nothing. Hence the count check below: an empty shard is a bug in this filter,
# never a legitimate outcome, so it fails loudly instead.
select_shard() {
	local kept selected
	((shard_total > 1)) || return 0
	kept="$work_dir/tasks.shard"
	if ! awk -v idx="$shard_index" -v total="$shard_total" 'NR % total == idx % total' \
		"$task_file" >"$kept"; then
		echo "sharding failed to select targets" >&2
		exit 2
	fi
	mv "$kept" "$task_file"
	selected=$(wc -l <"$task_file" | tr -d ' ')
	if ((selected == 0)); then
		echo "shard $shard_index of $shard_total selected no targets" >&2
		exit 2
	fi
	echo "=== shard $shard_index of $shard_total: $selected targets ==="
}

: >"$task_file"
discover_targets "$@"
select_shard
prebuild_packages
batch_pids=()
batch_labels=()
batch_logs=()
batch_package_dirs=()
task_index=0
while IFS=$'\t' read -r dir relative function_name; do
	[[ -n "$function_name" ]] || continue
	log_file="$work_dir/target-$task_index.log"
	package_dir="$dir"
	if [[ "$relative" != "." ]]; then
		package_dir="$dir/${relative#./}"
	fi
	# Announce before forking. Each target's own output is buffered to a file
	# and only replayed once its whole batch has been waited on, so a job that
	# dies mid-batch otherwise leaves no trace of how far it got -- which is
	# exactly what made the aws failures unreadable.
	echo "--- starting [$dir] $relative $function_name ---"
	run_target "$dir" "$relative" "$function_name" "$log_file" &
	batch_pids+=("$!")
	batch_labels+=("$dir $relative $function_name")
	batch_logs+=("$log_file")
	batch_package_dirs+=("$package_dir")
	task_index=$((task_index + 1))
	if ((${#batch_pids[@]} >= target_concurrency)); then
		wait_batch
	fi
done <"$task_file"
if ((${#batch_pids[@]} > 0)); then
	wait_batch
fi

if ((exit_status != 0)); then
	collect_new_crashers
	if [[ -d "$artifact_dir" ]]; then
		echo "fuzzing found at least one new crasher — minimized inputs are in $artifact_dir" >&2
	else
		echo "at least one fuzz target failed without producing a new crasher" >&2
	fi
fi
exit "$exit_status"
