#!/usr/bin/env bash
# CI licence gate (P0-31). Requires Go and Node; run `go mod download` and
# `npm ci` for the configured projects first. Reads the real LICENSE files of
# every incorporated dependency and fails on anything outside allowlist.json.
#   .licenses/check.sh            check (exit 1 on a violation)
#   .licenses/check.sh --notices  regenerate THIRD_PARTY_NOTICES.md and fail if it changed
#   .licenses/check.sh --report   print the markdown audit table (add --all for the full graph)
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(dirname "$here")"
if [[ "${1:-}" == "--notices" ]]; then
  tmp="$(mktemp)"
  node "$here/check.mjs" --notices >"$tmp"
  if ! cmp -s "$tmp" "$root/THIRD_PARTY_NOTICES.md"; then
    mv "$tmp" "$root/THIRD_PARTY_NOTICES.md"
    echo "THIRD_PARTY_NOTICES.md was out of date and has been regenerated; commit it." >&2
    exit 1
  fi
  rm -f "$tmp"
  echo "THIRD_PARTY_NOTICES.md is up to date."
  exit 0
fi
exec node "$here/check.mjs" "$@"
