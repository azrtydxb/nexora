#!/usr/bin/env bash
# Commit explicit paths from the main checkout through a throwaway worktree, so the
# pre-commit gate only sees these paths (not other agents' unfinished files), then
# fast-forward main.  Usage: scripts/commit-paths.sh "message" path...
set -euo pipefail
msg=$1; shift
root=$(git rev-parse --show-toplevel)
wt=$(mktemp -d)/wt
git -C "$root" worktree add -q --detach "$wt" HEAD
trap 'git -C "$root" worktree remove --force "$wt" >/dev/null 2>&1 || true' EXIT
for p in "$@"; do
	for f in $(cd "$root" && git ls-files -m -o --exclude-standard -- "$p"; cd "$root" && git ls-files -d -- "$p"); do
		if [ -e "$root/$f" ]; then mkdir -p "$wt/$(dirname "$f")" && cp -p "$root/$f" "$wt/$f"; else rm -f "$wt/$f"; fi
	done
done
git -C "$wt" add -A -- "$@"
git -C "$wt" commit -q -m "$msg"
sha=$(git -C "$wt" rev-parse HEAD)
# The main checkout already holds these exact bytes, so move the branch and
# refresh only the index; never touch working files other agents are editing.
old=$(git -C "$root" rev-parse HEAD)
[ "$(git -C "$wt" rev-parse HEAD^)" = "$old" ] || { echo "main moved during commit; retry" >&2; exit 1; }
git -C "$root" update-ref "refs/heads/$(git -C "$root" symbolic-ref --short HEAD)" "$sha" "$old"
git -C "$root" reset -q -- "$@"
git -C "$root" log --oneline -1
