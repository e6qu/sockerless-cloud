#!/usr/bin/env bash
# Fails when this checkout's own git configuration has been corrupted in a way
# that makes the next commit or push wrong or unexplainable.
#
# Both conditions below were observed here on 2026-09-04, mid-session:
# `.git/config` had acquired `core.bare = true` on a checkout that plainly has
# a work tree, and the local identity had been replaced by a test fixture's
# (`latest deps fixture <latest-deps@example.invalid>`, the identity
# scripts/test-latest-deps-*.sh give their throwaway repositories). Neither is
# reachable through this repository's own tooling — both of those scripts build
# their fixtures under `mktemp -d` and address them with `git -C` — so the cause
# is outside it and may recur.
#
# What made it expensive was the shape of the failure, not the failure. The
# bare flag surfaced as a pre-commit stack trace ending in
# `fatal: this operation must be run in a work tree`, several layers below
# anything naming git configuration, and the fixture identity would not have
# surfaced at all: it would simply have authored somebody's commits as a test
# fixture until a reviewer noticed. This says both outright.
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
