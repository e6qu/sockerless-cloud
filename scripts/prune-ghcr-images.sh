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

# A publish takes minutes per architecture, and this prune is triggered by
# another publish finishing, so it can run while a sibling is between its two
# per-arch pushes. Two hours is comfortably longer than any publish and only
# delays retention by one cycle.
grace="${PRUNE_GRACE_SECONDS:-7200}"

jq -r --argjson keep "$keep" --argjson grace "$grace" \
	-f "$(dirname "${BASH_SOURCE[0]}")/select-obsolete-container-versions.jq" \
	"$versions_file" |
	while IFS= read -r version_id; do
		echo "deleting obsolete $package package version $version_id"
		gh api --method DELETE "$base/$version_id"
	done

gh api --paginate "$base?per_page=100" | jq -s 'add' >"$remaining_versions_file"

bash "$(dirname "${BASH_SOURCE[0]}")/assert-ghcr-retention.sh" \
	"$package" "$keep" "$remaining_versions_file"
