#!/bin/sh
# install.sh test harness.
#
#   sh scripts/test-install/run.sh [--dry|--docker] [scenario...]
#
#   --dry     (default) no Docker: sources install.sh and runs its file-level
#             steps (env merge, managed files, KEK, cloudflared ingress edit)
#             against temporary directories. Scenarios: sync fresh upgrade
#             legacy-caddy modified obsolete kek cloudflared
#   --docker  runs install.sh for real inside throw-away docker:dind
#             containers. Scenarios: fresh upgrade legacy-caddy modified obsolete
set -eu
HERE=$(cd -- "$(dirname -- "$0")" && pwd -P)
mode=--dry
case "${1:-}" in --dry | --docker) mode=$1; shift ;; -h | --help) sed -n '2,11p' "$0"; exit 0 ;; esac
case "$mode" in
--dry) exec bash "$HERE/dry.sh" "$@" ;;
--docker) exec sh "$HERE/docker.sh" "$@" ;;
esac
