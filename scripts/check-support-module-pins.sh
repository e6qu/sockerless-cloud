#!/usr/bin/env bash
# check-support-module-pins.sh — every installable module's pin of a module
# this repository publishes must carry the working tree's content.
#
# `go install github.com/e6qu/sockerless-cloud/simulator-<cloud>@<version>`
# resolves sim, realexec and ui-auth from the pseudo-versions pinned in each
# simulator's go.mod, not from the working tree. A local build sees the tree
# through go.work, so a fix landed in a support module and never pinned ships
# in no installed binary while every workspace build and test passes: the
# ui-auth callback timeout fix went out that way, pinned a week behind the
# module it was fixed in.
#
# check-installable-build.sh only proves the pinned versions compile. This gate
# downloads each pinned version and requires its content to equal the working
# tree's copy of that module, so a pin is either current or the build is red.
#
# The pin cannot point at a commit that does not exist yet, so a change to a
# support module lands in two pushes: push the branch, then pin the pushed
# commit (`go get github.com/e6qu/sockerless-cloud/<module>@<sha>`) and push
# again. A squash-merged commit is content-identical to the branch head it
# squashed, so the pin stays current on main.
set -euo pipefail

if ! REPO_ROOT=$(git rev-parse --show-toplevel 2>/dev/null); then
	REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
fi
cd "$REPO_ROOT"

# pinned_support_modules prints "path version" for every module this repository
# publishes that the given module's build graph selects. A module with none
# prints nothing rather than failing the pipeline grep sits in.
pinned_support_modules() {
	local module=$1 selected
	selected=$(cd "$module" && GOWORK=off go list -m -f '{{if not .Main}}{{.Path}} {{.Version}}{{end}}' all 2>/dev/null)
	grep '^github.com/e6qu/sockerless-cloud/' <<<"$selected" || true
}

failed=0
for module in simulator-aws simulator-gcp simulator-azure; do
	[[ -f $module/go.mod ]] || continue
	while read -r pinned_path version; do
		name=${pinned_path#github.com/e6qu/sockerless-cloud/}
		if [[ ! -d $name ]]; then
			echo "check-support-module-pins: $module pins $pinned_path, which this repository does not contain" >&2
			failed=1
			continue
		fi
		if ! dir=$(cd "$module" && GOWORK=off GOFLAGS=-mod=mod go mod download -json "$pinned_path@$version" | python3 -c 'import json,sys; print(json.load(sys.stdin)["Dir"])'); then
			echo "check-support-module-pins: $module: cannot download $pinned_path@$version" >&2
			failed=1
			continue
		fi
		# A module zip carries the module's files and nothing under a nested
		# module or a VCS directory, plus the repository LICENSE that Go copies
		# into a subdirectory module's zip; the working tree adds only what git
		# ignores.
		if diff=$(diff -r --brief -x .git -x LICENSE -x '*.test' -x .gocache "$name" "$dir" 2>&1); then
			echo "check-support-module-pins: $module pins $name at its working-tree content ($version)"
			continue
		fi
		echo "check-support-module-pins: $module pins $pinned_path@$version, whose content differs from $name/:" >&2
		printf '  %s\n' "$diff" >&2
		echo "  Push the branch, then pin the pushed commit: (cd $module && go get $pinned_path@<sha>)" >&2
		failed=1
	done < <(pinned_support_modules "$module")
done

exit "$failed"
