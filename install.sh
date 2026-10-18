#!/usr/bin/env bash
# VaultDock installer: install / replace, upgrade, integrations, uninstall.
#
#   curl -fsSL https://raw.githubusercontent.com/ASPEC-RACING-PTY-LTD/vad-server/main/install.sh | bash
#   ./install.sh                 # interactive menu
#   ./install.sh install|upgrade|uninstall|status [--yes]
#   ./install.sh upgrade --if-newer   # only upgrade when a newer release exists
#   ./install.sh auto-update on|off|status
#   ./install.sh env-review      # add missing settings, review obsolete ones
#   ./install.sh integrations [cloudflared]   # detect and wire up a Cloudflare Tunnel
#   ./install.sh kek-status      # where the server key file is and whether it exists
#   --approve-env-removal NAME[,NAME]  approve removing obsolete .env settings
#
# Environment overrides:
#   VAULTDOCK_DIR      installation directory (default: $HOME/VaultDock)
#   VAULTDOCK_REPO     GitHub "owner/name" to download from
#                      (default ASPEC-RACING-PTY-LTD/vad-server; the server was
#                      published from ASPEC-RACING-PTY-LTD/VaultDock before)
#   VAULTDOCK_REF      branch or tag to install (default: the latest release,
#                      or main when no release has been published)
#   VAULTDOCK_SOURCE   use this local source directory instead of downloading
#   VAULTDOCK_IMAGE    pull this prebuilt image instead of building locally
#                      (releases use ghcr.io/<repo>:<version> automatically)
#   VAULTDOCK_HTTPS    tunnel | caddy | selfsigned | none (skips the question)
#   VAULTDOCK_PORT, VAULTDOCK_PUBLIC_URL, VAULTDOCK_STORAGE   answers for unattended installs
#   VAULTDOCK_REBUILD=0  reuse an existing local vaultdock:local image
#   VAULTDOCK_YES=1    accept defaults for every prompt (non-interactive;
#                      never approves removing settings)
#   VAULTDOCK_ENV_APPROVE_REMOVE=A,B  same as --approve-env-removal
#
# Only the runtime files are installed (app/: docker-compose.yml, install.sh,
# deploy/*.json, deploy/integrations/ and .env); the server itself runs from a
# container image. Reverse proxies and tunnels are opt-in integrations
# (VAULTDOCK_INTEGRATIONS); none runs by default.
#
# Upgrades never change existing .env values: missing settings are appended
# from deploy/env.schema.json and obsolete ones are removed only with
# per-setting approval. The script never deletes user files, databases or
# configuration without an explicit, typed confirmation.
#
# On a fresh install a 32-byte server key-encryption key (KEK) is generated in
# the state directory (VAULTDOCK_KEK_FILE, default /state/keys/kek). It is
# never printed and never regenerated; back it up off the server.
set -Eeuo pipefail
umask 027

DEFAULT_REPO="ASPEC-RACING-PTY-LTD/vad-server"
# Where releases were published before the server moved to its own repository
# (C21). Its images (ghcr.io/aspec-racing-pty-ltd/vaultdock) stay usable.
LEGACY_REPO="ASPEC-RACING-PTY-LTD/VaultDock"
REPO="${VAULTDOCK_REPO:-$DEFAULT_REPO}"
REF="${VAULTDOCK_REF:-}"
IF_NEWER=0
RELEASE_VERSION=""
YES="${VAULTDOCK_YES:-0}"
PROJECT="vaultdock"

# --- output helpers ----------------------------------------------------------
if [ -t 1 ]; then B=$'\e[1m'; G=$'\e[32m'; Y=$'\e[33m'; R=$'\e[31m'; C=$'\e[36m'; N=$'\e[0m'; else B='' G='' Y='' R='' C='' N=''; fi
info()  { printf '%s\n' "${C}==>${N} $*"; }
ok()    { printf '%s\n' "${G}✔${N} $*"; }
warn()  { printf '%s\n' "${Y}!${N} $*" >&2; }
die()   { printf '%s\n' "${R}✖ $*${N}" >&2; exit 1; }
trap 'die "Unexpected error on line $LINENO. Nothing was deleted; see the messages above."' ERR

# Prompts read from the terminal so `curl ... | bash` still works.
TTY=/dev/tty
have_tty() { [ -r "$TTY" ] && { : <"$TTY"; } 2>/dev/null; }
ask() { # ask VAR "question" "default"
	local __var=$1 q=$2 def=${3-} ans=
	if [ "$YES" = 1 ] || ! have_tty; then printf -v "$__var" '%s' "$def"; return; fi
	read -r -p "$q${def:+ [$def]}: " ans <"$TTY" || true
	printf -v "$__var" '%s' "${ans:-$def}"
}
confirm() { # confirm "question" default(y/n)
	local def=${2:-n} ans
	if [ "$YES" = 1 ]; then [ "$def" = y ]; return; fi
	have_tty || { [ "$def" = y ]; return; }
	read -r -p "$1 [$( [ "$def" = y ] && echo Y/n || echo y/N )]: " ans <"$TTY" || true
	ans=${ans:-$def}
	[[ "$ans" =~ ^[Yy] ]]
}
typed_confirm() { # typed_confirm "phrase" -- always interactive, never auto-accepted
	local ans
	have_tty || die "Destructive actions need an interactive terminal."
	read -r -p "Type '$1' to confirm: " ans <"$TTY" || true
	[ "$ans" = "$1" ]
}

# --- locations -----------------------------------------------------------------
SCRIPT_SRC="${BASH_SOURCE[0]:-}"
SCRIPT_DIR=""
if [ -n "$SCRIPT_SRC" ] && [ -f "$SCRIPT_SRC" ]; then
	SCRIPT_DIR=$(cd -- "$(dirname -- "$SCRIPT_SRC")" && pwd -P)
fi

default_dir() {
	# Installs always go to a dedicated directory, never scattered in $HOME.
	# Run from ~ or /root this is ~/VaultDock or /root/VaultDock.
	if [ -n "${VAULTDOCK_DIR:-}" ]; then printf '%s' "$VAULTDOCK_DIR"; return; fi
	local home=${HOME:-}
	[ -n "$home" ] || home=$(getent passwd "$(id -un)" | cut -d: -f6)
	printf '%s/VaultDock' "${home%/}"
}

DIR=$(default_dir)
APP="$DIR/app"
ENV_FILE="$APP/.env"
LEGACY_ENV="$DIR/.env" # where earlier installers kept the configuration
BACKUPS="$DIR/backups"
DEFAULT_PORT=3000
# Files that make up an installation; everything else is in the container image.
# The list comes from the replace/keep-modified entries of
# deploy/managed-files.json (downloaded first); RUNTIME_FILES is the fallback
# and must stay in sync with it (scripts/test-install checks this).
RUNTIME_FILES="docker-compose.yml install.sh deploy/env.schema.json deploy/managed-files.json deploy/integrations/caddy/compose.caddy.yml deploy/integrations/caddy/Caddyfile deploy/integrations/cloudflared/detect.sh deploy/integrations/cloudflared/compose.cloudflared-net.yml"
# User files in app/ that upgrades carry over.
KEEP_FILES=".env docker-compose.override.yml deploy/env-approvals"

compose() {
	local files=(-f "$APP/docker-compose.yml") i
	for i in $(integrations); do
		if [ -f "$(integration_file "$i")" ]; then files+=(-f "$(integration_file "$i")")
		else warn "Integration $i is enabled but $(integration_file "$i") is missing; skipping it."; fi
	done
	[ -f "$APP/docker-compose.override.yml" ] && files+=(-f "$APP/docker-compose.override.yml")
	docker compose -p "$PROJECT" --project-directory "$APP" "${files[@]}" --env-file "$ENV_FILE" "$@"
}

env_get() { # env_get KEY
	[ -f "$ENV_FILE" ] || return 0
	grep -E "^$1=" "$ENV_FILE" | tail -n1 | cut -d= -f2- || true
}

# --- checks --------------------------------------------------------------------
require() {
	command -v docker >/dev/null 2>&1 || die "Docker is required. Install Docker Engine (https://docs.docker.com/engine/install/) and run this again."
	docker info >/dev/null 2>&1 || die "Cannot talk to the Docker daemon. Is it running, and is your user allowed to use it (sudo or the docker group)?"
	docker compose version >/dev/null 2>&1 || die "The Docker Compose plugin is required (docker compose). Install docker-compose-plugin."
	command -v tar >/dev/null 2>&1 || die "tar is required."
	if [ -z "$(local_source)" ]; then
		command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || die "curl or wget is required to download VaultDock."
	fi
}

is_repo() { [ -n "${1:-}" ] && [ -f "$1/Dockerfile" ] && [ -f "$1/docker-compose.yml" ] && [ -f "$1/go.mod" ]; }

# local_source prints the source tree to install from, if any. The installed
# copy in $APP is never used as a source, so running $APP/install.sh upgrade
# downloads the new release instead of reinstalling itself.
local_source() {
	if [ -n "${VAULTDOCK_SOURCE:-}" ]; then printf '%s' "$VAULTDOCK_SOURCE"; return; fi
	is_repo "$SCRIPT_DIR" || return 0
	local app_real; app_real=$(cd -- "$APP" 2>/dev/null && pwd -P || true)
	[ "$SCRIPT_DIR" = "$app_real" ] || [ "$SCRIPT_DIR" = "$(cd -- "$APP.old" 2>/dev/null && pwd -P || true)" ] || printf '%s' "$SCRIPT_DIR"
}

http_get() { # http_get URL -> stdout
	if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 3 -H 'Accept: application/vnd.github+json' "$1"
	else wget -qO- --header='Accept: application/vnd.github+json' "$1"; fi
}

# latest_release prints the tag of the latest published release, or nothing.
latest_release() {
	[ -n "$REPO" ] || return 0
	http_get "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null |
		grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -n1 | sed 's/.*"\([^"]*\)"$/\1/' || true
}

# resolve_ref picks what to install: VAULTDOCK_REF, else the latest release,
# else main. Release tags (vX.Y.Z) also set RELEASE_VERSION.
resolve_ref() {
	if [ -z "$REF" ]; then
		REF=$(latest_release)
		if [ -z "$REF" ]; then
			REF=main
			[ "$IF_NEWER" = 1 ] || warn "No published release found for $REPO; installing the main branch."
		fi
	fi
	if [[ "$REF" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.]+)?$ ]]; then RELEASE_VERSION=${REF#v}; else RELEASE_VERSION=""; fi
}

installed() { [ -f "$ENV_FILE" ] && [ -f "$APP/docker-compose.yml" ]; }

resolve_ref_if_remote() { [ -n "$(local_source)" ] || resolve_ref; }

# migrate_layout moves the configuration of older installations from
# ~/VaultDock/.env into ~/VaultDock/app/.env, next to docker-compose.yml.
migrate_layout() {
	if [ -f "$LEGACY_ENV" ] && [ ! -f "$ENV_FILE" ] && [ -f "$APP/docker-compose.yml" ]; then
		mv -- "$LEGACY_ENV" "$ENV_FILE"
		env_set_managed VAULTDOCK_ENV_FILE "$ENV_FILE"
		info "Moved the configuration to $ENV_FILE"
	fi
}

