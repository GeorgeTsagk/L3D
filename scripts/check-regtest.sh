#!/usr/bin/env bash
set -euo pipefail
binary=${1:?path to btcd is required}
temp=$(mktemp -d)
trap 'rm -rf "$temp"' EXIT
touch "$temp/btcd.conf"
for network in mainnet testnet simnet; do
    args=()
    if [[ "$network" != mainnet ]]; then
        args+=("--$network")
    fi
    if "$binary" --configfile="$temp/btcd.conf" --experimental-layertree "${args[@]}" > "$temp/guard.log" 2>&1; then
        echo "LayerTree unexpectedly enabled on $network" >&2
        exit 1
    fi
    if ! grep -q 'experimental-layertree requires regtest' "$temp/guard.log"; then
        cat "$temp/guard.log" >&2
        exit 1
    fi
done
echo 'Experimental LayerTree activation is restricted to regtest.'
