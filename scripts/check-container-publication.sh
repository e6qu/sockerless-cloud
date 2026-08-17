#!/usr/bin/env bash
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
workflow="$root/.github/workflows/publish-container-images.yml"
prune_workflow="$root/.github/workflows/prune-container-images.yml"
release_workflow="$root/.github/workflows/release.yml"
gha='$'

expect_count() {
	local expected="$1" literal="$2" actual
	actual="$(grep -Fxc -- "$literal" "$workflow" || true)"
	if [[ "$actual" != "$expected" ]]; then
		echo "publication workflow expected $expected exact occurrence(s), found $actual: $literal" >&2
		exit 1
	fi
}

expect_count 1 '    branches: [main]'
expect_count 1 '          - { platform: linux/amd64, runner: ubuntu-latest, suffix: amd64 }'
expect_count 1 '          - { platform: linux/arm64, runner: ubuntu-24.04-arm, suffix: arm64 }'
expect_count 1 "          tags: ${gha}{{ env.REGISTRY }}/e6qu/${gha}{{ matrix.image.name }}:${gha}{{ needs.prepare.outputs.short_sha }}-${gha}{{ matrix.arch.suffix }}"
expect_count 1 '          provenance: false'
expect_count 1 "          labels: org.opencontainers.image.revision=${gha}{{ github.sha }}"
if [[ "$(grep -Fc "test \"\$MEDIA_TYPE\" = \"application/vnd.oci.image.manifest.v1+json\"" "$workflow")" != 1 ]]; then
	echo 'publication workflow must verify both architecture tags are direct OCI manifests' >&2
	exit 1
fi
if [[ "$(grep -Fc "test \"\$MEDIA_TYPE\" = \"application/vnd.oci.image.index.v1+json\"" "$workflow")" != 1 ]]; then
	echo 'publication workflow must verify the generic tag is an OCI index' >&2
	exit 1
fi
# A publish produces the immutable artifact for one commit and no later commit
# can produce it, so its concurrency group is the commit and nothing supersedes
# it. scripts/check-workflow-concurrency.sh states the rule for every workflow;
# these two literals pin the shape for the one that publishes images.
expect_count 1 "  group: ${gha}{{ github.workflow }}-${gha}{{ github.repository }}-${gha}{{ github.sha }}"
expect_count 1 '  cancel-in-progress: false'

for image in sockerless-simulator-aws sockerless-simulator-gcp sockerless-simulator-azure; do
	count="$(grep -Fc -- "$image" "$workflow")"
	if [[ "$count" != 2 ]]; then
		echo "publication workflow expected $image in the build and manifest matrices, found $count occurrence(s)" >&2
		exit 1
	fi
done

# Retention is its own workflow: pruning must be serialized and latest-wins,
# which is the opposite of what publishing needs, and sharing a run forced one
# of them to be wrong. It still runs on every publish — the completion of the
# publish workflow triggers it — with a schedule as the backstop.
if [[ ! -f "$prune_workflow" ]]; then
	echo "retention workflow is missing: $prune_workflow" >&2
	echo 'registry retention runs there, triggered by the publication workflow completing.' >&2
	exit 1
fi

expect_prune_count() {
	local expected="$1" literal="$2" actual
	actual="$(grep -Fxc -- "$literal" "$prune_workflow" || true)"
	if [[ "$actual" != "$expected" ]]; then
		echo "retention workflow expected $expected exact occurrence(s), found $actual: $literal" >&2
		exit 1
	fi
}

expect_prune_count 1 '    workflows: [Publish container images]'
expect_prune_count 1 '    types: [completed]'
expect_prune_count 1 "        run: ./scripts/prune-ghcr-images.sh \"${gha}{{ github.repository_owner }}\" \"${gha}{{ matrix.image }}\" 20"
publish_name="$(sed -n 's/^name: //p' "$workflow")"
if [[ "$publish_name" != 'Publish container images' ]]; then
	echo "publication workflow is named '$publish_name'; the retention workflow triggers on 'Publish container images'" >&2
	exit 1
fi
for image in sockerless-simulator-aws sockerless-simulator-gcp sockerless-simulator-azure; do
	count="$(grep -Fc -- "$image" "$prune_workflow")"
	if [[ "$count" != 1 ]]; then
		echo "retention workflow expected $image in the retention matrix, found $count occurrence(s)" >&2
		exit 1
	fi
done

if grep -Eiq 'tags?:[^#]*(:(latest|main))([[:space:]]|$)' "$workflow"; then
	echo 'publication workflow must not publish latest or main image tags' >&2
	exit 1
fi

# The release workflow publishes the same three images once per release-please
# release: one architecture tag per native runner (vX.Y.Z-amd64 / vX.Y.Z-arm64)
# and an unsuffixed vX.Y.Z OCI index composed from exactly those two — never a
# latest or floating tag.
expect_release_count() {
	local expected="$1" literal="$2" actual
	actual="$(grep -Fxc -- "$literal" "$release_workflow" || true)"
	if [[ "$actual" != "$expected" ]]; then
		echo "release workflow expected $expected exact occurrence(s), found $actual: $literal" >&2
		exit 1
	fi
}

expect_release_count 1 '          - { platform: linux/amd64, runner: ubuntu-latest, suffix: amd64 }'
expect_release_count 1 '          - { platform: linux/arm64, runner: ubuntu-24.04-arm, suffix: arm64 }'
expect_release_count 1 "          tags: ghcr.io/e6qu/${gha}{{ matrix.image.name }}:${gha}{{ inputs.tag_name }}-${gha}{{ matrix.arch.suffix }}"
expect_release_count 1 '          provenance: false'
expect_release_count 1 "          labels: org.opencontainers.image.revision=${gha}{{ github.sha }}"
if [[ "$(grep -Fc "test \"\$MEDIA_TYPE\" = \"application/vnd.oci.image.manifest.v1+json\"" "$release_workflow")" != 1 ]]; then
	echo 'release workflow must verify both architecture tags are direct OCI manifests' >&2
	exit 1
fi
if [[ "$(grep -Fc "test \"\$MEDIA_TYPE\" = \"application/vnd.oci.image.index.v1+json\"" "$release_workflow")" != 1 ]]; then
	echo 'release workflow must verify the generic tag is an OCI index' >&2
	exit 1
fi
for image in sockerless-simulator-aws sockerless-simulator-gcp sockerless-simulator-azure; do
	count="$(grep -Fc -- "$image" "$release_workflow")"
	if [[ "$count" != 2 ]]; then
		echo "release workflow expected $image in the image and manifest matrices, found $count occurrence(s)" >&2
		exit 1
	fi
done
if grep -Eiq 'tags?:[^#]*(:(latest|main))([[:space:]]|$)' "$release_workflow"; then
	echo 'release workflow must not publish latest or main image tags' >&2
	exit 1
fi

fixture="$(mktemp)"
trap 'rm -f "$fixture"' EXIT
jq -n '[
	range(0; 22) as $release
	| (("000000000000" + ($release | tostring))[-12:]) as $tag
	| range(0; 3) as $kind
	| {
		id: ($release * 10 + $kind),
		created_at: ("2026-07-" + (("00" + (($release + 1) | tostring))[-2:]) + "T00:00:00Z"),
		metadata: {container: {tags: [
			if $kind == 0 then $tag
			elif $kind == 1 then ($tag + "-amd64")
			else ($tag + "-arm64") end
		]}}
	}
] + [
	{id: 997, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["feedfacefeed"]}}},
	{id: 998, created_at: "2026-08-02T00:00:00Z", metadata: {container: {tags: ["latest"]}}},
	{id: 999, created_at: "2026-08-03T00:00:00Z", metadata: {container: {tags: []}}}
]' >"$fixture"

selected="$(jq -r --argjson keep 20 -f "$root/scripts/select-obsolete-container-versions.jq" "$fixture" | sort -n | paste -sd, -)"
if [[ "$selected" != '0,1,2,10,11,12,997,998,999' ]]; then
	echo "retention selector chose unexpected package versions: $selected" >&2
	exit 1
fi

shared_fixture="$(mktemp)"
trap 'rm -f "$fixture" "$shared_fixture"' EXIT
jq -n '[
	{id: 10, created_at: "2026-08-03T00:00:00Z", metadata: {container: {tags: ["cafebabecafe"]}}},
	{id: 11, created_at: "2026-08-03T00:00:00Z", metadata: {container: {tags: ["cafebabecafe-amd64"]}}},
	{id: 12, created_at: "2026-08-03T00:00:00Z", metadata: {container: {tags: ["cafebabecafe-arm64"]}}},
	{id: 1, created_at: "2026-08-02T00:00:00Z", metadata: {container: {tags: ["feedfacefeed", "deadbeefdead"]}}},
	{id: 2, created_at: "2026-08-02T00:00:00Z", metadata: {container: {tags: ["feedfacefeed-amd64", "deadbeefdead-amd64"]}}},
	{id: 3, created_at: "2026-08-02T00:00:00Z", metadata: {container: {tags: ["feedfacefeed-arm64", "deadbeefdead-arm64"]}}}
]' >"$shared_fixture"

