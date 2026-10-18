#!/usr/bin/env bash
# Dry-mode scenarios for install.sh: no Docker, no network, no root.
#
# install.sh is sourced with VAULTDOCK_INSTALL_LIB=1 and its file-level steps
# (install_files, upgrade_files, env_merge, apply_managed_files, kek_ensure,
# the cloudflared ingress editor, ...) run against throw-away directories.
# Each scenario runs in its own subshell with its own VAULTDOCK_DIR.
#
#   bash scripts/test-install/dry.sh [scenario...]
#   scenarios: sync fresh upgrade legacy-caddy modified obsolete kek cloudflared
set -u

HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
ROOT=$(cd -- "$HERE/../.." && pwd -P)
FIX="$HERE/fixtures"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/vd-install-test.XXXXXX")
trap 'rm -rf -- "$WORK"' EXIT
FAILS=0
LINUX=0
[ "$(uname -s)" = Linux ] && LINUX=1

# --- assertions --------------------------------------------------------------
fail() { printf '    FAIL: %s\n' "$*"; echo x >>"$WORK/.fails"; }
pass() { printf '    ok:   %s\n' "$*"; }
check() { # check "description" command...
	local d=$1; shift
	if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d"; fi
}
envval() { grep -E "^$1=" "$ENV_FILE" | tail -n1 | cut -d= -f2-; }
same() { cmp -s -- "$1" "$2"; }
hex() { od -An -tx1 "$1" | tr -d ' \n'; }

# load DIRNAME: fresh environment and the install.sh functions.
load() {
	export VAULTDOCK_DIR="$WORK/$1" VAULTDOCK_SOURCE="$ROOT" VAULTDOCK_YES=1 VAULTDOCK_HTTPS=none \
		VAULTDOCK_STORAGE="$WORK/$1/data/storage" VAULTDOCK_PORT=3000 VAULTDOCK_PUBLIC_URL=http://vd.test:3000
	mkdir -p -- "$VAULTDOCK_DIR"
	# shellcheck source=../../install.sh
	VAULTDOCK_INSTALL_LIB=1 . "$ROOT/install.sh"
	set +eEu
	trap - ERR
	have_tty() { return 1; }
}

fresh_install() { install_files >"$WORK/$1.install.log" 2>&1; }

# --- scenarios -----------------------------------------------------------------
sc_sync() {
	load sync
	local a b
	a=$(runtime_files "$ROOT" | sort | tr '\n' ' ')
	# shellcheck disable=SC2086
	b=$(printf '%s\n' $RUNTIME_FILES | sort | tr '\n' ' ')
	check "RUNTIME_FILES matches managed-files.json" [ "$a" = "$b" ]
	local f missing=""
	for f in $a; do [ -f "$ROOT/$f" ] || missing="$missing $f"; done
	check "every managed file exists in the repository${missing:+ (missing:$missing)}" [ -z "$missing" ]
	a=$(json_records "$ROOT/$ENV_SCHEMA_REL" name managed | awk -F"$US" '$2 == "true" { print $1 }' | sort | tr '\n' ' ')
	# shellcheck disable=SC2086
	b=$(printf '%s\n' $MANAGED_KEYS | sort | tr '\n' ' ')
	check "MANAGED_KEYS matches the schema's managed variables" [ "$a" = "$b" ]
	check "schema has VAULTDOCK_TUNNEL_NETWORK" grep -q '"VAULTDOCK_TUNNEL_NETWORK"' "$ROOT/$ENV_SCHEMA_REL"
	check "default repository is vad-server" [ "$REPO" = ASPEC-RACING-PTY-LTD/vad-server ]
	VAULTDOCK_SOURCE="" RELEASE_VERSION=1.2.3
	local_source() { :; }
	check "release image is ghcr.io/aspec-racing-pty-ltd/vad-server" [ "$(release_image)" = ghcr.io/aspec-racing-pty-ltd/vad-server:1.2.3 ]
	check "legacy image fallback is ghcr.io/aspec-racing-pty-ltd/vaultdock" [ "$(legacy_release_image)" = ghcr.io/aspec-racing-pty-ltd/vaultdock:1.2.3 ]
	check "integration_file maps cloudflared-net to cloudflared/" [ "$(integration_file cloudflared-net)" = "$APP/deploy/integrations/cloudflared/compose.cloudflared-net.yml" ]
	check "detect.sh runs read-only and reports a count" sh -c "sh '$ROOT/$CF_DETECT_REL' 3000 | grep -q '^count='"
}

