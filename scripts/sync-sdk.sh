#!/usr/bin/env bash
# Copies the shared SDK from a vad-client checkout into ./sdk, which the server
# builds against (go.mod: replace => ./sdk). The vad-client repository is the
# source of truth for the SDK; run this after changing it there, then commit
# both repositories.
#   scripts/sync-sdk.sh [path/to/vad-client]   (default ../vad-client)
set -euo pipefail
root=$(cd -- "$(dirname -- "$0")/.." && pwd)
src="${1:-$root/../vad-client}/sdk"
[ -f "$src/go.mod" ] || { echo "no SDK at $src" >&2; exit 1; }
rm -rf -- "$root/sdk"
cp -r -- "$src" "$root/sdk"
echo "SDK copied from $src"