random_token() {
	if command -v openssl >/dev/null 2>&1; then openssl rand -hex 24
	else head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'; fi
}

# --- source --------------------------------------------------------------------
download() { # download URL DEST
	if command -v curl >/dev/null 2>&1; then curl -fsSL --retry 3 -o "$2" "$1"; else wget -qO "$2" "$1"; fi
}

# runtime_files DIR prints the files an installation consists of: the replace
# and keep-modified entries of DIR/deploy/managed-files.json, or RUNTIME_FILES
# when DIR has no such list. Unsafe paths (absolute, "..") are dropped.
runtime_files() {
	local list=""
	if [ -f "$1/$MANAGED_FILES_REL" ]; then
		list=$(json_records "$1/$MANAGED_FILES_REL" path policy |
			awk -F"$US" '($2 == "replace" || $2 == "keep-modified") && $1 ~ /^[A-Za-z0-9_][A-Za-z0-9._\/-]*$/ && $1 !~ /\.\./ { print $1 }' || true)
	fi
	# shellcheck disable=SC2086 # word splitting of the fallback list is intended
	if [ -n "$list" ]; then printf '%s\n' "$list"; else printf '%s\n' $RUNTIME_FILES; fi
}

# fetch_app DEST stages a complete app/ directory: the runtime files for the
# selected version plus the user's .env and compose override, if any.
fetch_app() {
	local dest=$1 src f files
	src=$(local_source)
	rm -rf -- "$dest"
	mkdir -p -- "$dest/deploy"
	if [ -n "$src" ]; then
		is_repo "$src" || die "$src does not look like a VaultDock source directory."
		info "Using local source: $src"
		files=$(runtime_files "$src")
		for f in $files; do mkdir -p -- "$(dirname -- "$dest/$f")"; cp -- "$src/$f" "$dest/$f"; done
	else
		[ -n "$REPO" ] || die "No repository configured. Set VAULTDOCK_REPO=owner/name or run it from a cloned repository."
		[ -n "$REF" ] || resolve_ref
		info "Downloading VaultDock ${RELEASE_VERSION:-$REF} from $REPO"
		# The file list ships with the release; older releases fall back to RUNTIME_FILES.
		download "https://raw.githubusercontent.com/$REPO/$REF/$MANAGED_FILES_REL" "$dest/$MANAGED_FILES_REL" 2>/dev/null ||
			rm -f -- "$dest/$MANAGED_FILES_REL"
		files=$(runtime_files "$dest")
		for f in $files; do
			[ ! -s "$dest/$f" ] || continue
			mkdir -p -- "$(dirname -- "$dest/$f")"
			download "https://raw.githubusercontent.com/$REPO/$REF/$f" "$dest/$f" || die "Could not download $f for $REF."
		done
	fi
	chmod 755 -- "$dest/install.sh"
	if [ -f "$dest/$CF_DETECT_REL" ]; then chmod 755 -- "$dest/$CF_DETECT_REL"; fi
	record_shipped "$dest"
	for f in $KEEP_FILES; do
		if [ -f "$APP/$f" ]; then cp -p -- "$APP/$f" "$dest/$f"; fi
	done
	[ -f "$dest/docker-compose.yml" ] && [ -s "$dest/install.sh" ] || die "Downloaded files are incomplete."
}

# with_build_source CMD... runs CMD with a source tree as its last argument.
# A downloaded source lives in a temporary directory that is always removed.
with_build_source() {
	local src tmp rc=0
	src=$(local_source)
	if [ -n "$src" ]; then
		"$@" "$src"
		return
	fi
	[ -n "$REF" ] || resolve_ref
	tmp=$(mktemp -d)
	info "Downloading the source for $REF to build the image"
	if download "https://github.com/$REPO/archive/$REF.tar.gz" "$tmp/src.tar.gz" &&
		mkdir -p "$tmp/src" && tar -xzf "$tmp/src.tar.gz" -C "$tmp/src" --strip-components=1 && is_repo "$tmp/src"; then
		"$@" "$tmp/src" || rc=$?
	else
		rc=1
		warn "Could not download the source for $REF."
	fi
	rm -rf -- "$tmp"
	return "$rc"
}

docker_build() { # docker_build VERSION SRC
	DOCKER_BUILDKIT=1 docker build -t vaultdock:local --build-arg "VERSION=$1" "$2"
}

# build_image: a registry image (VAULTDOCK_IMAGE) is reused or pulled, falling
# back to a source build if it cannot be pulled. Otherwise the image is built
# from source; VAULTDOCK_REBUILD=0 reuses an existing local build.
release_image() { # the prebuilt image published for RELEASE_VERSION, if any
	if [ -n "$RELEASE_VERSION" ] && [ -z "$(local_source)" ]; then
		printf 'ghcr.io/%s:%s' "$(printf '%s' "$REPO" | tr '[:upper:]' '[:lower:]')" "$RELEASE_VERSION"
	else
		printf 'vaultdock:local'
	fi
}

# legacy_release_image prints the image the same release had under the former
# repository name (ghcr.io/aspec-racing-pty-ltd/vaultdock), tried when the
# vad-server image cannot be pulled during the transition (C21).
legacy_release_image() {
	[ -z "${VAULTDOCK_IMAGE:-}" ] && [ -n "$RELEASE_VERSION" ] && [ -z "$(local_source)" ] && [ "$REPO" = "$DEFAULT_REPO" ] || return 0
	printf 'ghcr.io/%s:%s' "$(printf '%s' "$LEGACY_REPO" | tr '[:upper:]' '[:lower:]')" "$RELEASE_VERSION"
}

# build_image only pulls or builds when the target image is not present yet:
# an upgrade to the version already installed reuses the image, and
# "docker compose up -d" then recreates only services whose image or
# configuration changed. Volumes and bind mounts are never touched.
build_image() {
	local img=${VAULTDOCK_IMAGE:-$(release_image)} cand
	[ -z "$RELEASE_VERSION" ] || env_set_managed VAULTDOCK_VERSION "$RELEASE_VERSION"
	if [ "$img" != "vaultdock:local" ]; then
		for cand in "$img" $(legacy_release_image); do
			if docker image inspect "$cand" >/dev/null 2>&1 && [ "${VAULTDOCK_PULL:-0}" != 1 ]; then
				info "Using existing image $cand"
				env_set_managed VAULTDOCK_IMAGE "$cand"
				return
			fi
			info "Pulling $cand"
			if docker pull "$cand"; then
				env_set_managed VAULTDOCK_IMAGE "$cand"
				return
			fi
		done
		warn "Could not pull $img; building from source instead."
	elif [ "${VAULTDOCK_REBUILD:-1}" = 0 ] && docker image inspect "$img" >/dev/null 2>&1; then
		info "Reusing the existing local image"
		env_set_managed VAULTDOCK_IMAGE "$img"
		return
	fi
	info "Building the VaultDock image (first build downloads base images and can take a few minutes)"
	local ver=$RELEASE_VERSION src
	src=$(local_source)
	if [ -z "$ver" ] && [ -n "$src" ]; then ver=$(git -C "$src" describe --tags --always 2>/dev/null || true); fi
	[ -n "$ver" ] || ver="$REF-$(date +%Y%m%d%H%M)"
	with_build_source docker_build "${ver#v}" || return 1
	env_set_managed VAULTDOCK_VERSION "${ver#v}"
	env_set_managed VAULTDOCK_IMAGE "vaultdock:local"
}

# --- configuration (.env) ------------------------------------------------------
# The .env file is merged, never rewritten (DR4/DR5):
#   * existing values are never changed, except the few variables install.sh
#     owns itself (MANAGED_KEYS, "managed": true in deploy/env.schema.json);
#   * missing variables are appended with their schema default (or a generated
#     secret where the schema says "generate");
#   * obsolete variables are only listed; each is removed only after explicit
#     approval: an interactive per-variable prompt, --approve-env-removal NAME,
#     VAULTDOCK_ENV_APPROVE_REMOVE, app/deploy/env-approvals (one name per
#     line) or the admin console (state/deploy/env-removal-approvals.json,
#     api-v1 section 11A). VAULTDOCK_YES=1 never approves a removal.
#
# deploy/env.schema.json and deploy/managed-files.json are read with jq when it
# is installed. Without jq a small awk reader is used instead; it understands
# exactly the subset those files are written in: entries are flat JSON objects
# whose values are strings, numbers, booleans or null (no nested objects or
# arrays inside an entry). Keep both files in that form.

# Variables install.sh may update in place (must match "managed": true).
MANAGED_KEYS="COMPOSE_PROJECT_NAME VAULTDOCK_ENV_FILE VAULTDOCK_IMAGE VAULTDOCK_VERSION VAULTDOCK_AUTO_UPDATE VAULTDOCK_INTEGRATIONS VAULTDOCK_TUNNEL_NETWORK"
ENV_SCHEMA_REL="deploy/env.schema.json"
MANAGED_FILES_REL="deploy/managed-files.json"
ENV_APPROVALS_REL="deploy/env-approvals"
ENV_APPROVE_REMOVE="${VAULTDOCK_ENV_APPROVE_REMOVE:-}" # also --approve-env-removal
US=$'\037' # field separator of json_records output
ENV_BACKED_UP=""

# json_records FILE FIELD... prints one line per flat object in FILE that has
# the first FIELD, with the requested fields joined by \037 (missing or null
# fields are empty; booleans print as true/false).
json_records() {
	local file=$1 fields
	shift
	fields=$(IFS=,; printf '%s' "$*")
	[ -f "$file" ] || return 1
	if command -v jq >/dev/null 2>&1; then
		jq -r --arg f "$fields" '($f | split(",")) as $k
			| .. | objects
			| select(all(.[]; type != "object" and type != "array"))
			| select(has($k[0]))
			| [ $k[] as $x | (if has($x) and .[$x] != null then .[$x] | tostring else "" end) ]
			| join("\u001f")' "$file"
		return
	fi
	awk -v fields="$fields" '
	function emit(   n, line) {
		if (!(F[1] in val)) return
		line = ""
		for (n = 1; n <= nf; n++) line = line (n > 1 ? US : "") ((F[n] in val) ? val[F[n]] : "")
		print line
	}
	BEGIN { RS = "\001"; nf = split(fields, F, ","); US = sprintf("%c", 31) }
	{
		s = $0; L = length(s); i = 1; inobj = 0; expect = ""
		while (i <= L) {
			c = substr(s, i, 1)
			if (c == "\"") {
				str = ""; j = i + 1
				while (j <= L) {
					d = substr(s, j, 1)
					if (d == "\\") {
						e = substr(s, j + 1, 1)
						if (e == "n") str = str "\n"; else if (e == "t") str = str "\t"; else if (e == "u") { str = str "?"; j += 4 } else str = str e
						j += 2; continue
					}
					if (d == "\"") break
					str = str d; j++
				}
				i = j + 1
				if (inobj && expect == "key") { key = str; expect = ":" }
				else if (inobj && expect == "val") { val[key] = str; expect = "," }
				continue
			}
			if (c == "{") { inobj = 1; split("", val); expect = "key" }
			else if (c == "}") { if (inobj) emit(); inobj = 0 }
			else if (c == "[") { inobj = 0 }
			else if (c == ":" && inobj) { expect = "val" }
			else if (c == "," && inobj) { expect = "key" }
			else if (inobj && expect == "val" && c ~ /[-0-9tfn]/) {
				j = i
				while (j <= L && substr(s, j, 1) !~ /[,}\] \t\r\n]/) j++
				lit = substr(s, i, j - i)
				val[key] = (lit == "null") ? "" : lit
				expect = ","; i = j; continue
			}
			i++
		}
	}' "$file"
}