sc_fresh() {
	load fresh
	fresh_install fresh
	check "install_files succeeded" [ -f "$ENV_FILE" ]
	local f missing=""
	for f in $(runtime_files "$APP"); do [ -f "$APP/$f" ] || missing="$missing $f"; done
	check "all runtime files installed${missing:+ (missing:$missing)}" [ -z "$missing" ]
	check "shipped checksums recorded" [ "$(wc -l <"$APP/.managed-files.sha256")" -eq "$(runtime_files "$APP" | wc -l)" ]
	missing=""
	while IFS="$US" read -r f e o; do
		[ "$e" = true ] && [ "$o" != true ] && ! env_has "$f" && missing="$missing $f"
	done < <(json_records "$APP/$ENV_SCHEMA_REL" name env_file obsolete)
	check "every schema variable is in .env${missing:+ (missing:$missing)}" [ -z "$missing" ]
	check "VAULTDOCK_UPDATE_REPO is vad-server" [ "$(envval VAULTDOCK_UPDATE_REPO)" = ASPEC-RACING-PTY-LTD/vad-server ]
	local kek; kek="$(state_path)/keys/kek"
	check "KEK generated at the host path of /state/keys/kek" [ -f "$kek" ]
	check "KEK is 32 bytes" [ "$(wc -c <"$kek" | tr -d ' ')" = 32 ]
	if [ "$LINUX" = 1 ]; then check "KEK mode 0600" [ "$(stat -c %a "$kek")" = 600 ]; fi
	check "install output does not contain the KEK" sh -c "! grep -qiF '$(hex "$kek")' '$WORK/fresh.install.log'"
	kek_backup_prompt >"$WORK/fresh.prompt.log" 2>&1
	check "backup prompt explains the off-server copy" grep -q 'OUTSIDE this server' "$WORK/fresh.prompt.log"
	check "backup prompt warns about losing key and recovery keys" grep -q 'BOTH this key and every recovery key' "$WORK/fresh.prompt.log"
	check "backup prompt does not print the KEK" sh -c "! grep -qiF '$(hex "$kek")' '$WORK/fresh.prompt.log'"
}

sc_upgrade() {
	load upgrade
	fresh_install upgrade
	local kek h0 tok
	kek="$(state_path)/keys/kek"; h0=$(hex "$kek"); tok=$(envval VAULTDOCK_SETUP_TOKEN)
	sed -i -e 's/^VAULTDOCK_PORT=.*/VAULTDOCK_PORT=4444/' -e '/^VAULTDOCK_SYNC_TOMBSTONE_DAYS=/d' -e '/^VAULTDOCK_TUNNEL_NETWORK=/d' "$ENV_FILE"
	echo 'MY_OWN_SETTING=keep me' >>"$ENV_FILE"
	echo 'services: {}' >"$APP/docker-compose.override.yml"
	kek_ensure >/dev/null 2>&1
	upgrade_files >"$WORK/upgrade.log" 2>&1
	check "existing value kept (VAULTDOCK_PORT=4444)" [ "$(envval VAULTDOCK_PORT)" = 4444 ]
	check "secret kept (setup token)" [ "$(envval VAULTDOCK_SETUP_TOKEN)" = "$tok" ]
	check "unknown user setting kept" [ "$(envval MY_OWN_SETTING)" = "keep me" ]
	check "missing variable re-added with its default" [ "$(envval VAULTDOCK_SYNC_TOMBSTONE_DAYS)" = 90 ]
	check "new variable VAULTDOCK_TUNNEL_NETWORK added" env_has VAULTDOCK_TUNNEL_NETWORK
	check "KEK not regenerated" [ "$(hex "$kek")" = "$h0" ]
	check "compose override carried over" [ -f "$APP/docker-compose.override.yml" ]
	check "previous app/ kept for rollback" grep -q '^VAULTDOCK_PORT=4444' "$APP.old/.env"
	check "no .new files for unmodified files" [ -z "$(find "$APP" -name '*.new' -o -name '*.local')" ]
	check ".env backup written before the merge" [ -n "$(find "$BACKUPS" -name .env)" ]
}

