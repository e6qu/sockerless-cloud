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
if ((remaining_releases > keep)); then
	echo "$package retained $remaining_releases releases; expected at most $keep" >&2
	exit 1
fi

remaining_unrecognized="$(jq '[.[] | select(
	(.metadata.container.tags | length) == 0
	or any(.metadata.container.tags[]; test("^[0-9a-f]{12}(-(amd64|arm64))?$"; "i") | not)
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

remaining_versions="$(jq 'length' "$remaining_versions_file")"
maximum_versions=$((keep * 3))
if ((remaining_versions > maximum_versions)); then
	echo "$package retained $remaining_versions package versions; expected at most $maximum_versions for $keep releases" >&2
	exit 1
fi

echo "$package retained $remaining_releases immutable release(s) across $remaining_versions package version(s)"