env_has() { [ -f "$ENV_FILE" ] && grep -qE "^$1=" "$ENV_FILE"; }

# env_backup copies .env into the backups once per run, before the first change.
env_backup() {
	{ [ -f "$ENV_FILE" ] && [ -z "$ENV_BACKED_UP" ]; } || return 0
	local dest; dest="$BACKUPS/env-$(date +%Y%m%d-%H%M%S)"
	mkdir -p -- "$dest"
	cp -p -- "$ENV_FILE" "$dest/.env"
	ENV_BACKED_UP=$dest
}

# env_write_file TMP replaces the content of .env (same inode and mode).
env_write_file() {
	cat "$1" >"$ENV_FILE"
	rm -f -- "$1"
	chmod 600 "$ENV_FILE"
}

# env_add KEY VALUE appends KEY when it is missing. Existing values (including
# empty ones) are never changed.
env_add() {
	env_has "$1" && return 0
	env_backup
	if [ -s "$ENV_FILE" ] && [ -n "$(tail -c1 "$ENV_FILE")" ]; then printf '\n' >>"$ENV_FILE"; fi
	printf '%s=%s\n' "$1" "$2" >>"$ENV_FILE"
	chmod 600 "$ENV_FILE"
}

# env_set_managed KEY VALUE updates or appends a variable install.sh owns
# (MANAGED_KEYS). Any other variable is refused: user values are never changed.
env_set_managed() {
	local key=$1 val=$2 tmp
	case " $MANAGED_KEYS " in *" $key "*) ;; *) die "Internal error: $key is not managed by install.sh and is never changed." ;; esac
	if ! env_has "$key"; then env_add "$key" "$val"; return; fi
	[ "$(env_get "$key")" != "$val" ] || return 0
	env_backup
	tmp=$(mktemp)
	awk -v k="$key" -v v="$val" 'index($0, k "=") == 1 { print k "=" v; next } { print }' "$ENV_FILE" >"$tmp"
	env_write_file "$tmp"
}

# env_remove KEY deletes every KEY= line. Only called after explicit approval.
env_remove() {
	local tmp
	env_backup
	tmp=$(mktemp)
	awk -v k="$1" 'index($0, k "=") != 1' "$ENV_FILE" >"$tmp"
	env_write_file "$tmp"
}

env_schema() { printf '%s' "$APP/$ENV_SCHEMA_REL"; }

# state_path prints the host state directory as an absolute path.
state_path() {
	local p; p=$(env_get VAULTDOCK_STATE_PATH); p=${p:-./data/state}
	case "$p" in /*) printf '%s' "$p" ;; *) printf '%s/%s' "$APP" "${p#./}" ;; esac
}

# --- server key-encryption key (KEK) ------------------------------------------------
# The server seals its private keys (server_keys), catalog keys and stored
# secrets with a 32-byte KEK kept outside the database (backup-format-v1
# section 4.6). install.sh creates it once, in the state directory, and never
# prints, copies off-host or regenerates it. Losing both the KEK and every
# recovery key means the encrypted data cannot be recovered.
KEK_DEFAULT=/state/keys/kek
KEK_MARK_REL=.kek-backup-confirmed # in $DIR: the admin confirmed an off-server copy
KEK_CREATED=0

# kek_host_path prints the host path of VAULTDOCK_KEK_FILE (a path inside the
# container), or nothing when it is not under /state (for example a Docker
# secret the administrator provides).
kek_host_path() {
	local p; p=$(env_get VAULTDOCK_KEK_FILE); p=${p:-$KEK_DEFAULT}
	case "$p" in
	*/../* | */..) return 0 ;;
	/state/?*) printf '%s/%s' "$(state_path)" "${p#/state/}" ;;
	esac
}

# db_has_wrapped_keys: true when the database holds (or may hold) keys sealed
# by the KEK. With sqlite3 the key tables are counted; without it, any trace of
# those tables counts, which errs on the side of refusing.
db_has_wrapped_keys() {
	local db t n tables
	db="$(state_path)/vaultdock.db"
	[ -f "$db" ] || return 1
	if command -v sqlite3 >/dev/null 2>&1 &&
		tables=$(sqlite3 -readonly "$db" "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('server_keys','catalog_keys','secrets');" 2>/dev/null); then
		for t in $tables; do
			n=$(sqlite3 -readonly "$db" "SELECT count(*) FROM $t;" 2>/dev/null) || return 0
			[ "${n:-0}" = 0 ] || return 0
		done
		return 1
	fi
	grep -aqs -e private_sealed_by_kek -e dek_wrapped_by_kek -e 'CREATE TABLE secrets' -- "$db" "$db-wal"
}

# kek_generate FILE writes 32 random bytes to FILE (mode 0600). Nothing is printed.
kek_generate() {
	local f=$1 tmp="$1.tmp.$$"
	(
		umask 077
		if command -v openssl >/dev/null 2>&1; then openssl rand -out "$tmp" 32
		else head -c 32 /dev/urandom >"$tmp"; fi
	) || { rm -f -- "$tmp"; return 1; }
	[ "$(wc -c <"$tmp" | tr -d ' ')" = 32 ] || { rm -f -- "$tmp"; return 1; }
	chmod 600 -- "$tmp"
	mv -f -- "$tmp" "$f"
}

# kek_ensure creates the KEK when it does not exist. It refuses (and changes
# nothing) when the database already holds keys sealed by a KEK that is now
# missing: a new key could not open them.
kek_ensure() {
	local f dir uid gid
	f=$(kek_host_path)
	if [ -z "$f" ]; then
		info "VAULTDOCK_KEK_FILE ($(env_get VAULTDOCK_KEK_FILE)) is not under /state; install.sh does not manage it. Make sure that key file exists."
		return 0
	fi
	[ ! -s "$f" ] || return 0
	uid=$(env_get VAULTDOCK_UID); gid=$(env_get VAULTDOCK_GID)
	if db_has_wrapped_keys; then
		die "The server key file is missing: $f
  The database already holds keys sealed with it, and a new key cannot open
  them, so install.sh will not create one. Nothing was changed.
  Restore the key from your off-server backup to that path (mode 0600, owner
  ${uid:-65532}:${gid:-65532}) and run this again.
  If no copy exists, the server's escrow keys, catalog keys and stored
  credentials are lost: backups can then only be opened with a device key or
  a recovery key (VDRK1-...). Ask for help before going further.$(command -v sqlite3 >/dev/null 2>&1 || printf '\n  (sqlite3 is not installed, so the key tables could not be counted; install it\n  to let install.sh check whether they are actually empty.)')"
	fi
	dir=$(dirname -- "$f")
	mkdir -p -- "$dir"
	chmod 700 -- "$dir"
	kek_generate "$f" || die "Could not generate the server key file $f."
	if [ "$(id -u)" = 0 ] && [ -n "$uid" ]; then chown "$uid:${gid:-$uid}" -- "$dir" "$f"; fi
	rm -f -- "$DIR/$KEK_MARK_REL"
	KEK_CREATED=1
	ok "Created the server key file $f (32 random bytes, mode 0600; it is never shown)."
}

# link_source_env makes "docker compose" work in the source checkout the
# installer ran from (e.g. ~/vad-server): its .env becomes a link to the real
# configuration ($ENV_FILE), and COMPOSE_PROJECT_NAME pins the project, so
# compose commands there control the running installation, not a second one.
link_source_env() {
	[ -f "$ENV_FILE" ] || return 0
	env_set_managed COMPOSE_PROJECT_NAME "$PROJECT"
	local src; src=$(local_source)
	[ -n "$src" ] || return 0
	local src_real app_real
	src_real=$(cd -- "$src" && pwd -P); app_real=$(cd -- "$APP" && pwd -P)
	[ "$src_real" != "$app_real" ] || return 0
	if [ -e "$src/.env" ] && [ ! -L "$src/.env" ]; then
		warn "$src/.env is a separate file; docker compose in $src uses it, not $ENV_FILE. Remove it to link the installed configuration."
		return 0
	fi
	ln -sfn -- "$ENV_FILE" "$src/.env"
	info "Linked $src/.env to $ENV_FILE; docker compose works in either folder."
}

# kek_backup_prompt asks the administrator to copy the KEK off the server,
# until they confirm once ($DIR/.kek-backup-confirmed).
kek_backup_prompt() {
	local f; f=$(kek_host_path)
	{ [ -n "$f" ] && [ -s "$f" ] && [ ! -f "$DIR/$KEK_MARK_REL" ]; } || return 0
	cat <<EOF

${B}Back up the server key now${N}
  $f
  This file (the server key-encryption key) protects the server's private keys,
  catalog keys and stored credentials. It is not inside the database backups.
  * Download it once from the web portal: sign in as a superuser, open
    Devices > Server keys and choose "Download server key". Keep it in a safe
    place OUTSIDE this server (password manager, encrypted USB drive). The
    portal allows this download only once.
  * Keep it secret: with this file and a copy of the database, anyone can open
    the server-side escrow.
  * After rebuilding the server, put it back at the same path before starting
    VaultDock.
  * If you lose BOTH this key and every recovery key (VDRK1-...) of your users,
    encrypted backups cannot be recovered.
  The key is never printed and install.sh never copies it off this server.
EOF
	if [ "$YES" != 1 ] && have_tty; then
		if confirm "Have you downloaded the server key from the portal (or will you right after setup)?" n; then
			date -u +%Y-%m-%dT%H:%M:%SZ >"$DIR/$KEK_MARK_REL"
			ok "Thank you. Keep that copy safe."
		else
			warn "Not confirmed. You will be reminded on the next upgrade (or run: $APP/install.sh kek-status)."
		fi
	fi
}

do_kek_status() {
	installed || die "No VaultDock installation found in $DIR."
	local f; f=$(kek_host_path)
	if [ -z "$f" ]; then info "VAULTDOCK_KEK_FILE=$(env_get VAULTDOCK_KEK_FILE) is managed outside install.sh."; return 0; fi
	if [ -s "$f" ]; then
		ok "Server key file: $f ($(wc -c <"$f" | tr -d ' ') bytes, mode $(stat -c %a -- "$f" 2>/dev/null || echo ?))"
	else
		warn "Server key file missing: $f"
	fi
	if [ -f "$DIR/$KEK_MARK_REL" ]; then
		printf '  Off-server copy confirmed on %s
' "$(cat -- "$DIR/$KEK_MARK_REL")"
	else
		kek_backup_prompt
	fi
}

