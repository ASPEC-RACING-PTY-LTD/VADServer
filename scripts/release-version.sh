#!/usr/bin/env bash
# Works out the version and tag of a release from the existing git tags.
#
#   scripts/release-version.sh production|development   -> prints "version=… tag=…"
#   scripts/release-version.sh bump X.Y.Z               -> prints the next version
#
# Rules:
#   * Only production releases raise the version, by one step each: every part
#     runs 0-9 and then carries, so 0.0.9 -> 0.1.0 and 0.9.9 -> 1.0.0. Commits
#     between releases do not count. The first production release is 0.1.0.
#   * A production run on a commit that already has a production tag reuses it
#     (re-running a release does not skip a version).
#   * Development releases use the latest production version (0.0.0 before
#     the first one) with the tag vX.Y.Z-dev, which is moved on every
#     development release.
set -euo pipefail

bump() {
	local major minor patch
	IFS=. read -r major minor patch <<<"$1"
	patch=$((10#$patch + 1))
	if ((patch > 9)); then patch=0; minor=$((10#$minor + 1)); fi
	if ((minor > 9)); then minor=0; major=$((10#$major + 1)); fi
	printf '%d.%d.%d\n' "$major" "$minor" "$patch"
}

prod_tags() { git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' || true; }

latest_prod() { prod_tags | sed 's/^v//' | sort -t. -k1,1n -k2,2n -k3,3n | tail -n1; }

case "${1:-}" in
bump)
	bump "${2:?version}"
	;;
production)
	here=$(git tag --points-at HEAD | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n1 || true)
	if [ -n "$here" ]; then
		v=${here#v}
	else
		last=$(latest_prod)
		if [ -z "$last" ]; then v=0.1.0; else v=$(bump "$last"); fi
	fi
	printf 'version=%s\ntag=v%s\n' "$v" "$v"
	;;
development)
	v=$(latest_prod)
	v=${v:-0.0.0}
	printf 'version=%s-dev\ntag=v%s-dev\n' "$v" "$v"
	;;
*)
	echo "usage: $0 production|development | bump X.Y.Z" >&2
	exit 2
	;;
esac
