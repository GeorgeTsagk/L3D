#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$root/scripts/backend.env"
dest="$root/.deps/btcd"

verify() {
    test "$(git -C "$1" rev-parse HEAD)" = "$BTCD_COMMIT" &&
    test "$(git -C "$1" write-tree)" = "$BTCD_TREE" &&
    git -C "$1" diff --quiet &&
    test -z "$(git -C "$1" ls-files --others --exclude-standard)"
}

if [[ -e "$dest" ]]; then
    if ! verify "$dest"; then
        echo 'Backend differs from the pinned source. Save any edits, then run make clean-deps.' >&2
        exit 1
    fi
    exit 0
fi

mkdir -p "$root/.deps"
lock="$root/.deps/bootstrap.lock"
if ! mkdir "$lock" 2>/dev/null; then
    echo 'Another backend bootstrap is running. Retry after it finishes.' >&2
    exit 1
fi
temp=''
trap 'if [[ -n "$temp" ]]; then rm -rf "$temp"; fi; rmdir "$lock"' EXIT
# Another invocation may have finished between the first check and the lock.
if [[ -e "$dest" ]]; then
    verify "$dest"
    exit
fi
temp=$(mktemp -d "$root/.deps/btcd.XXXXXX")
git -C "$temp" init -q
git -C "$temp" remote add origin "$BTCD_REPO"
git -C "$temp" fetch -q --depth=1 origin "$BTCD_COMMIT"
git -C "$temp" -c advice.detachedHead=false checkout -q FETCH_HEAD
git -C "$temp" -c core.autocrlf=false apply --index "$root/patches/btcd-layertree.patch"
if ! verify "$temp"; then
    echo 'Patched backend failed its source tree check.' >&2
    exit 1
fi
mv "$temp" "$dest"
echo "Prepared btcd at $BTCD_COMMIT with the LayerTree regtest patch."