# env_merge_missing appends every schema variable missing from .env under one
# dated comment block. Prints nothing when .env is complete.
env_merge_missing() {
	local schema name def generate env_file obsolete since added=0
	schema=$(env_schema)
	[ -f "$schema" ] || { warn "No $ENV_SCHEMA_REL found; skipping the .env merge."; return 0; }
	while IFS="$US" read -r name def generate env_file obsolete since; do
		{ [ -n "$name" ] && [ "$env_file" = true ] && [ "$obsolete" != true ]; } || continue
		! env_has "$name" || continue
		[ "$generate" != hex24 ] || def=$(random_token)
		if [ "$added" = 0 ]; then
			env_backup
			if [ -s "$ENV_FILE" ] && [ -n "$(tail -c1 "$ENV_FILE")" ]; then printf '\n' >>"$ENV_FILE"; fi
			printf '\n# --- Added by install.sh on %s from %s (defaults; edit as needed)\n' \
				"$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$ENV_SCHEMA_REL" >>"$ENV_FILE"
		fi
		printf '%s=%s\n' "$name" "$def" >>"$ENV_FILE"
		info "Added $name (new in ${since:-?})"
		added=$((added + 1))
	done < <(json_records "$schema" name default generate env_file obsolete since)
	chmod 600 "$ENV_FILE"
	[ "$added" = 0 ] || ok "Added $added new setting(s) to $ENV_FILE; existing values were not changed."
}

# env_approved_names prints the names approved for removal outside the
# interactive prompt: flag/variable, app/deploy/env-approvals, admin console.
env_admin_approvals_file() { printf '%s/deploy/env-removal-approvals.json' "$(state_path)"; }
env_approved_names() {
	local f
	printf '%s\n' "$ENV_APPROVE_REMOVE" | tr ', ' '\n\n'
	f="$APP/$ENV_APPROVALS_REL"
	if [ -f "$f" ]; then sed -e 's/#.*//' -e 's/[[:space:]]//g' "$f"; fi
	f=$(env_admin_approvals_file)
	if [ -f "$f" ]; then json_records "$f" name || true; fi
}

# env_review_obsolete lists obsolete variables present in .env and removes
# each one only with explicit approval. Without approval they are kept.
env_review_obsolete() {
	local schema name dep repl obsolete approved found=0 kept=0 admin
	schema=$(env_schema)
	[ -f "$schema" ] || return 0
	approved=" $(env_approved_names | grep -E '^[A-Za-z_][A-Za-z0-9_]*$' | tr '\n' ' ' || true) "
	while IFS="$US" read -r name dep repl obsolete; do
		{ [ "$obsolete" = true ] && [ -n "$name" ] && env_has "$name"; } || continue
		[ "$found" != 0 ] || info "Obsolete settings in $ENV_FILE (kept unless you approve removing each one):"
		found=$((found + 1))
		printf '  %s (obsolete since %s%s)\n' "$name" "${dep:-?}" "${repl:+, replaced by $repl}"
		if [[ "$approved" == *" $name "* ]]; then
			env_remove "$name"; ok "Removed $name (approved)"
		elif [ "$YES" != 1 ] && have_tty && confirm "    Remove $name from .env?" n; then
			env_remove "$name"; ok "Removed $name"
		else
			kept=$((kept + 1))
		fi
	done < <(json_records "$schema" name deprecated_since replacement obsolete)
	if [ "$kept" -gt 0 ]; then
		printf '  Kept %s obsolete setting(s). Remove them later with "%s env-review", --approve-env-removal NAME, %s or the admin console.\n' \
			"$kept" "$APP/install.sh" "$APP/$ENV_APPROVALS_REL"
	fi
	# The admin console's approvals are applied once, then cleared (api-v1 11A).
	admin=$(env_admin_approvals_file)
	if [ -f "$admin" ]; then rm -f -- "$admin" 2>/dev/null || warn "Could not clear $admin; remove it yourself."; fi
	if [ -n "$ENV_BACKED_UP" ]; then info "Previous .env saved in $ENV_BACKED_UP"; fi
}

# env_merge runs the add-only merge and the obsolete review.
env_merge() {
	[ -f "$ENV_FILE" ] || return 0
	env_merge_missing
	env_review_obsolete
}

do_env_review() {
	installed || die "No VaultDock installation found in $DIR."
	env_merge
	ok "Environment review complete. Apply changes with: $APP/install.sh upgrade (or restart VaultDock)."
}

# --- managed files and integrations --------------------------------------------
file_sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum -- "$1" | cut -d' ' -f1
	elif command -v shasum >/dev/null 2>&1; then shasum -a 256 -- "$1" | cut -d' ' -f1
	else return 1; fi
}

# record_shipped DEST writes the checksums of the shipped runtime files, so the
# next upgrade can tell whether the user modified them.
record_shipped() {
	local dest=$1 f h files
	files=$(runtime_files "$dest")
	: >"$dest/.managed-files.sha256"
	for f in $files; do
		h=$(file_sha256 "$dest/$f" || true)
		[ -z "$h" ] || printf '%s  %s\n' "$h" "$f" >>"$dest/.managed-files.sha256"
	done
}

# apply_managed_files NEW compares every managed file of the current $APP (or
# its legacy_path) with the version it was installed as (app/.managed-files.sha256
# or known_sha256). A locally modified file is handled by its policy:
#   keep-modified  the user's copy is kept; the shipped version goes to <path>.new
#   replace        installer internals must match the release: the shipped
#                  version is installed and the user's copy is saved as <path>.local
# Unmodified files are simply replaced. The upgrade backup has every old file.
MODIFIED_FILES=""
apply_managed_files() {
	local new=$1 path policy legacy known src sum rec
	MODIFIED_FILES=""
	[ -f "$new/$MANAGED_FILES_REL" ] || return 0
	while IFS="$US" read -r path policy legacy known; do
		{ { [ "$policy" = keep-modified ] || [ "$policy" = replace ]; } && [ -f "$new/$path" ]; } || continue
		src=""
		if [ -f "$APP/$path" ]; then src="$APP/$path"
		elif [ -n "$legacy" ] && [ -f "$APP/$legacy" ]; then src="$APP/$legacy"; fi
		[ -n "$src" ] || continue
		! cmp -s -- "$src" "$new/$path" || continue
		sum=$(file_sha256 "$src" || true)
		[ -n "$sum" ] || continue
		rec=""
		if [ -f "$APP/.managed-files.sha256" ]; then rec=$(awk -v p="$path" '$2 == p { print $1 }' "$APP/.managed-files.sha256"); fi
		if [ "$sum" = "$rec" ] || [[ " $known " == *" $sum "* ]]; then continue; fi
		if [ "$policy" = replace ]; then
			# Without a recorded checksum there is nothing to compare against.
			[ -n "$rec" ] || continue
			cp -p -- "$src" "$new/$path.local"
			MODIFIED_FILES="$MODIFIED_FILES $path"
			warn "$path had local changes. It was replaced by the new version (required); your copy is in $APP/$path.local."
			continue
		fi
		cp -p -- "$new/$path" "$new/$path.new"
		cp -p -- "$src" "$new/$path"
		MODIFIED_FILES="$MODIFIED_FILES $path"
		warn "$path has local changes and was kept. The new version is in $APP/$path.new; merge it when convenient."
	done < <(json_records "$new/$MANAGED_FILES_REL" path policy legacy_path known_sha256)
}

# integrations prints the enabled opt-in integrations (VAULTDOCK_INTEGRATIONS).
integrations() { env_get VAULTDOCK_INTEGRATIONS | tr ',' '\n' | tr -d ' ' | grep -E '^[a-z0-9-]+$' || true; }
integration_enabled() { integrations | grep -qx "$1"; }
# integration_file NAME: deploy/integrations/<family>/compose.<NAME>.yml, where
# the family is NAME up to the first "-" (cloudflared-net -> cloudflared/).
integration_file() { printf '%s/deploy/integrations/%s/compose.%s.yml' "$APP" "${1%%-*}" "$1"; }

# legacy_caddy_profile: the install still relies on the Caddy service that
# used to be bundled in docker-compose.yml (COMPOSE_PROFILES=caddy).
legacy_caddy_profile() {
	local p; p=$(env_get COMPOSE_PROFILES | tr -d ' ')
	[[ ",$p," == *",caddy,"* ]]
}

# migrate_caddy_profile moves an install that used the bundled Caddy profile
# to the opt-in caddy integration (B16/C20). The service name and volumes are
# the same, so certificates and Caddy state are kept; a modified Caddyfile is
# carried over by apply_managed_files (legacy_path). COMPOSE_PROFILES is left
# in place (obsolete; removed only with approval). Rollback: the upgrade
# backup plus the $APP.old swap restore the previous files and .env.
migrate_caddy_profile() {
	legacy_caddy_profile || return 0
	! integration_enabled caddy || return 0
	[ -f "$(integration_file caddy)" ] || { warn "The caddy integration files are missing; Caddy was not migrated."; return 1; }
	local cur; cur=$(env_get VAULTDOCK_INTEGRATIONS)
	env_set_managed VAULTDOCK_INTEGRATIONS "${cur:+$cur,}caddy"
	ok "Moved the bundled Caddy proxy to the opt-in integration deploy/integrations/caddy (same volumes and certificates)."
}

prepare_dirs() {
	local state storage uid gid
	state=$(env_get VAULTDOCK_STATE_PATH); storage=$(env_get VAULTDOCK_STORAGE_PATH)
	uid=$(env_get VAULTDOCK_UID); gid=$(env_get VAULTDOCK_GID)
	mkdir -p -- "$state" "$storage" "$BACKUPS"
	if [ "$(id -u)" = 0 ]; then
		chown "$uid:$gid" -- "$state" "$storage"
	fi
	chmod 750 -- "$state" "$storage"
}

wait_healthy() {
	local port bind url i
	port=$(env_get VAULTDOCK_PORT); port=${port:-$DEFAULT_PORT}
	url="http://127.0.0.1:$port/readyz"
	if [ "$(env_get VAULTDOCK_TLS_SELF_SIGNED)" = true ]; then url="https://127.0.0.1:$port/readyz"; fi
	info "Waiting for VaultDock to become ready"
	for i in $(seq 1 60); do
		if [ "$(docker inspect -f '{{.State.Health.Status}}' vaultdock 2>/dev/null || true)" = healthy ]; then ok "VaultDock is running"; return 0; fi
		if [ "$(docker inspect -f '{{.State.Running}}' vaultdock 2>/dev/null || true)" != true ] && [ "$i" -gt 5 ]; then break; fi
		sleep 2
	done
	warn "VaultDock did not become healthy. Recent logs:"
	compose logs --tail 40 vaultdock >&2 || true
	return 1
}

# --- integrations: Cloudflare Tunnel (DR1, DR3, DR6, C24) ----------------------
# install.sh never adds a cloudflared container and never restarts a
# cloudflared without asking; a cloudflared it cannot restart safely (a plain
# process) only gets printed instructions. Detection is read-only
# (deploy/integrations/cloudflared/detect.sh).
CF_DETECT_REL=deploy/integrations/cloudflared/detect.sh
declare -gA CF=()