legacy_layout() { # legacy_layout DIRNAME [modified-caddyfile]
	mkdir -p -- "$APP/deploy" "$DIR/data/state" "$DIR/data/storage"
	cp -- "$FIX/legacy-docker-compose.yml" "$APP/docker-compose.yml"
	cp -- "$FIX/legacy-Caddyfile" "$APP/deploy/Caddyfile"
	[ -z "${2:-}" ] || echo '# my change' >>"$APP/deploy/Caddyfile"
	printf '#!/bin/sh\necho old installer\n' >"$APP/install.sh"
	cat >"$ENV_FILE" <<EOF
VAULTDOCK_STATE_PATH=$DIR/data/state
VAULTDOCK_STORAGE_PATH=$DIR/data/storage
VAULTDOCK_ENV_FILE=$ENV_FILE
VAULTDOCK_UID=65532
VAULTDOCK_GID=65532
VAULTDOCK_BIND=127.0.0.1
VAULTDOCK_PORT=3000
VAULTDOCK_PUBLIC_URL=https://vault.example.com
VAULTDOCK_TRUSTED_PROXIES=172.16.0.0/12
COMPOSE_PROFILES=caddy
VAULTDOCK_DOMAIN=vault.example.com
VAULTDOCK_SETUP_TOKEN=legacy-token
VAULTDOCK_UPDATE_REPO=ASPEC-RACING-PTY-LTD/vad-client
VAULTDOCK_IMAGE=ghcr.io/aspec-racing-pty-ltd/vaultdock:1.0.0
VAULTDOCK_VERSION=1.0.0
EOF
	printf 'SQLite format 3\000legacy database without key tables' >"$DIR/data/state/vaultdock.db"
}

sc_legacy_caddy() {
	load legacy
	legacy_layout legacy
	kek_ensure >"$WORK/legacy.log" 2>&1
	check "KEK created for an install from before the KEK" [ -s "$(state_path)/keys/kek" ]
	upgrade_files >>"$WORK/legacy.log" 2>&1
	check "caddy integration enabled" integration_enabled caddy
	check "compose.caddy.yml installed" [ -f "$(integration_file caddy)" ]
	check "legacy docker-compose.yml (known version) replaced, no .new" sh -c "cmp -s '$ROOT/docker-compose.yml' '$APP/docker-compose.yml' && [ ! -e '$APP/docker-compose.yml.new' ]"
	check "unmodified legacy Caddyfile moved to the integration, no .new" sh -c "cmp -s '$ROOT/deploy/integrations/caddy/Caddyfile' '$APP/deploy/integrations/caddy/Caddyfile' && [ ! -e '$APP/deploy/integrations/caddy/Caddyfile.new' ]"
	check "COMPOSE_PROFILES kept without approval (--yes)" env_has COMPOSE_PROFILES
	check "existing values kept" [ "$(envval VAULTDOCK_SETUP_TOKEN)" = legacy-token ]
	check "VAULTDOCK_UPDATE_REPO not changed silently" [ "$(envval VAULTDOCK_UPDATE_REPO)" = ASPEC-RACING-PTY-LTD/vad-client ]
	check "previous image recorded for rollback is untouched" [ "$(envval VAULTDOCK_IMAGE)" = ghcr.io/aspec-racing-pty-ltd/vaultdock:1.0.0 ]

	load legacy-mod
	legacy_layout legacy-mod modified
	upgrade_files >"$WORK/legacy-mod.log" 2>&1
	check "modified legacy Caddyfile kept at the new path" grep -q '# my change' "$APP/deploy/integrations/caddy/Caddyfile"
	check "shipped Caddyfile written as .new" same "$ROOT/deploy/integrations/caddy/Caddyfile" "$APP/deploy/integrations/caddy/Caddyfile.new"
}

