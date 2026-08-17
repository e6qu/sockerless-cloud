#!/usr/bin/env bash
set -euo pipefail

owner="${1:?usage: prune-ghcr-images.sh <owner> <package> [release-count]}"
package="${2:?usage: prune-ghcr-images.sh <owner> <package> [release-count]}"
keep="${3:-20}"

if [[ ! "$keep" =~ ^[1-9][0-9]*$ ]]; then
	echo "release count must be a positive integer: $keep" >&2
	exit 1
fi

case "$(gh api "/users/$owner" --jq .type)" in
	Organization) package_namespace=orgs ;;
	User) package_namespace=users ;;
	*)
		echo "unsupported GitHub package owner: $owner" >&2
		exit 1
		;;
esac

base="/$package_namespace/$owner/packages/container/$package/versions"
versions_file="$(mktemp)"
remaining_versions_file="$(mktemp)"
trap 'rm -f "$versions_file" "$remaining_versions_file"' EXIT

gh api --paginate "$base?per_page=100" | jq -s 'add' >"$versions_file"

jq -r --argjson keep "$keep" \
	-f "$(dirname "${BASH_SOURCE[0]}")/select-obsolete-container-versions.jq" \
	"$versions_file" |
	while IFS= read -r version_id; do
		echo "deleting obsolete $package package version $version_id"
		gh api --method DELETE "$base/$version_id"
	done

gh api --paginate "$base?per_page=100" | jq -s 'add' >"$remaining_versions_file"

remaining_releases="$(jq '[.[].metadata.container.tags[]? | select(test("^[0-9a-f]{12}$"))] | unique | length' "$remaining_versions_file")"

# Only the releases this script is permitted to delete are held to the limit.
# GHCR coalesces byte-identical tags into one indivisible package version, so a
# short-SHA tag whose image is identical to a published vX.Y.Z release lives on
# that release's package version. Those versions are immortal by policy -- a
# published release must stay pullable forever -- and deleting one to satisfy a
# short-SHA budget would unpublish the release with it.
#
# Counting them anyway made this gate fail on a correctly pruned package and
# guaranteed it would keep failing: for sockerless-simulator-aws, 21 short-SHA
# releases remained against a limit of 20, of which 10 were pinned to v0.3.0
# through v0.12.0 and only 11 were prunable. Every release adds another, so once
# the release count reaches the limit no amount of pruning can satisfy it.
prunable_releases="$(jq '[.[]
	| select(all((.metadata.container.tags // [])[];
		test("^v[0-9]+\\.[0-9]+\\.[0-9]+(-(amd64|arm64))?$"; "i") | not))
	| (.metadata.container.tags // [])[]
	| select(test("^[0-9a-f]{12}$"))
] | unique | length' "$remaining_versions_file")"
if ((prunable_releases > keep)); then
	echo "$package retained $prunable_releases prunable releases; expected at most $keep" >&2
	exit 1
fi

remaining_unrecognized="$(jq '[.[] | select(
	(.metadata.container.tags | length) == 0
	or any(.metadata.container.tags[]; test("^([0-9a-f]{12}|v[0-9]+\\.[0-9]+\\.[0-9]+)(-(amd64|arm64))?$"; "i") | not)
)] | length' "$remaining_versions_file")"
if ((remaining_unrecognized > 0)); then
	echo "$package retained $remaining_unrecognized untagged or non-release package version(s)" >&2
	exit 1
fi

remaining_incomplete="$(jq '[.[].metadata.container.tags[]?] as $tags
	| [ $tags[]
		| select(test("^[0-9a-f]{12}$"))
		| . as $tag
		| select(
			($tags | index($tag + "-amd64")) == null
			or ($tags | index($tag + "-arm64")) == null
		)
	] | unique | length' "$remaining_versions_file")"
if ((remaining_incomplete > 0)); then
	echo "$package retained $remaining_incomplete incomplete immutable release(s)" >&2
	exit 1
fi

# Versioned release images (vX.Y.Z) are immortal and accumulate by design;
# only the short-SHA stream is bounded.
remaining_sha_versions="$(jq '[.[] | select(
	(.metadata.container.tags | length) > 0
	and all(.metadata.container.tags[]; test("^[0-9a-f]{12}(-(amd64|arm64))?$"; "i"))
)] | length' "$remaining_versions_file")"
remaining_versions="$(jq 'length' "$remaining_versions_file")"
maximum_versions=$((keep * 3))
if ((remaining_sha_versions > maximum_versions)); then
	echo "$package retained $remaining_sha_versions short-SHA package versions; expected at most $maximum_versions for $keep releases" >&2
	exit 1
fi

echo "$package retained $remaining_releases immutable release(s) across $remaining_versions package version(s)"