# integration_change WHAT CMD...: runs CMD (which edits .env or app/ files)
# with a snapshot of app/ (including .env), recreates VaultDock and restores
# the snapshot when it does not come back healthy.
integration_change() {
	local what=$1 bk; shift
	bk="$BACKUPS/integration-$(date +%Y%m%d-%H%M%S)"
	mkdir -p -- "$bk"
	(cd -- "$DIR" && tar -cf "$bk/app.tar" app)
	if "$@" && compose up -d && wait_healthy; then
		ok "$what"
		info "The previous configuration is saved in $bk/app.tar"
		return 0
	fi
	warn "$what failed; restoring the previous configuration from $bk."
	compose down || true
	rm -rf -- "$APP.failed"; mv -- "$APP" "$APP.failed"
	(cd -- "$DIR" && tar -xf "$bk/app.tar")
	rm -rf -- "$APP.failed"
	compose up -d --remove-orphans || true
	wait_healthy || true
	return 1
}

# trusted_proxies_add CIDRS: VAULTDOCK_TRUSTED_PROXIES is set to the loopback
# addresses plus CIDRS only when the variable is missing (add-only .env);
# an existing value is never changed, only reported.
trusted_proxies_add() {
	local want=$1 cur c missing=""
	[ -n "$want" ] || return 0
	if ! env_has VAULTDOCK_TRUSTED_PROXIES; then
		env_add VAULTDOCK_TRUSTED_PROXIES "127.0.0.1/32,::1/128,$want"
		ok "Set VAULTDOCK_TRUSTED_PROXIES to trust the tunnel network ($want)."
		return 0
	fi
	cur=$(env_get VAULTDOCK_TRUSTED_PROXIES | tr -d ' ')
	for c in ${want//,/ }; do
		[[ ",$cur," == *",$c,"* ]] || missing="${missing:+$missing,}$c"
	done
	[ -z "$missing" ] || warn "VAULTDOCK_TRUSTED_PROXIES is already set (${cur:-empty}) and was not changed. Add $missing to it in $ENV_FILE so client addresses forwarded by the tunnel are trusted."
}

# cf_detect fills CF[] from detect.sh (key=value lines). It runs as its own
# POSIX sh process, so nothing leaks into this script.
cf_detect() {
	local lib="$APP/$CF_DETECT_REL" k v port
	if [ ! -f "$lib" ] && [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/$CF_DETECT_REL" ]; then lib="$SCRIPT_DIR/$CF_DETECT_REL"; fi
	[ -f "$lib" ] || die "$CF_DETECT_REL is missing. Run \"$APP/install.sh upgrade\" first."
	port=$(env_get VAULTDOCK_PORT)
	CF=()
	while IFS='=' read -r k v; do
		[ -n "$k" ] && CF[$k]=$v
	done < <(sh "$lib" "${port:-$DEFAULT_PORT}" "${PROJECT}_default" 2>/dev/null || true)
	[ -n "${CF[schema]:-}" ] || die "cloudflared detection failed."
}

cf() { printf '%s' "${CF[instance.$1.$2]:-}"; } # cf N KEY

# cf_pick prints the instance to configure: the only one, or the user's
# choice (default: an active, locally managed one).
cf_pick() {
	local n=${CF[count]:-0} i def=1 choice
	for ((i = n; i >= 1; i--)); do
		case "$(cf "$i" active)" in active | running) [ "$(cf "$i" mode)" = local ] && def=$i ;; esac
	done
	[ "$n" -gt 1 ] || { printf '1'; return; }
	ask choice "Which cloudflared should serve VaultDock? (1-$n)" "$def"
	[[ "$choice" =~ ^[0-9]+$ ]] && [ "$choice" -ge 1 ] && [ "$choice" -le "$n" ] || die "Choose a number between 1 and $n."
	printf '%s' "$choice"
}

# cf_action N: edit-ingress | dashboard | manual. A dashboard-managed tunnel
# overrides any local ingress, so it is never edited locally.
cf_action() {
	local a; a=$(cf "$1" action)
	if [ "$a" = edit-ingress ]; then
		if [ "$(cf "$1" remote_config_seen)" = yes ]; then a=dashboard
		elif [ "$(cf "$1" kind)" = process ] || [ "$(cf "$1" config_readable)" != yes ]; then a=manual; fi
	fi
	printf '%s' "${a:-manual}"
}

cf_hostname_default() { env_get VAULTDOCK_PUBLIC_URL | sed -e 's#^[a-z]*://##' -e 's#[:/].*##'; }

cf_print_none() {
	cat <<EOF
No cloudflared was found on this server${CF[root]:+ (searched as root=${CF[root]})}.
VaultDock does not bundle a tunnel. To publish it through Cloudflare:
  1. Install cloudflared outside VaultDock, either as a system package
     (https://pkg.cloudflare.com) or as its own Docker Compose project.
  2. Create a tunnel (Cloudflare dashboard: Zero Trust -> Networks -> Tunnels)
     and add a public hostname whose service is http://127.0.0.1:$(env_get VAULTDOCK_PORT)
     (cloudflared on the host) or http://vaultdock:8080 (cloudflared in Docker).
  3. Run "$APP/install.sh integrations cloudflared" again to check the setup.
EOF
	[ "${CF[root]:-}" = yes ] || printf 'Tip: run it with sudo; without root some services and containers are not visible.\n'
}

cf_print_dashboard() { # cf_print_dashboard N HOSTNAME SERVICE
	local where; where="$(cf "$1" kind) $(cf "$1" id)"
	cat <<EOF
Your Cloudflare Tunnel ($where) is managed in the Cloudflare dashboard, so
install.sh does not edit it. Add a public hostname for VaultDock there:
  1. Cloudflare dashboard -> Zero Trust -> Networks -> Tunnels -> your tunnel -> Configure
  2. Public Hostname -> Add a public hostname
       Hostname:      $2
       Service type:  HTTP
       URL:           ${3#http://}
  3. Save. Cloudflare creates the DNS record.
Leave "No TLS Verify" off: VaultDock speaks plain HTTP behind the tunnel.
If a Cloudflare Access policy protects the hostname, let /api/* through (or
use a service token): the desktop app talks to the API directly.
EOF
}

cf_print_manual() { # cf_print_manual N HOSTNAME SERVICE
	cat <<EOF
install.sh will not change this cloudflared ($(cf "$1" kind) $(cf "$1" id), mode $(cf "$1" mode)).
Add this rule to its ingress list, before the final catch-all rule, then
restart it yourself:
  - hostname: $2
    service: $3
EOF
	if [ "$(cf "$1" mode)" = quick ]; then
		printf 'A quick tunnel (--url, TryCloudflare) cannot serve a fixed hostname; create a named tunnel instead.\n'
	fi
}

# cf_probe HOSTNAME: VaultDock answers through the tunnel (retries ~30 s).
cf_probe() {
	local i
	command -v curl >/dev/null 2>&1 || { warn "curl is not installed; check https://$1/readyz yourself."; return 0; }
	for i in 1 2 3 4 5 6; do
		if curl -fsS -o /dev/null --max-time 5 "https://$1/readyz" 2>/dev/null; then ok "VaultDock answers on https://$1"; return 0; fi
		sleep 5
	done
	return 1
}

# cf_ingress_candidate CONFIG HOSTNAME SERVICE OUT writes CONFIG plus a rule for
# HOSTNAME, inserted right before the catch-all rule. It refuses (returns 1)
# unless the file has exactly one top-level block-style "ingress:" list whose
# last rule is a catch-all, and no YAML anchors, aliases or merge keys.
cf_ingress_candidate() {
	local cfg=$1 host=$2 svc=$3 out=$4
	grep -Eq '^[^#]*([&*][A-Za-z0-9_-]|<<:)' -- "$cfg" && return 1
	[ "$(grep -Ec '^ingress:[[:space:]]*(#.*)?$' -- "$cfg")" = 1 ] || return 1
	awk -v host="$host" -v svc="$svc" '
		{ line[NR] = $0 }
		/^ingress:[[:space:]]*(#.*)?$/ { start = NR; next }
		start && !stop && /^[^[:space:]#-]/ { stop = NR }
		start && !stop && /^[[:space:]]*-[[:space:]]/ { last = NR; bad = 0; ind = $0; sub(/-.*/, "", ind) }
		start && !stop && last && /^[[:space:]]*(-[[:space:]]+)?(hostname|path):/ { bad = 1 }
		END {
			if (!start || !last || bad) exit 1
			if (line[last] !~ /^[[:space:]]*-[[:space:]]+service:[[:space:]]*[^[:space:]]/) exit 1
			for (i = 1; i <= NR; i++) {
				if (i == last) { print ind "- hostname: " host; print ind "  service: " svc }
				print line[i]
			}
		}' "$cfg" >"$out"
}

# cf_validate N FILE: cloudflared accepts FILE and routes HOSTNAME to SERVICE.
# Uses the host binary, or the cloudflared container for Docker instances.
cf_validate() { # cf_validate N FILE HOSTNAME SERVICE
	local run=() file=$2
	if [ "$(cf "$1" kind)" = docker ]; then
		command -v docker >/dev/null 2>&1 || return 2
		run=(docker exec "$(cf "$1" id)" cloudflared)
		file="$(dirname -- "$(cf "$1" config_in_container)")/$(basename -- "$2")"
	elif command -v cloudflared >/dev/null 2>&1; then
		run=(cloudflared)
	else
		return 2
	fi
	"${run[@]}" tunnel --config "$file" ingress validate >/dev/null 2>&1 || return 1
	"${run[@]}" tunnel --config "$file" ingress rule "https://$3" 2>/dev/null | grep -qF -- "$4" || return 1
}

cf_write_config() { # cf_write_config N SRC DEST: same owner/mode; in place for Docker file mounts
	if [ "$(cf "$1" kind)" = docker ]; then cat -- "$2" >"$3"; return; fi
	cat -- "$2" >"$3.vaultdock-tmp"
	chown --reference="$3" -- "$3.vaultdock-tmp" 2>/dev/null || true
	chmod --reference="$3" -- "$3.vaultdock-tmp" 2>/dev/null || true
	mv -f -- "$3.vaultdock-tmp" "$3"
}

cf_restart() { # cf_restart N
	case "$(cf "$1" kind)" in
	systemd | snap) systemctl restart "$(cf "$1" id)" ;;
	docker) docker restart "$(cf "$1" id)" >/dev/null ;;
	*) return 1 ;;
	esac
}

cf_running() { # cf_running N (after a short grace period)
	local i st
	for i in 1 2 3 4; do
		sleep 5
		case "$(cf "$1" kind)" in
		systemd | snap) st=$(systemctl is-active "$(cf "$1" id)" 2>/dev/null || true); [ "$st" = active ] && return 0 ;;
		docker) st=$(docker inspect -f '{{.State.Running}}' "$(cf "$1" id)" 2>/dev/null || true); [ "$st" = true ] && return 0 ;;
		esac
	done
	return 1
}

