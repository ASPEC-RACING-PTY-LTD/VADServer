#!/bin/sh
# Container-based install.sh scenarios: each scenario runs install.sh for real
# inside a fresh, privileged docker:dind container (its own Docker daemon), so
# the host's containers, images and any real VaultDock install are never
# touched. Needs Docker on the host and permission to run privileged
# containers. The first image build inside each container takes a few minutes.
#
#   sh scripts/test-install/docker.sh [scenario...]
#   scenarios: fresh upgrade legacy-caddy modified obsolete
set -eu

HERE=$(cd -- "$(dirname -- "$0")" && pwd -P)
ROOT=$(cd -- "$HERE/../.." && pwd -P)
DIND_IMAGE=${VAULTDOCK_TEST_DIND_IMAGE:-docker:27.3.1-dind}
FAILED=""

command -v docker >/dev/null 2>&1 || { echo "docker is required (use run.sh --dry without it)" >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo "the Docker daemon is not reachable" >&2; exit 2; }

# The scenario script that runs inside the container (POSIX sh + bash for install.sh).
inner() {
	cat <<'INNER'
set -eu
apk add --no-cache bash curl openssl coreutils tar >/dev/null
i=0; until docker info >/dev/null 2>&1; do i=$((i + 1)); [ $i -lt 60 ] || exit 3; sleep 1; done
export VAULTDOCK_DIR=/root/VaultDock VAULTDOCK_SOURCE=/src VAULTDOCK_YES=1 VAULTDOCK_HTTPS=none \
	VAULTDOCK_STORAGE=/root/VaultDock/data/storage VAULTDOCK_PORT=3000 VAULTDOCK_PUBLIC_URL=http://localhost:3000
APP=$VAULTDOCK_DIR/app ENV=$VAULTDOCK_DIR/app/.env STATE=$VAULTDOCK_DIR/data/state
ok() { echo "    ok:   $*"; }
no() { echo "    FAIL: $*"; touch /tmp/failed; }
t() { d=$1; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else no "$d"; fi; }
val() { grep -E "^$1=" "$ENV" | tail -n1 | cut -d= -f2-; }
healthy() { [ "$(docker inspect -f '{{.State.Health.Status}}' vaultdock 2>/dev/null)" = healthy ]; }
legacy() {
	mkdir -p "$APP/deploy" "$STATE" "$VAULTDOCK_DIR/data/storage"
	cp /src/scripts/test-install/fixtures/legacy-docker-compose.yml "$APP/docker-compose.yml"
	cp /src/scripts/test-install/fixtures/legacy-Caddyfile "$APP/deploy/Caddyfile"
	cp /src/install.sh "$APP/install.sh"
	cat >"$ENV" <<EOF
VAULTDOCK_STATE_PATH=$STATE
VAULTDOCK_STORAGE_PATH=$VAULTDOCK_DIR/data/storage
VAULTDOCK_ENV_FILE=$ENV
VAULTDOCK_UID=65532
VAULTDOCK_GID=65532
VAULTDOCK_BIND=127.0.0.1
VAULTDOCK_PORT=3000
VAULTDOCK_PUBLIC_URL=https://localhost
VAULTDOCK_TRUSTED_PROXIES=172.16.0.0/12
COMPOSE_PROFILES=caddy
VAULTDOCK_DOMAIN=localhost
VAULTDOCK_SETUP_TOKEN=legacy-token
VAULTDOCK_IMAGE=vaultdock:local
EOF
	chown -R 65532:65532 "$STATE" "$VAULTDOCK_DIR/data/storage"
}
case "$1" in
fresh)
	bash /src/install.sh install --yes
	t "VaultDock healthy" healthy
	t "KEK is 32 bytes" sh -c "[ \$(wc -c <'$STATE/keys/kek') = 32 ]"
	t "KEK mode 0600, owned by the container user" sh -c "[ \$(stat -c '%a %u' '$STATE/keys/kek') = '600 65532' ]"
	t "only the vaultdock service runs" sh -c "[ \$(docker ps --format '{{.Names}}' | grep -c .) = 1 ]"
	;;