sc_modified() {
	load modified
	fresh_install modified
	echo '# local tweak' >>"$APP/docker-compose.yml"
	echo '# local tweak' >>"$APP/deploy/integrations/cloudflared/compose.cloudflared-net.yml"
	echo '# local tweak' >>"$APP/install.sh"
	upgrade_files >"$WORK/modified.log" 2>&1
	check "modified docker-compose.yml kept" grep -q '# local tweak' "$APP/docker-compose.yml"
	check "shipped docker-compose.yml written as .new" same "$ROOT/docker-compose.yml" "$APP/docker-compose.yml.new"
	check "modified integration compose kept with .new" sh -c "grep -q '# local tweak' '$APP/deploy/integrations/cloudflared/compose.cloudflared-net.yml' && [ -f '$APP/deploy/integrations/cloudflared/compose.cloudflared-net.yml.new' ]"
	check "install.sh (replace) updated to the shipped version" same "$ROOT/install.sh" "$APP/install.sh"
	check "modified install.sh saved as install.sh.local" grep -q '# local tweak' "$APP/install.sh.local"
	check "user warned about local changes" grep -q 'has local changes and was kept' "$WORK/modified.log"
	check "unmodified files have no .new" [ ! -e "$APP/deploy/env.schema.json.new" ]
	upgrade_files >"$WORK/modified2.log" 2>&1
	check "second upgrade still keeps the modified compose" grep -q '# local tweak' "$APP/docker-compose.yml"
	check "second upgrade leaves install.sh alone (now unmodified)" [ ! -e "$APP/install.sh.local" ]
}

sc_obsolete() {
	load obsolete
	fresh_install obsolete
	env_add COMPOSE_PROFILES caddy
	env_merge >"$WORK/obsolete.log" 2>&1
	check "obsolete variable kept with --yes and no approval" env_has COMPOSE_PROFILES
	check "obsolete variable listed" grep -q 'COMPOSE_PROFILES (obsolete since' "$WORK/obsolete.log"
	ENV_APPROVE_REMOVE=COMPOSE_PROFILES env_merge >/dev/null 2>&1
	check "removed with --approve-env-removal" sh -c "! grep -q '^COMPOSE_PROFILES=' '$ENV_FILE'"
	ENV_APPROVE_REMOVE=""
	env_add COMPOSE_PROFILES caddy
	mkdir -p -- "$(state_path)/deploy"
	printf '[{"name":"COMPOSE_PROFILES","approved_by":"admin","approved_at":"2026-10-18T00:00:00Z"},{"name":"VAULTDOCK_PORT"}]\n' >"$(env_admin_approvals_file)"
	env_merge >/dev/null 2>&1
	check "removed with an admin console approval" sh -c "! grep -q '^COMPOSE_PROFILES=' '$ENV_FILE'"
	check "approval for a non-obsolete variable ignored" env_has VAULTDOCK_PORT
	check "admin approvals file cleared" [ ! -e "$(env_admin_approvals_file)" ]
	env_add COMPOSE_PROFILES caddy
	echo COMPOSE_PROFILES >"$APP/$ENV_APPROVALS_REL"
	env_merge >/dev/null 2>&1
	check "removed with app/deploy/env-approvals" sh -c "! grep -q '^COMPOSE_PROFILES=' '$ENV_FILE'"
	check "env_set_managed refuses user variables" sh -c "VAULTDOCK_INSTALL_LIB=1 VAULTDOCK_DIR='$DIR' bash -c '. \"$ROOT/install.sh\"; env_set_managed VAULTDOCK_PORT 1' 2>/dev/null; [ \$? -ne 0 ]"
}

