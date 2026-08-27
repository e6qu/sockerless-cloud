#!/usr/bin/env bash
# Tests for check-latest-deps.sh's drift attribution, including the negative
# controls that prove the gate still fails on drift a branch is answerable for.
#
# Attribution is the one change that can make this gate quieter, so it is the
# one change that could make it stop gating. The controls below are the point
# of this file: a pin the branch moved, and a pin the branch introduced, must
# still fail even with a baseline. Only a pin byte-identical to the baseline's
# is forgiven, and only into the scheduled run.
#
# The fixture is a real git repository holding real, long-superseded pins, and
# the check runs against the real module proxy and the real GitHub API — the
# same code path, the same network, the same drift the gate sees in CI. Nothing
# is stubbed; only the repository the check reads is a fixture.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

# Versions old enough that no quarantine window or future release can make them
# current again, so these cases cannot rot into false passes.
superseded_module='github.com/fxamacker/cbor/v2'
superseded_version='v2.5.0'
# A different superseded version of the same module, standing in for a pin a
# branch moved to something that is still not the newest adoptable version.
other_version='v2.4.0'
superseded_action='actions/checkout'
superseded_action_tag='v4.0.0'

mkdir -p "$fixture/scripts" "$fixture/.github/workflows"
cp "$root/scripts/check-latest-deps.sh" "$fixture/scripts/"
# The Actions half sources its API throttling helper relative to the script, so
# the fixture needs it too. Without it that half dies mid-run and the exit
# status stops meaning what these cases read it as.
cp -R "$root/scripts/lib" "$fixture/scripts/lib"

write_gomod() {
	cat >"$fixture/go.mod" <<EOF
module example.invalid/fixture

go 1.25

require $superseded_module $1
EOF
}

write_workflow() {
	cat >"$fixture/.github/workflows/fixture.yml" <<EOF
name: fixture
on: workflow_dispatch
jobs:
  fixture:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: $superseded_action@$1
EOF
}

git -C "$fixture" init -q
git -C "$fixture" config user.email latest-deps@example.invalid
git -C "$fixture" config user.name 'latest deps fixture'
write_gomod "$superseded_version"
write_workflow "$superseded_action_tag"
git -C "$fixture" add -A
git -C "$fixture" commit -qm 'baseline'
baseline="$(git -C "$fixture" rev-parse HEAD)"

expect() {
	local want_status="$1" want_text="$2" label="$3" out exit_status
	shift 3
	set +e
	out="$(cd "$fixture" && GITHUB_TOKEN="${GITHUB_TOKEN:-}" bash "$fixture/scripts/check-latest-deps.sh" "$@" 2>&1)"
	exit_status=$?
	set -e
	if [[ "$exit_status" != "$want_status" ]]; then
		echo "$label: expected exit $want_status, got $exit_status" >&2
		printf '%s\n' "$out" >&2
		exit 1
	fi
	if [[ "$out" != *"$want_text"* ]]; then
		echo "$label: expected output to contain '$want_text'" >&2
		printf '%s\n' "$out" >&2
		exit 1
	fi
	echo "ok: $label"
}

# 1. No baseline: every drift fails. This is the scheduled run's form, and the
#    reason inherited drift is relocated rather than forgiven.
expect 1 "FAIL  .: $superseded_module pinned $superseded_version" \
	'a superseded Go pin fails when nothing is attributed'

expect 1 "FAIL  .github/workflows/fixture.yml: $superseded_action pinned $superseded_action_tag" \
	'a superseded action tag fails when nothing is attributed'

# 2. The baseline records the same pins, so the branch did not let anything
#    rot: upstream moved underneath it. Reported, annotated, not failed.
expect 0 "INHERITED  .: $superseded_module pinned $superseded_version" \
	'a Go pin unchanged from the baseline is inherited' --baseline "$baseline"

expect 0 "INHERITED  .github/workflows/fixture.yml: $superseded_action pinned $superseded_action_tag" \
	'an action tag unchanged from the baseline is inherited' --baseline "$baseline"

# 3. Negative control: the branch moved the pin. It is the branch's own, and a
#    baseline must not forgive it just because the result is still behind.
write_gomod "$other_version"
expect 1 "FAIL  .: $superseded_module pinned $other_version" \
	'a Go pin the branch moved still fails against a baseline' --baseline "$baseline"
write_gomod "$superseded_version"

# 4. Negative control: absence is not inheritance. A file the baseline does not
#    have is a file the branch added, and its pins are the branch's own.
mkdir -p "$fixture/added"
cat >"$fixture/added/go.mod" <<EOF
module example.invalid/fixture/added

go 1.25

require $superseded_module $superseded_version
EOF
git -C "$fixture" add -A
expect 1 "FAIL  added: $superseded_module pinned $superseded_version" \
	'a module the branch added is not inherited' --baseline "$baseline"
rm -rf "$fixture/added"
git -C "$fixture" add -A

# 5. The summary has to say so. Inherited drift printed with no count reads as
#    a clean run to anyone skimming, which is how a quiet gate goes unnoticed.
expect 0 "dependency drift(s) inherited from $baseline" \
	'the summary reports how much drift was inherited' --baseline "$baseline"

echo
echo 'check-latest-deps baseline attribution: drift a branch owns still fails'