selected="$(jq -r --argjson keep 2 -f "$root/scripts/select-obsolete-container-versions.jq" "$shared_fixture" | sort -n | paste -sd, -)"
if [[ "$selected" != '1,2,3' ]]; then
	echo "retention selector split a shared release component at its limit: $selected" >&2
	exit 1
fi

selected="$(jq -r --argjson keep 3 -f "$root/scripts/select-obsolete-container-versions.jq" "$shared_fixture" | sort -n | paste -sd, -)"
if [[ -n "$selected" ]]; then
	echo "retention selector did not preserve a complete shared release component: $selected" >&2
	exit 1
fi

# scripts/assert-ghcr-retention.sh holds the pruned package to the retention
# contract. The shapes below are the ones the live registry produces, so the
# assertions are exercised against real geometry rather than only against
# whatever GHCR happens to hold on the day a publish runs.
retention_fixture="$(mktemp)"
trap 'rm -f "$fixture" "$shared_fixture" "$retention_fixture"' EXIT

# releases_fixture <count> <coalesced> writes a listing of <count> short-SHA
# releases, the first <coalesced> of which share their package versions with an
# immortal vX.Y.Z release — the coalescing GHCR performs when a release's image
# is byte-identical to the main-push image it was cut from.
releases_fixture() {
	jq -n --argjson count "$1" --argjson coalesced "$2" '[
		range(0; $count) as $release
		| (("000000000000" + ($release | tostring))[-12:]) as $tag
		| ("v0." + (($release + 1) | tostring) + ".0") as $version
		| range(0; 3) as $kind
		| (if $kind == 0 then "" elif $kind == 1 then "-amd64" else "-arm64" end) as $suffix
		| {
			id: ($release * 10 + $kind),
			created_at: ("2026-07-" + (("00" + (($release + 1) | tostring))[-2:]) + "T00:00:00Z"),
			metadata: {container: {tags: (
				[$tag + $suffix]
				+ (if $release < $coalesced then [$version + $suffix] else [] end)
			)}}
		}
	]' >"$retention_fixture"
}