sc_kek() {
	load kek
	fresh_install kek
	local kek h0; kek="$(state_path)/keys/kek"; h0=$(hex "$kek")
	kek_ensure >/dev/null 2>&1
	check "existing KEK never regenerated" [ "$(hex "$kek")" = "$h0" ]
	rm -f -- "$kek"
	printf 'SQLite format 3\000CREATE TABLE server_keys (private_sealed_by_kek BLOB)' >"$(state_path)/vaultdock.db"
	if command -v sqlite3 >/dev/null 2>&1; then
		rm -f -- "$(state_path)/vaultdock.db"
		sqlite3 "$(state_path)/vaultdock.db" "CREATE TABLE server_keys (id INTEGER, private_sealed_by_kek BLOB); INSERT INTO server_keys VALUES (1, x'00');"
	fi
	(kek_ensure) >"$WORK/kek.refuse.log" 2>&1
	check "missing KEK with sealed keys in the DB: refused" [ $? -ne 0 ]
	check "refusal explains the restore and the data loss" grep -q 'will not create one' "$WORK/kek.refuse.log"
	check "no new KEK written after the refusal" [ ! -e "$kek" ]
	if command -v sqlite3 >/dev/null 2>&1; then
		sqlite3 "$(state_path)/vaultdock.db" "DELETE FROM server_keys;"
		kek_ensure >/dev/null 2>&1
		check "sqlite3: empty key tables allow a new KEK" [ -s "$kek" ]
		rm -f -- "$kek"
	else
		pass "sqlite3 not installed: exact key-table count not tested"
	fi
	printf 'SQLite format 3\000legacy' >"$(state_path)/vaultdock.db"
	kek_ensure >/dev/null 2>&1
	check "DB without key tables: KEK generated" [ "$(wc -c <"$kek" | tr -d ' ')" = 32 ]
	check "new KEK differs from the lost one" [ "$(hex "$kek")" != "$h0" ]
	sed -i 's#^VAULTDOCK_KEK_FILE=.*#VAULTDOCK_KEK_FILE=/run/secrets/vaultdock_kek#' "$ENV_FILE"
	check "KEK outside /state (Docker secret) is not managed" [ -z "$(kek_host_path)" ]
	sed -i 's#^VAULTDOCK_KEK_FILE=.*#VAULTDOCK_KEK_FILE=/state/../etc/kek#' "$ENV_FILE"
	check "KEK path with .. rejected" [ -z "$(kek_host_path)" ]
}

