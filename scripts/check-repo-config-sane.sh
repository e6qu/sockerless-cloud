#!/usr/bin/env bash
# Fails when this checkout's own git configuration has been corrupted in a way
# that makes the next commit or push wrong or unexplainable: `core.bare = true`
# on a checkout that has a work tree, or a local identity a test fixture uses
# (scripts/test-latest-deps-*.sh give their throwaway repositories one). Neither
# is reachable through this repository's own tooling, so the cause is outside
# it and may recur. The bare flag otherwise surfaces as `fatal: this operation
# must be run in a work tree` deep in a pre-commit stack trace, and a fixture
# identity would author commits silently; this says both outright.
set -euo pipefail

root="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
problems=0

# A checkout with a work tree is not a bare repository, whatever the config
# says. Git believes the config, so every work-tree operation fails until it is
# corrected.
if [[ -d "$root/.git" ]]; then
    bare="$(git -C "$root" config --local --get core.bare || true)"
    if [[ "$bare" == "true" ]]; then
        echo "[repo-config] .git/config says core.bare=true, but $root has a work tree." >&2
        echo "[repo-config] Every work-tree command fails until this is corrected:" >&2
        echo "[repo-config]     git -C $root config --local core.bare false" >&2
        problems=1
    fi
fi

# An identity a test fixture uses is never the identity a human commits under.
email="$(git -C "$root" config --get user.email || true)"
name="$(git -C "$root" config --get user.name || true)"
case "$email" in
*@example.invalid | *@example.com)
    echo "[repo-config] the commit identity is a test fixture's: $name <$email>." >&2
    echo "[repo-config] Commits made now would be authored as that fixture. Set the real one:" >&2
    echo "[repo-config]     git -C $root config --local user.email <you@example>" >&2
    problems=1
    ;;
esac
if [[ -z "$email" || -z "$name" ]]; then
    echo "[repo-config] no commit identity is configured, so a commit would be attributed to nobody." >&2
    problems=1
fi

exit "$problems"