# cf_edit_ingress N HOSTNAME SERVICE: backup, candidate, validation, atomic
# install, restart only after confirmation, health check, rollback.
cf_edit_ingress() {
	local n=$1 host=$2 svc=$3 cfg ts bk cand rc=0
	cfg=$(cf "$n" config)
	if [ "$(cf "$n" has_vaultdock_rule)" = yes ] && grep -qF -- "$host" "$cfg"; then
		ok "$cfg already routes $host to VaultDock; nothing to change."
		return 0
	fi
	ts=$(date +%Y%m%d-%H%M%S)
	cand="$(dirname -- "$cfg")/.vaultdock-candidate-$ts.yml"
	if ! cf_ingress_candidate "$cfg" "$host" "$svc" "$cand"; then
		rm -f -- "$cand"
		warn "$cfg is not a simple ingress list ending in a catch-all rule, so install.sh does not edit it."
		cf_print_manual "$n" "$host" "$svc"
		return 1
	fi
	cf_validate "$n" "$cand" "$host" "$svc" || rc=$?
	if [ "$rc" != 0 ]; then
		rm -f -- "$cand"
		if [ "$rc" = 2 ]; then warn "cloudflared is not available to validate the change, so $cfg was not edited."
		else warn "cloudflared rejected the new ingress rule (or another rule matches $host first); $cfg was not changed."; fi
		cf_print_manual "$n" "$host" "$svc"
		return 1
	fi
	bk="$BACKUPS/cloudflared-$ts"
	mkdir -p -- "$bk"
	cp -p -- "$cfg" "$bk/$(basename -- "$cfg")"
	cp -p -- "$cfg" "$cfg.vaultdock-$ts"
	cf_write_config "$n" "$cand" "$cfg"
	rm -f -- "$cand"
	ok "Added $host -> $svc to $cfg (backup: $bk and $cfg.vaultdock-$ts)."
	if ! confirm "Restart $(cf "$n" kind) $(cf "$n" id) now to apply it? Every hostname on this tunnel drops for a few seconds" n; then
		info "Not restarted. The rule takes effect at the next restart of cloudflared ($(cf "$n" id))."
		return 0
	fi
	if cf_restart "$n" && cf_running "$n"; then
		if cf_probe "$host"; then return 0; fi
		warn "cloudflared runs, but https://$host/readyz does not answer yet (often DNS: the hostname needs a route)."
		[ "$(cf "$n" tunnel_named)" != yes ] || printf '  Create it with: cloudflared tunnel route dns <tunnel name or id> %s\n  (or a CNAME %s -> <tunnel id>.cfargotunnel.com in the Cloudflare DNS settings)\n' "$host" "$host"
		confirm "Keep the new rule?" y && return 0
	else
		warn "cloudflared did not come back after the restart."
	fi
	warn "Restoring the previous cloudflared configuration."
	cf_write_config "$n" "$bk/$(basename -- "$cfg")" "$cfg"
	if cf_restart "$n" && cf_running "$n"; then ok "cloudflared runs again with its previous configuration."
	else warn "cloudflared is still not running. Its previous configuration is back in $cfg; check it with systemctl status or docker logs."; fi
	return 1
}

# cf_join_network N: for cloudflared in Docker on a user-defined network,
# VaultDock joins that network (cloudflared-net integration) so the tunnel
# reaches http://vaultdock:8080. 127.0.0.1 would be the cloudflared container.
cf_enable_network() { # cf_enable_network NETWORK SUBNETS
	local cur; cur=$(env_get VAULTDOCK_INTEGRATIONS)
	env_set_managed VAULTDOCK_TUNNEL_NETWORK "$1"
	integration_enabled cloudflared-net || env_set_managed VAULTDOCK_INTEGRATIONS "${cur:+$cur,}cloudflared-net"
	trusted_proxies_add "$2"
}

cf_join_network() {
	local n=$1 net c
	net=$(cf "$n" network)
	case "$net" in
	host | "") return 0 ;;
	bridge-default)
		cat <<EOF
cloudflared ($(cf "$n" id)) runs on Docker's default bridge network, which has no
container DNS, so it cannot reach VaultDock by name. Either:
  a) attach it to a shared network (survives restarts, not re-creation):
       docker network create vaultdock_tunnel
       docker network connect vaultdock_tunnel $(cf "$n" id)
     then run "$APP/install.sh integrations cloudflared" again; or
  b) point the tunnel at http://172.17.0.1:$(env_get VAULTDOCK_PORT) and set VAULTDOCK_BIND=172.17.0.1
     (this exposes VaultDock to every container on the default bridge).
EOF
		return 1 ;;
	esac
	if integration_enabled cloudflared-net && [ "$(env_get VAULTDOCK_TUNNEL_NETWORK)" = "$net" ]; then
		ok "VaultDock already joins the Docker network $net."
		trusted_proxies_add "$(cf "$n" network_subnet)"
		return 0
	fi
	for c in $(docker network inspect -f '{{range .Containers}}{{.Name}} {{end}}' "$net" 2>/dev/null); do
		[ "$c" != vaultdock ] || continue
		if docker inspect -f "{{range \$k, \$v := .NetworkSettings.Networks}}{{if eq \$k \"$net\"}}{{join \$v.Aliases \" \"}}{{end}}{{end}}" "$c" 2>/dev/null | tr ' ' '\n' | grep -qx vaultdock; then
			warn "Container $c already uses the name vaultdock on $net; VaultDock was not added to that network."
			return 1
		fi
	done
	confirm "Join VaultDock to cloudflared's Docker network $net? (only VaultDock is recreated; cloudflared is not touched)" y || return 1
	integration_change "VaultDock joined the Docker network $net" cf_enable_network "$net" "$(cf "$n" network_subnet)"
}

do_cloudflared() {
	installed || die "No VaultDock installation found in $DIR."
	local n i cnt action host svc
	info "Looking for cloudflared (systemd, snap, Docker, processes)"
	cf_detect
	cnt=${CF[count]:-0}
	[[ "$cnt" =~ ^[0-9]+$ ]] || cnt=0
	if [ "$cnt" = 0 ]; then cf_print_none; return 0; fi
	for ((i = 1; i <= cnt; i++)); do
		printf '  %s) %s %s: %s, mode %s, config %s\n' "$i" "$(cf "$i" kind)" "$(cf "$i" id)" "$(cf "$i" active)" "$(cf "$i" mode)" "$(cf "$i" config)"
	done
	n=$(cf_pick)
	[ -n "$n" ] || die "No cloudflared selected."
	action=$(cf_action "$n")
	svc=$(cf "$n" vaultdock_service)
	if [ "$svc" = "http://vaultdock:8080" ]; then
		cf_join_network "$n" || action=manual
	elif [ "$(env_get VAULTDOCK_BIND)" != 127.0.0.1 ]; then
		info "VaultDock also listens on $(env_get VAULTDOCK_BIND):$(env_get VAULTDOCK_PORT). To serve it only through the tunnel, set VAULTDOCK_BIND=127.0.0.1 in $ENV_FILE."
	fi
	if [ "$(env_get VAULTDOCK_TLS_SELF_SIGNED)" = true ]; then
		warn "VaultDock serves its own self-signed HTTPS. Behind a tunnel it should speak plain HTTP: set VAULTDOCK_TLS_SELF_SIGNED=false first. Nothing was changed."
		return 1
	fi
	ask host "Public hostname for VaultDock" "$(cf_hostname_default)"
	[[ "$host" =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] || die "Not a valid hostname: $host"
	case "$action" in
	edit-ingress) cf_edit_ingress "$n" "$host" "$svc" ;;
	dashboard)
		[ "$(cf "$n" remote_config_seen)" != yes ] || [ "$(cf "$n" mode)" != local ] ||
			info "This tunnel has a local config, but its logs show a configuration pushed from the dashboard, which overrides it."
		cf_print_dashboard "$n" "$host" "$svc"
		if [ "$YES" != 1 ] && have_tty && confirm "Check https://$host now?" y; then
			cf_probe "$host" || warn "https://$host/readyz does not answer yet. Check the service URL, the DNS record and any Access policy."
		fi
		;;
	*) cf_print_manual "$n" "$host" "$svc" ;;
	esac
}

do_integrations() {
	installed || die "No VaultDock installation found in $DIR."
	local which=${1:-} i
	if [ -z "$which" ]; then
		printf '  Enabled integrations: %s\n' "$(integrations | tr '\n' ' ' | sed 's/ *$//')"
		[ -n "$(integrations)" ] || printf '  (none: only the vaultdock service runs)\n'
		printf '  1) Cloudflare Tunnel: detect cloudflared and connect VaultDock\n  q) Back\n'
		ask i "Choose" "q"
		case "$i" in 1) which=cloudflared ;; *) return 0 ;; esac
	fi
	case "$which" in
	cloudflared) do_cloudflared ;;
	*) die "Unknown integration: $which (use cloudflared)" ;;
	esac
}

# --- install -------------------------------------------------------------------
https_mode_default() {
	case "${VAULTDOCK_HTTPS:-}" in
	tunnel|proxy) echo 1 ;; caddy) echo 2 ;; selfsigned) echo 3 ;; none) echo 4 ;;
	*) if [ "$YES" = 1 ] || ! have_tty; then echo 4; else echo 1; fi ;;
	esac
}