sc_cloudflared() {
	load cloudflared
	fresh_install cloudflared
	local d="$WORK/cf" svc="http://127.0.0.1:3000"
	mkdir -p -- "$d/bin"
	cat >"$d/config.yml" <<'EOF'
tunnel: 6ff42ae2-765d-4adf-8112-31c55c1551ef
credentials-file: /root/.cloudflared/6ff42ae2.json
ingress:
  - hostname: other.example.com
    service: http://127.0.0.1:8000
  # keep this comment
  - service: http_status:404
EOF
	cp -- "$d/config.yml" "$d/orig.yml"
	cf_ingress_candidate "$d/config.yml" vault.example.com "$svc" "$d/cand.yml"
	check "candidate inserts the rule before the catch-all" sh -c "awk '/vault.example.com/{a=NR} /http_status:404/{b=NR} END{exit !(a && b && a < b)}' '$d/cand.yml'"
	check "candidate keeps the other rules and comments" sh -c "grep -q other.example.com '$d/cand.yml' && grep -q 'keep this comment' '$d/cand.yml'"
	printf 'ingress:\n  - hostname: a.example.com\n    service: http://x\n' >"$d/nocatch.yml"
	check "no catch-all: refused" sh -c "! (VAULTDOCK_INSTALL_LIB=1 VAULTDOCK_DIR='$DIR' bash -c '. \"$ROOT/install.sh\"; cf_ingress_candidate \"$d/nocatch.yml\" v.example.com x \"$d/o.yml\"')"
	printf 'defaults: &d\n  x: 1\ningress:\n  - service: http_status:404\n' >"$d/anchor.yml"
	check "YAML anchors: refused" sh -c "! (VAULTDOCK_INSTALL_LIB=1 VAULTDOCK_DIR='$DIR' bash -c '. \"$ROOT/install.sh\"; cf_ingress_candidate \"$d/anchor.yml\" v.example.com x \"$d/o.yml\"')"

	# Stub cloudflared: validates, and "ingress rule" prints the matching service.
	cat >"$d/bin/cloudflared" <<'EOF'
#!/bin/sh
cfg=$3
[ -n "${CF_STUB_FAIL:-}" ] && exit 1
case "$*" in
*"ingress validate"*) exit 0 ;;
*"ingress rule"*) host=${6#https://}; grep -A1 "hostname: $host" "$cfg" | sed -n 's/.*service: //p'; exit 0 ;;
esac
exit 1
EOF
	chmod 755 "$d/bin/cloudflared"
	CF=([instance.1.kind]=systemd [instance.1.id]=cloudflared.service [instance.1.config]="$d/config.yml" [instance.1.has_vaultdock_rule]=no [instance.1.tunnel_named]=yes)
	cf_restart() { echo RESTARTED >"$d/restarted"; }
	CF_STUB_FAIL=1 PATH="$d/bin:$PATH" cf_edit_ingress 1 vault.example.com "$svc" >"$d/fail.log" 2>&1
	check "rejected candidate: live config unchanged" same "$d/config.yml" "$d/orig.yml"
	check "rejected candidate: removed" [ -z "$(find "$d" -name '.vaultdock-candidate-*')" ]
	PATH="$d/bin:$PATH" cf_edit_ingress 1 vault.example.com "$svc" >"$d/ok.log" 2>&1
	check "validated rule installed" grep -q 'hostname: vault.example.com' "$d/config.yml"
	check "backup in backups/cloudflared-*" [ -n "$(find "$BACKUPS" -path '*cloudflared-*' -name config.yml)" ]
	check "backup next to the config" [ -n "$(find "$d" -name 'config.yml.vaultdock-*')" ]
	check "cloudflared not restarted without confirmation (--yes)" [ ! -e "$d/restarted" ]
	CF=([instance.1.action]=edit-ingress [instance.1.remote_config_seen]=yes [instance.1.kind]=systemd [instance.1.config_readable]=yes)
	check "local config with remote config seen -> dashboard" [ "$(cf_action 1)" = dashboard ]
	CF=([instance.1.action]=edit-ingress [instance.1.kind]=process [instance.1.config_readable]=yes)
	check "plain process -> manual (never restarted)" [ "$(cf_action 1)" = manual ]

	sed -i '/^VAULTDOCK_TRUSTED_PROXIES=/d' "$ENV_FILE"
	trusted_proxies_add 192.168.48.0/20 >/dev/null 2>&1
	check "TRUSTED_PROXIES added when missing" [ "$(envval VAULTDOCK_TRUSTED_PROXIES)" = "127.0.0.1/32,::1/128,192.168.48.0/20" ]
	trusted_proxies_add 10.9.0.0/16 >"$d/tp.log" 2>&1
	check "existing TRUSTED_PROXIES never changed" [ "$(envval VAULTDOCK_TRUSTED_PROXIES)" = "127.0.0.1/32,::1/128,192.168.48.0/20" ]
	check "missing subnet reported instead" grep -q '10.9.0.0/16' "$d/tp.log"
	cf_enable_network cf_tunnel 192.168.48.0/20 >/dev/null 2>&1
	check "cloudflared-net integration enabled" integration_enabled cloudflared-net
	check "VAULTDOCK_TUNNEL_NETWORK set" [ "$(envval VAULTDOCK_TUNNEL_NETWORK)" = cf_tunnel ]
}

ALL="sync fresh upgrade legacy-caddy modified obsolete kek cloudflared"
for s in ${*:-$ALL}; do
	printf '== %s\n' "$s"
	if ! ( "sc_${s//-/_}" ); then fail "scenario $s stopped early"; fi
done
if [ -f "$WORK/.fails" ]; then
	printf '\n%s check(s) FAILED\n' "$(wc -l <"$WORK/.fails" | tr -d ' ')"
	exit 1
fi
printf '\nAll dry-mode checks passed.\n'
