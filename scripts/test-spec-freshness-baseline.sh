#!/usr/bin/env bash
# Tests for check-spec-freshness.sh's drift attribution, including the negative
# controls that prove the gate still fails on drift a branch is answerable for.
#
# The fixture is a real git repository holding a real SOURCES.md row pinned to a
# real, superseded upstream commit, and the check runs against the real GitHub
# API — the same code path, the same network, the same drift the gate sees in
# CI. Nothing here is stubbed; only the repository the check reads is a fixture.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

# A commit of aws-sdk-go-v2 that is no longer the tip for this path: the
# vendored Glue model was pinned here until the branch that carried
# c06550cb44f3 advanced it, so upstream is guaranteed to have moved past it.
superseded_pin='701936ed82997e9cbd0dd2bab29f9d2b1b3b5ff5'
# An older commit of the same repository, standing in for a pin a branch
# changed to something that is still not upstream tip.
other_pin='0000000000000000000000000000000000000000'

mkdir -p "$fixture/scripts" "$fixture/specs/cloud-api/aws"
cp "$root/scripts/check-spec-freshness.sh" "$fixture/scripts/"

write_sources() {
	{
		echo '# Vendored Amazon Web Services API models'
		echo
		echo '| file | repository | path | licence | pin | vendored |'
		echo '| --- | --- | --- | --- | --- | --- |'
		printf '%s\n' "$@"
	} >"$fixture/specs/cloud-api/aws/SOURCES.md"
}

glue_row() {
	echo "| \`glue.smithy.json.gz\` | \`aws/aws-sdk-go-v2\` | \`codegen/sdk-codegen/aws-models/glue.json\` | Apache-2.0 | \`$1\` | 2026-08-01T00:00:00Z |"
}

git -C "$fixture" init -q
git -C "$fixture" config user.email spec-freshness@example.invalid
git -C "$fixture" config user.name 'spec freshness fixture'
write_sources "$(glue_row "$superseded_pin")"
git -C "$fixture" add -A
git -C "$fixture" commit -qm 'baseline'
baseline="$(git -C "$fixture" rev-parse HEAD)"

run_check() {
	local out status
	set +e
	out="$(bash "$fixture/scripts/check-spec-freshness.sh" "$@" 2>&1)"
	status=$?
	set -e
	printf '%s\n' "$out"
	return "$status"
}

expect() {
	local want_status="$1" want_text="$2" label="$3" out status
	shift 3
	set +e
	out="$(run_check "$@")"
	status=$?
	set -e
	if [[ "$status" != "$want_status" ]]; then
		echo "$label: expected exit $want_status, got $status" >&2
		printf '%s\n' "$out" >&2
		exit 1
	fi
	if [[ "$out" != *"$want_text"* ]]; then
		echo "$label: expected output to mention '$want_text'" >&2
		printf '%s\n' "$out" >&2
		exit 1
	fi
}

# Strict mode still fails on a stale pin: this is the scheduled run, and it is
# the negative control for the whole attribution idea.
expect 1 'DRIFT glue.smithy.json.gz' 'strict mode on a stale pin' aws

# The branch left the row exactly as the baseline recorded it, so the drift is
# the baseline's and this branch is not the place to absorb it.
expect 0 'INHERITED glue.smithy.json.gz' 'inherited drift' --baseline "$baseline" aws

# Negative control: the branch changed the pin, so it owns the row and is held
# to upstream tip even under a baseline.
write_sources "$(glue_row "$other_pin")"
expect 1 'DRIFT glue.smithy.json.gz' 'branch rolled the pin back' --baseline "$baseline" aws

# Negative control: a row the baseline never recorded is the branch's own and
# is checked strictly.
write_sources "$(glue_row "$superseded_pin")" \
	"| \`iam.smithy.json.gz\` | \`aws/aws-sdk-go-v2\` | \`codegen/sdk-codegen/aws-models/iam.json\` | Apache-2.0 | \`$other_pin\` | 2026-08-01T00:00:00Z |"
expect 1 'DRIFT iam.smithy.json.gz' 'row absent from the baseline' --baseline "$baseline" aws

# Negative control: a baseline ref that is not present must fail loudly rather
# than quietly degrade to strict or to permissive.
expect 2 'is not available' 'missing baseline ref' --baseline deadbeefdeadbeefdeadbeefdeadbeefdeadbeef aws

echo 'specification freshness attribution tests passed'