write_env() {
	local storage port url mode domain="" token uid gid host bind=0.0.0.0 proxies="" secure=auto
	local integ="" tls_self=false tls_hosts=""
	info "Configuration"
	printf '  Installation directory: %s\n' "$DIR"
	ask storage "Where should VaultDock store files? (use a path on your HDD or mounted volume)" "${VAULTDOCK_STORAGE:-$DIR/data/storage}"
	case "$storage" in /*) ;; *) die "Use an absolute path for storage." ;; esac
	ask port "Port to publish VaultDock on" "${VAULTDOCK_PORT:-$DEFAULT_PORT}"
	[[ "$port" =~ ^[0-9]+$ ]] && [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || die "Port must be a number between 1 and 65535."
	host=$(hostname -f 2>/dev/null || hostname)
	printf '  How will people reach VaultDock?\n'
	printf '    1) Cloudflare Tunnel or a reverse proxy on this server (HTTPS on your domain)\n'
	printf '    2) Built-in Caddy with a Let'"'"'s Encrypt certificate (needs a domain and ports 80/443)\n'
	printf '    3) Self-signed certificate on the local network (the desktop app pins it)\n'
	printf '    4) Plain HTTP on the local network (testing only; the desktop app needs HTTPS)\n'
	ask mode "Choose 1, 2, 3 or 4" "$(https_mode_default)"
	case "$mode" in
	1)
		ask url "Public HTTPS address (for example https://app.example.com)" "${VAULTDOCK_PUBLIC_URL:-https://$host}"
		case "$url" in https://*) ;; *) die "The public address must start with https://" ;; esac
		url=${url%/}
		# The tunnel or proxy runs on this host: listen on localhost only and
		# trust forwarded headers from the host and Docker networks.
		bind=127.0.0.1; proxies="127.0.0.1/32,::1/128,172.16.0.0/12"; secure=true
		;;
	2)
		ask domain "Domain name pointing at this server" "${VAULTDOCK_DOMAIN:-}"
		[ -n "$domain" ] || die "A domain is required for Let's Encrypt."
		url="https://$domain"; bind=127.0.0.1; integ=caddy; secure=true
		proxies="172.16.0.0/12,10.0.0.0/8,192.168.0.0/16"
		;;
	3)
		tls_self=true
		tls_hosts=$(hostname -I 2>/dev/null | tr ' ' ',' | sed 's/,*$//')
		url="https://$(hostname -I 2>/dev/null | awk '{print $1}'):$port"
		;;
	4) url=${VAULTDOCK_PUBLIC_URL:-http://$host:$port} ;;
	*) die "Choose 1, 2, 3 or 4." ;;
	esac
	if [ "$(id -u)" = 0 ]; then uid=65532; gid=65532; else uid=$(id -u); gid=$(id -g); fi
	token=$(random_token)
	mkdir -p -- "$APP"
	# A fresh install never silently replaces an existing configuration.
	if [ -f "$ENV_FILE" ]; then env_backup; warn "Existing $ENV_FILE saved in $ENV_BACKED_UP"; fi
	cat >"$ENV_FILE" <<ENVFILE
# VaultDock configuration, generated by install.sh on $(date -u +%Y-%m-%dT%H:%M:%SZ).
# Keep this file private: it contains the setup token. After editing it, apply
# the change with "docker compose up -d" in this folder, or "./install.sh upgrade".

# --- Paths --------------------------------------------------------------------
# Database and generated certificates.
VAULTDOCK_STATE_PATH=$DIR/data/state
# Stored files. Put this on your large disk.
VAULTDOCK_STORAGE_PATH=$storage
# This file, as used by docker compose.
VAULTDOCK_ENV_FILE=$ENV_FILE

# --- Container user that owns the data directories ---------------------------
VAULTDOCK_UID=$uid
VAULTDOCK_GID=$gid

# --- Network ------------------------------------------------------------------
# Host address and port VaultDock is published on.
VAULTDOCK_BIND=$bind
VAULTDOCK_PORT=$port
# Address people open in the browser and enter in the desktop app.
VAULTDOCK_PUBLIC_URL=$url
# Proxies whose X-Forwarded-For and X-Forwarded-Proto headers are trusted.
VAULTDOCK_TRUSTED_PROXIES=$proxies

# --- HTTPS --------------------------------------------------------------------
# Opt-in integrations under deploy/integrations/ (managed by install.sh).
# caddy = Caddy reverse proxy with Let's Encrypt for VAULTDOCK_DOMAIN.
VAULTDOCK_INTEGRATIONS=$integ
VAULTDOCK_DOMAIN=$domain
# Self-signed certificate served by VaultDock itself.
VAULTDOCK_TLS_SELF_SIGNED=$tls_self
VAULTDOCK_TLS_HOSTS=$tls_hosts
# Your own certificate (paths inside the container, for example under /state).
VAULTDOCK_TLS_CERT=
VAULTDOCK_TLS_KEY=

# --- Security -----------------------------------------------------------------
# One-time token required to create the first administrator (generated).
VAULTDOCK_SETUP_TOKEN=$token
# Secure cookies: auto (when HTTPS is detected), true or false.
VAULTDOCK_COOKIE_SECURE=$secure

# --- Transfers ----------------------------------------------------------------
VAULTDOCK_CHUNK_SIZE_MB=8
VAULTDOCK_MAX_CONCURRENT_CHUNKS=64
VAULTDOCK_UPLOAD_TTL_HOURS=48

# --- Logging ------------------------------------------------------------------
VAULTDOCK_LOG_LEVEL=info
VAULTDOCK_LOG_FORMAT=json

# --- Optional city database for device request locations (mount under /state)
VAULTDOCK_GEOIP_DB=

# --- Updates ------------------------------------------------------------------
VAULTDOCK_UPDATE_CHECK=true
VAULTDOCK_UPDATE_REPO=$REPO
# Managed by "install.sh auto-update on|off".
VAULTDOCK_AUTO_UPDATE=off
# Managed by install.sh: the image and version currently installed.
VAULTDOCK_IMAGE=
VAULTDOCK_VERSION=
ENVFILE
	chmod 600 "$ENV_FILE"
	# Everything else the schema defines (new settings get their defaults).
	env_merge_missing >/dev/null
}

# install_files is the file part of a fresh install: runtime files, a new
# .env, data directories and the server key (KEK). No Docker calls.
install_files() {
	fetch_app "$APP.new"
	rm -rf -- "$APP"; mv -- "$APP.new" "$APP"
	write_env
	prepare_dirs
	kek_ensure
}

do_install() {
	require
	mkdir -p -- "$DIR"
	if installed; then
		warn "VaultDock is already installed in $DIR."
		printf '  1) Reinstall the application and keep all data and settings (same as upgrade)\n  2) Fresh install: move the existing data and settings aside to %s (nothing is deleted)\n  3) Cancel\n' "$BACKUPS"
		local choice; ask choice "Choose" "3"
		case "$choice" in
		1) do_upgrade; return ;;
		2)
			local ts dest; ts=$(date +%Y%m%d-%H%M%S); dest="$BACKUPS/replaced-$ts"
			confirm "Stop VaultDock and move $DIR/data and .env to $dest?" n || die "Cancelled."
			compose down || true
			mkdir -p -- "$dest"
			[ -d "$DIR/data" ] && mv -- "$DIR/data" "$dest/"
			mv -- "$ENV_FILE" "$dest/.env"
			local old_storage; old_storage=$(grep -E '^VAULTDOCK_STORAGE_PATH=' "$dest/.env" | cut -d= -f2- || true)
			if [ -n "$old_storage" ] && [ -d "$old_storage" ] && [[ "$old_storage" != "$DIR/data/"* ]]; then
				warn "Stored files at $old_storage are outside the install directory and were left untouched. Choose a different storage path or move them yourself."
			fi
			ok "Previous installation preserved in $dest"
			;;
		*) die "Cancelled. Nothing was changed." ;;
		esac
	fi
	resolve_ref_if_remote
	install_files
	build_image
	info "Starting VaultDock"
	compose up -d
	wait_healthy || die "Installation finished but VaultDock is not healthy. Fix the problem above, then run: $0 upgrade"
	print_summary
	link_source_env
	kek_backup_prompt
	if [ "$(env_get VAULTDOCK_BIND)" = 127.0.0.1 ] && ! integration_enabled caddy && [ "$YES" != 1 ] && have_tty &&
		confirm "Look for an existing Cloudflare Tunnel (cloudflared) on this server and connect it now?" y; then
		(do_cloudflared) || warn "The tunnel was not set up. Run it again any time: $APP/install.sh integrations cloudflared"
	fi
}

print_summary() {
	local url bind port
	url=$(env_get VAULTDOCK_PUBLIC_URL); bind=$(env_get VAULTDOCK_BIND); port=$(env_get VAULTDOCK_PORT)
	printf '\n%sVaultDock is installed.%s\n' "$B" "$N"
	printf '  Open:            %s\n' "$url"
	printf '  Setup token:     %s   (needed once to create the administrator)\n' "$(env_get VAULTDOCK_SETUP_TOKEN)"
	printf '  Listening on:    %s:%s\n' "$bind" "$port"
	printf '  Configuration:   %s\n' "$ENV_FILE"
	printf '  Files stored in: %s\n' "$(env_get VAULTDOCK_STORAGE_PATH)"
	printf '  Database/state:  %s\n' "$(env_get VAULTDOCK_STATE_PATH)"
	printf '  Manage:          %s   (upgrade, auto-update, status, uninstall)\n' "$APP/install.sh"
	printf '  Logs:            docker logs -f vaultdock\n'
	if [ "$bind" = 127.0.0.1 ] && ! integration_enabled caddy; then
		printf '\nPoint your Cloudflare Tunnel or reverse proxy for %s at http://127.0.0.1:%s\n' "$url" "$port"
		printf '(cloudflared in Docker on its own network: run "%s integrations cloudflared")\n' "$APP/install.sh"
	fi
	printf '\nNext: open the address, create the administrator with the setup token, then\ninstall the VaultDock desktop app on Windows and connect it to this server.\n'
}

# --- upgrade -------------------------------------------------------------------
# version_newer A B: true when release A is newer than B. A non-version B
# (for example a build of the main branch) is always considered older.
version_newer() {
	local a=${1%%[-+]*} b=${2%%[-+]*} i x y
	local -a va vb
	[[ "$b" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]] || return 0
	IFS=. read -r -a va <<<"$a"; IFS=. read -r -a vb <<<"$b"
	for i in 0 1 2; do
		x=${va[$i]:-0}; y=${vb[$i]:-0}
		((10#$x > 10#$y)) && return 0
		((10#$x < 10#$y)) && return 1
	done
	# Same core version: a final release is newer than a pre-release of it.
	[ "$1" = "$a" ] && [ "$2" != "$b" ]
}

backup_now() { # backup_now -> prints backup dir
	local ts dest; ts=$(date +%Y%m%d-%H%M%S); dest="$BACKUPS/upgrade-$ts"
	mkdir -p -- "$dest"
	cp -p -- "$ENV_FILE" "$dest/.env"
	if [ "$(docker inspect -f '{{.State.Running}}' vaultdock 2>/dev/null || true)" = true ]; then
		docker exec vaultdock vaultdock backup "/state/vaultdock-backup-$ts.db" >/dev/null
		mv -- "$(env_get VAULTDOCK_STATE_PATH)/vaultdock-backup-$ts.db" "$dest/vaultdock.db"
	else
		cp -p -- "$(env_get VAULTDOCK_STATE_PATH)/vaultdock.db" "$dest/vaultdock.db" 2>/dev/null || warn "No database found to back up."
	fi
	(cd -- "$DIR" && tar -cf "$dest/app.tar" app)
	# The KEK is only checked, never copied (it must not sit next to the DB copy).
	if [ -s "$(kek_host_path)" ]; then echo present; else echo missing; fi >"$dest/kek-status"
	printf '%s' "$dest"
}

# upgrade_lock makes sure only one upgrade (manual or scheduled) runs at a time.
upgrade_lock() {
	command -v flock >/dev/null 2>&1 || return 0
	exec 9>"$DIR/.upgrade.lock"
	flock -n 9 || die "Another upgrade is already running."
}

# upgrade_files is the file part of an upgrade (no Docker calls): stage the new
# app/, keep locally modified managed files (.new/.local), swap it in with the
# previous app/ (including .env) kept in $APP.old for rollback, migrate the
# bundled Caddy profile and merge .env (add-only). Stops with the previous
# app/ restored when the Caddy migration fails. (Called without "||" so that
# errexit stays active inside.)
upgrade_files() {
	fetch_app "$APP.new"
	apply_managed_files "$APP.new"
	rm -rf -- "$APP.old"; mv -- "$APP" "$APP.old"; mv -- "$APP.new" "$APP"
	env_set_managed VAULTDOCK_ENV_FILE "$ENV_FILE"
	if ! migrate_caddy_profile; then
		rm -rf -- "$APP"; mv -- "$APP.old" "$APP"
		die "Upgrade aborted: could not migrate the bundled Caddy proxy. Nothing else was changed."
	fi
	env_merge
}

do_upgrade() {
	require
	installed || die "No VaultDock installation found in $DIR. Choose Install first."
	upgrade_lock
	if [ -z "$(local_source)" ]; then
		resolve_ref
		local current; current=$(env_get VAULTDOCK_VERSION)
		if [ "$IF_NEWER" = 1 ]; then
			if [ -z "$RELEASE_VERSION" ]; then
				info "No published release to upgrade to. Nothing to do."
				return 0
			fi
			if [ "${current#v}" = "$RELEASE_VERSION" ] || ! version_newer "$RELEASE_VERSION" "${current#v}"; then
				info "VaultDock ${current:-?} is up to date (latest release: $RELEASE_VERSION)."
				return 0
			fi
		fi
		info "Upgrading VaultDock ${current:-?} to ${RELEASE_VERSION:-$REF}"
	fi
	# The KEK is checked before anything changes: an install from before the
	# KEK gets one now; a missing KEK with sealed keys in the DB stops here.
	kek_ensure
	env_add VAULTDOCK_UPDATE_REPO "$REPO"
	if [ "$(env_get VAULTDOCK_UPDATE_REPO)" = "$LEGACY_REPO" ] && [ "$REPO" != "$LEGACY_REPO" ]; then
		warn "VAULTDOCK_UPDATE_REPO is $LEGACY_REPO, where the server was published before. Releases now come from $REPO; change it in $ENV_FILE so the admin console finds them (install.sh does not change your settings)."
	fi
	info "Backing up the database, configuration and application files"
	local bk; bk=$(backup_now)
	ok "Backup written to $bk"
	local prev_image; prev_image=$(env_get VAULTDOCK_IMAGE); prev_image=${prev_image:-vaultdock:local}
	local prev_version; prev_version=$(env_get VAULTDOCK_VERSION)
	docker image inspect "$prev_image" >/dev/null 2>&1 && docker tag "$prev_image" vaultdock:rollback
	upgrade_files
	if ! build_image; then
		warn "Build failed; restoring the previous version."
		rm -rf -- "$APP"; mv -- "$APP.old" "$APP"
		env_set_managed VAULTDOCK_IMAGE "$prev_image"; env_set_managed VAULTDOCK_VERSION "$prev_version"
		die "Upgrade aborted. The previous version is still running."
	fi
	info "Restarting with the new version (database migrations run automatically)"
	if compose up -d --remove-orphans && wait_healthy; then
		rm -rf -- "$APP.old"
		ok "Upgrade complete. Backup kept at $bk"
		link_source_env
		if [ -n "$MODIFIED_FILES" ]; then warn "Locally modified files:$MODIFIED_FILES (see the .new/.local copies in $APP)."; fi
		kek_backup_prompt
		return
	fi
	warn "The new version is not healthy. Rolling back to the previous version and database backup."
	# compose down with the new file set also stops integration services the
	# upgrade added; the previous app/ (files, .env, integrations) comes back.
	compose down || true
	rm -rf -- "$APP"; mv -- "$APP.old" "$APP"
	cp -p -- "$bk/vaultdock.db" "$(env_get VAULTDOCK_STATE_PATH)/vaultdock.db"
	rm -f -- "$(env_get VAULTDOCK_STATE_PATH)/vaultdock.db-wal" "$(env_get VAULTDOCK_STATE_PATH)/vaultdock.db-shm"
	docker image inspect vaultdock:rollback >/dev/null 2>&1 && docker tag vaultdock:rollback "$prev_image"
	env_set_managed VAULTDOCK_IMAGE "$prev_image"; env_set_managed VAULTDOCK_VERSION "$prev_version"
	compose up -d
	wait_healthy || true
	die "Upgrade failed and was rolled back. Backup: $bk"
}

# --- scheduled updates ---------------------------------------------------------
CRON_TAG="# vaultdock-auto-update $DIR"

cron_line() {
	printf '0 4 * * * PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin VAULTDOCK_DIR=%q VAULTDOCK_YES=1 %q upgrade --if-newer >>%q 2>&1 %s\n' \
		"$DIR" "$APP/install.sh" "$DIR/logs/auto-update.log" "$CRON_TAG"
}

current_crontab() { crontab -l 2>/dev/null | grep -vF "$CRON_TAG" || true; }

apply_auto_update_env() { # apply_auto_update_env on|off
	env_set_managed VAULTDOCK_AUTO_UPDATE "$1"
	# Recreate the container so the admin console shows the new setting.
	if [ "$(docker inspect -f '{{.State.Running}}' vaultdock 2>/dev/null || true)" = true ]; then
		if compose up -d >/dev/null 2>&1; then
			wait_healthy >/dev/null 2>&1 || warn "VaultDock is restarting slowly; check: docker logs vaultdock"
		else
			warn "Could not restart VaultDock; the admin console shows the change after the next restart."
		fi
	fi
}

do_auto_update() {
	installed || die "No VaultDock installation found in $DIR."
	local action=${1:-status}
	case "$action" in
	on)
		command -v crontab >/dev/null 2>&1 || die "Scheduled updates need cron (the crontab command). Install cron, or run '$APP/install.sh upgrade' yourself."
		mkdir -p -- "$DIR/logs"
		{ current_crontab; cron_line; } | crontab -
		apply_auto_update_env on
		ok "Automatic updates are on. VaultDock checks every day at 04:00 and installs new releases."
		printf '  Log: %s\n' "$DIR/logs/auto-update.log"
		;;
	off)
		if command -v crontab >/dev/null 2>&1; then current_crontab | crontab -; fi
		apply_auto_update_env off
		ok "Automatic updates are off."
		;;
	status)
		if command -v crontab >/dev/null 2>&1 && crontab -l 2>/dev/null | grep -qF "$CRON_TAG"; then
			ok "Automatic updates are on (daily at 04:00)."
		else
			info "Automatic updates are off. Turn them on with: $APP/install.sh auto-update on"
		fi
		printf '  Installed version: %s\n' "$(env_get VAULTDOCK_VERSION)"
		if [ -f "$DIR/logs/auto-update.log" ]; then
			printf '  Last log lines (%s):\n' "$DIR/logs/auto-update.log"
			tail -n 5 "$DIR/logs/auto-update.log" | sed 's/^/    /'
		fi
		;;
	*) die "Use: auto-update on|off|status" ;;
	esac
}

# --- uninstall -----------------------------------------------------------------
do_uninstall() {
	installed || die "No VaultDock installation found in $DIR."
	command -v docker >/dev/null 2>&1 || die "Docker is required to remove the containers."
	local storage state; storage=$(env_get VAULTDOCK_STORAGE_PATH); state=$(env_get VAULTDOCK_STATE_PATH)
	cat <<EOF
${B}Uninstall VaultDock${N}
  1) Remove the VaultDock services only. Keeps every stored file, the database,
     settings and backups in $DIR so you can reinstall later.
  2) Remove the services AND permanently delete all VaultDock data:
       stored files: $storage
       database:     $state
       settings, backups and application files in $DIR
  3) Cancel
EOF
	local choice; ask choice "Choose" "3"
	case "$choice" in
	1)
		confirm "Stop and remove the VaultDock containers?" n || die "Cancelled."
		if command -v crontab >/dev/null 2>&1; then current_crontab | crontab - || true; fi
		compose down --remove-orphans
		docker image rm vaultdock:local vaultdock:rollback >/dev/null 2>&1 || true
		ok "Services removed. Your data is still in $DIR (storage: $storage)."
		;;
	2)
		warn "This permanently deletes every file stored in VaultDock. It cannot be undone."
		typed_confirm "DELETE ALL DATA" || die "Cancelled. Nothing was deleted."
		if command -v crontab >/dev/null 2>&1; then current_crontab | crontab - || true; fi
		compose down --remove-orphans --volumes
		docker image rm vaultdock:local vaultdock:rollback >/dev/null 2>&1 || true
		if [ -n "$storage" ] && [ "$storage" != "/" ] && [ -d "$storage" ]; then
			rm -rf -- "$storage"
		fi
		rm -rf -- "$DIR"
		ok "VaultDock and all its data were removed."
		;;
	*) die "Cancelled. Nothing was changed." ;;
	esac
}

do_status() {
	if installed; then
		info "Installed in $DIR (version $(env_get VAULTDOCK_VERSION), configuration $ENV_FILE)"
		compose ps || true
	else
		info "Not installed in $DIR"
	fi
}

menu() {
	printf '\n%sVaultDock installer%s  (install directory: %s)\n' "$B" "$N" "$DIR"
	if installed; then printf '  Existing installation detected.\n'; fi
	printf '  1) Install / Replace\n  2) Upgrade\n  3) Integrations (Cloudflare Tunnel)\n  4) Environment review\n  5) Automatic updates\n  6) Uninstall\n  7) Server key status\n  q) Quit\n'
	local choice; ask choice "Choose an option" "q"
	case "$choice" in
	1) do_install ;;
	2) do_upgrade ;;
	3) do_integrations ;;
	4) do_env_review ;;
	5)
		do_auto_update status
		local a; ask a "Turn automatic updates on or off? (on/off, empty to keep)" ""
		case "$a" in on|off) do_auto_update "$a" ;; esac
		;;
	6) do_uninstall ;;
	7) do_kek_status ;;
	*) echo "Bye." ;;
	esac
}

main() {
	local cmd=${1:-} a prev=""
	migrate_layout
	for a in "$@"; do
		if [ "$prev" = --approve-env-removal ]; then ENV_APPROVE_REMOVE="$ENV_APPROVE_REMOVE,$a"; prev=""; continue; fi
		case "$a" in
		--yes) YES=1 ;;
		--if-newer) IF_NEWER=1 ;;
		--approve-env-removal) prev=$a ;;
		--approve-env-removal=*) ENV_APPROVE_REMOVE="$ENV_APPROVE_REMOVE,${a#*=}" ;;
		esac
	done
	case "$cmd" in
	install) do_install ;;
	upgrade) do_upgrade ;;
	uninstall) do_uninstall ;;
	status) do_status ;;
	auto-update) do_auto_update "${2:-status}" ;;
	env-review) do_env_review ;;
	integrations) do_integrations "${2:-}" ;;
	kek-status) do_kek_status ;;
	""|--yes) menu ;;
	-h|--help) sed -n '2,/^set -Eeuo/p' "$0" | sed '$d' ;;
	*) die "Unknown command: $cmd (use install, upgrade, uninstall, status, auto-update, env-review, integrations or kek-status)" ;;
	esac
}

# VAULTDOCK_INSTALL_LIB=1 only defines the functions (scripts/test-install).
if [ "${VAULTDOCK_INSTALL_LIB:-0}" != 1 ]; then
	main "$@"
fi
