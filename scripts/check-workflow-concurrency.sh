#!/usr/bin/env bash
# check-workflow-concurrency.sh — no workflow may let a later commit cancel an
# earlier commit's run.
#
# A `concurrency` group keyed by branch (github.ref_name, github.ref, or a
# constant) collapses every push to that branch into one slot, and
# cancel-in-progress then kills the run under way each time a new commit lands.
# For a workflow that runs on `push` that is a loss of the commit's own result:
# the publish workflow left merged commits with no container image at all —
# `sockerless-simulator-{aws,gcp,azure}` had none for 36a81145cbb0, b52cc80edf47,
# c06550cb44f3, b9d651fb5a1c, b01a8e29385e or 418e0c8482f2 — and CI left main
# commits with no verdict. Neither can be recovered without a manual re-run,
# because no later commit produces the missing commit's artifact.
#
# The rule: a workflow triggered by `push` that declares a `concurrency` group
# must key that group on ${{ github.sha }}, so the group has exactly one member
# and nothing supersedes it. Workflows not triggered by `push` (pull_request,
# workflow_run, schedule, workflow_dispatch, workflow_call) are free to cancel:
# there the superseding run subsumes the superseded one.
#
# Usage: check-workflow-concurrency.sh [workflow-dir]
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
workflow_dir="${1:-$root/.github/workflows}"
failed=0
checked=0

for workflow in "$workflow_dir"/*.yml "$workflow_dir"/*.yaml; do
	[[ -f "$workflow" ]] || continue
	checked=$((checked + 1))
	section=""
	has_push=0
	has_concurrency=0
	group=""
	cancel=""
	while IFS= read -r line || [[ -n "$line" ]]; do
		# A key at column zero opens a new top-level section.
		if [[ "$line" =~ ^[^[:space:]#][^:]*: ]]; then
			section="${line%%:*}"
			section="${section//\"/}"
			section="${section//\'/}"
			rest="${line#*:}"
			if [[ "$section" == "on" ]]; then
				# Flow forms: `on: push` and `on: [push, pull_request]`.
				for token in ${rest//[\[\],]/ }; do
					[[ "$token" == "push" ]] && has_push=1
				done
			fi
			[[ "$section" == "concurrency" ]] && has_concurrency=1
			continue
		fi
		case "$section" in
			on)
				# Block forms: `  push:` and `  - push`.
				[[ "$line" =~ ^\ \ push: ]] && has_push=1
				[[ "$line" =~ ^\ \ -\ *push\ *$ ]] && has_push=1
				;;
			concurrency)
				if [[ "$line" =~ ^\ \ group:\ *(.*)$ ]]; then
					group="${BASH_REMATCH[1]}"
				fi
				if [[ "$line" =~ ^\ \ cancel-in-progress:\ *(.*)$ ]]; then
					cancel="${BASH_REMATCH[1]}"
				fi
				;;
		esac
	done <"$workflow"

	if ((has_concurrency == 0)); then
		continue
	fi
	if [[ -z "$group" ]]; then
		echo "$workflow: concurrency block declares no group" >&2
		failed=1
		continue
	fi
	if ((has_push == 0)); then
		continue
	fi
	if [[ "$group" != *'github.sha'* ]]; then
		echo "$workflow: runs on push and groups concurrency by '$group', which a later commit shares" >&2
		echo "  key the group on \${{ github.sha }} so a merge cannot cancel an earlier commit's run" >&2
		failed=1
		continue
	fi
	if [[ "$cancel" == "true" && "$group" != *'pull_request'* ]]; then
		echo "$workflow: cancel-in-progress: true on a push workflow whose group is not pull-request scoped" >&2
		failed=1
	fi
done

if ((checked == 0)); then
	echo "check-workflow-concurrency: no workflows found under $workflow_dir" >&2
	exit 1
fi

if ((failed != 0)); then
	exit 1
fi

echo "checked $checked workflow(s): no push-triggered run can be cancelled by a later commit"