upgrade)
	bash /src/install.sh install --yes
	sum=$(sha256sum "$STATE/keys/kek"); tok=$(val VAULTDOCK_SETUP_TOKEN)
	echo 'MY_SETTING=1' >>"$ENV"
	VAULTDOCK_REBUILD=0 bash "$APP/install.sh" upgrade --yes
	t "healthy after upgrade" healthy
	t "settings kept" sh -c "[ '$(val VAULTDOCK_SETUP_TOKEN)' = '$tok' ] && grep -q '^MY_SETTING=1' '$ENV'"
	t "KEK unchanged" sh -c "[ \"\$(sha256sum '$STATE/keys/kek')\" = '$sum' ]"
	t "database kept" test -s "$STATE/vaultdock.db"
	;;
legacy-caddy)
	legacy
	bash /src/install.sh upgrade --yes || true
	t "caddy integration enabled" sh -c "grep -q '^VAULTDOCK_INTEGRATIONS=caddy' '$ENV'"
	t "caddy service runs from the integration file" sh -c "docker ps --format '{{.Names}} {{.Label \"com.docker.compose.project.config_files\"}}' | grep caddy | grep -q compose.caddy.yml"
	t "caddy data volume path unchanged" sh -c "docker inspect -f '{{range .Mounts}}{{.Source}} {{end}}' \$(docker ps -qf name=caddy) | grep -q '$STATE/caddy-data'"
	t "COMPOSE_PROFILES kept (no approval)" grep -q '^COMPOSE_PROFILES=' "$ENV"
	t "KEK created" test -s "$STATE/keys/kek"
	;;
modified)
	bash /src/install.sh install --yes
	echo '# local tweak' >>"$APP/docker-compose.yml"
	VAULTDOCK_REBUILD=0 bash "$APP/install.sh" upgrade --yes
	t "healthy" healthy
	t "modified compose kept" grep -q '# local tweak' "$APP/docker-compose.yml"
	t "shipped compose as .new" cmp -s /src/docker-compose.yml "$APP/docker-compose.yml.new"
	;;
obsolete)
	bash /src/install.sh install --yes
	echo 'COMPOSE_PROFILES=caddy' >>"$ENV"
	VAULTDOCK_REBUILD=0 bash "$APP/install.sh" upgrade --yes
	t "kept without approval" grep -q '^COMPOSE_PROFILES=' "$ENV"
	VAULTDOCK_REBUILD=0 bash "$APP/install.sh" upgrade --yes --approve-env-removal COMPOSE_PROFILES
	t "removed with --approve-env-removal" sh -c "! grep -q '^COMPOSE_PROFILES=' '$ENV'"
	t "healthy" healthy
	;;
*) echo "unknown scenario $1"; exit 2 ;;
esac
[ ! -e /tmp/failed ]
INNER
}

run_scenario() {
	name=vd-install-test-$1-$$
	echo "== $1 (container $name)"
	docker run -d --privileged --name "$name" -e DOCKER_TLS_CERTDIR= "$DIND_IMAGE" >/dev/null
	tar -C "$ROOT" --exclude=./.git --exclude=./web/node_modules --exclude=./web/dist -cf - . |
		docker exec -i "$name" sh -c 'mkdir -p /src && tar -xf - -C /src'
	if inner | docker exec -i "$name" sh -s "$1"; then :; else FAILED="$FAILED $1"; fi
	docker rm -f -v "$name" >/dev/null 2>&1 || true
}

for s in ${*:-fresh upgrade legacy-caddy modified obsolete}; do run_scenario "$s"; done
if [ -n "$FAILED" ]; then echo "FAILED:$FAILED"; exit 1; fi
echo "All container scenarios passed."
