#!/usr/bin/env bash
# assert-ghcr-retention.sh — hold a GHCR container package's retained versions
# to the immutable-release contract, given the package's version listing.
#
# Split out of prune-ghcr-images.sh so the contract is a pure function of a
# listing document: the pruner passes the listing it re-read after deleting,
# and scripts/check-container-publication.sh passes fixtures reproducing shapes
# the live registry produces. Both run this same code.
#
# Usage: assert-ghcr-retention.sh <package> <keep> <versions-json>
set -euo pipefail

package="${1:?usage: assert-ghcr-retention.sh <package> <keep> <versions-json>}"
keep="${2:?usage: assert-ghcr-retention.sh <package> <keep> <versions-json>}"
versions_file="${3:?usage: assert-ghcr-retention.sh <package> <keep> <versions-json>}"

if [[ ! "$keep" =~ ^[1-9][0-9]*$ ]]; then
	echo "release count must be a positive integer: $keep" >&2
	exit 1
fi
if [[ ! -f "$versions_file" ]]; then
	echo "package version listing does not exist: $versions_file" >&2
	exit 1
fi

remaining_releases="$(jq '[.[].metadata.container.tags[]? | select(test("^[0-9a-f]{12}$"))] | unique | length' "$versions_file")"

# Only the releases the pruner is permitted to delete are held to the limit.
# GHCR coalesces byte-identical tags into one indivisible package version, so a
# short-SHA tag whose image is identical to a published vX.Y.Z release lives on
# that release's package version. Those versions are immortal by policy -- a
# published release must stay pullable forever -- and deleting one to satisfy a
# short-SHA budget would unpublish the release with it, which is why
# select-obsolete-container-versions.jq never selects them.
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
] | unique | length' "$versions_file")"
if ((prunable_releases > keep)); then
	echo "$package retained $prunable_releases prunable releases; expected at most $keep" >&2
	exit 1
fi

remaining_unrecognized="$(jq '[.[] | select(
	(.metadata.container.tags | length) == 0
	or any(.metadata.container.tags[]; test("^([0-9a-f]{12}|v[0-9]+\\.[0-9]+\\.[0-9]+)(-(amd64|arm64))?$"; "i") | not)
)] | length' "$versions_file")"
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
	] | unique | length' "$versions_file")"
if ((remaining_incomplete > 0)); then
	echo "$package retained $remaining_incomplete incomplete immutable release(s)" >&2
	exit 1
fi

# Versioned release images (vX.Y.Z) are immortal and accumulate by design;
# only the short-SHA stream is bounded. A version carrying a release tag is
# counted with the releases above, not here, so this budget scales with what
# the pruner can actually delete.
remaining_sha_versions="$(jq '[.[] | select(
	(.metadata.container.tags | length) > 0
	and all(.metadata.container.tags[]; test("^[0-9a-f]{12}(-(amd64|arm64))?$"; "i"))
)] | length' "$versions_file")"
remaining_versions="$(jq 'length' "$versions_file")"
maximum_versions=$((keep * 3))
if ((remaining_sha_versions > maximum_versions)); then
	echo "$package retained $remaining_sha_versions short-SHA package versions; expected at most $maximum_versions for $keep releases" >&2
	exit 1
fi

echo "$package retained $remaining_releases immutable release(s) ($prunable_releases prunable) across $remaining_versions package version(s)"
