#!/usr/bin/env bash
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
source "$root/scripts/backend.env"
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
mkdir -p "$temp/scripts" "$temp/patches" "$temp/.deps"
cp "$root/scripts/bootstrap-btcd.sh" "$root/scripts/backend.env" "$temp/scripts/"
cp "$root/patches/btcd-layertree.patch" "$temp/patches/"
git -c advice.detachedHead=false clone -q --shared "$root/.deps/btcd" "$temp/.deps/btcd"
git -C "$temp/.deps/btcd" apply --index "$temp/patches/btcd-layertree.patch"
"$temp/scripts/bootstrap-btcd.sh"
"$temp/scripts/bootstrap-btcd.sh"

expect_rejection() {
    if "$temp/scripts/bootstrap-btcd.sh" > "$temp/output" 2>&1; then
        echo 'Bootstrap accepted a changed backend.' >&2
        exit 1
    fi
    grep -q 'Backend differs from the pinned source' "$temp/output"
}

file="$temp/.deps/btcd/README.md"
cp "$file" "$temp/original"
echo 'local edit must survive' >> "$file"
expect_rejection
grep -q 'local edit must survive' "$file"
cp "$temp/original" "$file"
echo 'package main' > "$temp/.deps/btcd/untracked.go"
expect_rejection
test -f "$temp/.deps/btcd/untracked.go"
rm "$temp/.deps/btcd/untracked.go"
"$temp/scripts/bootstrap-btcd.sh"
echo 'Bootstrap verifies the pin, is repeatable and preserves rejected local edits.'