expect_retention_pass() {
	if ! bash "$root/scripts/assert-ghcr-retention.sh" fixture-package "$1" "$retention_fixture" >/dev/null; then
		echo "expected the retention contract to hold: $2" >&2
		exit 1
	fi
}

expect_retention_fail() {
	local message
	if message="$(bash "$root/scripts/assert-ghcr-retention.sh" fixture-package "$1" "$retention_fixture" 2>&1)"; then
		echo "expected the retention contract to fail: $2" >&2
		exit 1
	fi
	if [[ "$message" != *"$3"* ]]; then
		echo "retention contract failed for the wrong reason: wanted '$3', got '$message'" >&2
		exit 1
	fi
}

# The live shape that made this gate unsatisfiable: 21 short-SHA releases over
# a limit of 20, ten of them pinned to published releases and therefore
# undeletable, leaving 11 the pruner may actually remove. Counting the pinned
# ones failed a package that was pruned as far as policy permits, and every
# release added another, so no amount of pruning could ever have satisfied it.
releases_fixture 21 10
expect_retention_pass 20 '21 releases of which only 11 are prunable'

# Negative control: the same 21 releases with none of them pinned to a
# published release are 21 the pruner could have deleted, and the limit stands.
releases_fixture 21 0
expect_retention_fail 20 '21 prunable releases over a limit of 20' 'retained 21 prunable releases'

# Negative control: an untagged or unrecognised package version is a version
# nothing accounts for.
releases_fixture 3 0
jq '. + [{id: 900, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: []}}}]' \
	"$retention_fixture" >"$retention_fixture.tmp" && mv "$retention_fixture.tmp" "$retention_fixture"
expect_retention_fail 20 'an untagged package version' 'untagged or non-release package version'

# Negative control: a release missing one of its two architecture tags is half
# published, and a pull of the index would fail on that platform.
releases_fixture 3 0
jq '[.[] | select((.metadata.container.tags[0] | endswith("-arm64")) | not)]' \
	"$retention_fixture" >"$retention_fixture.tmp" && mv "$retention_fixture.tmp" "$retention_fixture"
expect_retention_fail 20 'a release missing its arm64 tag' 'incomplete immutable release'

# Negative control: architecture tags with no index tag above them. A publish
# that dies between pushing the two architecture images and composing the
# index leaves exactly this, and it is invisible to the release assertions —
# there is no bare short-SHA tag to count. The package-version ceiling is what
# notices the versions accumulating.
releases_fixture 1 0
jq '. + [
	{id: 800, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["abcabcabcabc-amd64"]}}},
	{id: 801, created_at: "2026-08-01T00:00:00Z", metadata: {container: {tags: ["abcabcabcabc-arm64"]}}}
]' "$retention_fixture" >"$retention_fixture.tmp" && mv "$retention_fixture.tmp" "$retention_fixture"
expect_retention_fail 1 'orphaned architecture tags from an interrupted publish' 'short-SHA package versions'

echo 'container publication workflow contract passed'
