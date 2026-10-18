#!/bin/sh
# Read-only detection of an existing cloudflared on a Linux host (systemd,
# snap, Docker or a plain process). Used by install.sh ("integrations").
#
# Usage:  sh detect.sh [VAULTDOCK_PORT] [VAULTDOCK_COMPOSE_NETWORK]
#         DETECT_CLOUDFLARED_LIB=1 . ./detect.sh; detect_cloudflared 3000
#
# Prints "key=value" lines (schema vaultdock-cloudflared-detect/1): global
# keys schema, vaultdock_port, vaultdock_network, root, binary,
# binary_version, package, systemd, docker, vaultdock_network_exists, count
# (and action=install-outside-compose when count=0), plus per-instance keys
# instance.N.{kind,id,active,enabled,restart,mode,config,config_readable,
# ingress,ingress_hostnames,ingress_catchall,has_vaultdock_rule,
# credentials_file,tunnel_named,remote_config_seen,network,network_mode,
# networks,network_subnet,compose_project,compose_file,image,
# config_in_container,vaultdock_bind,vaultdock_service,action}.
# Run it as root for complete results. It changes nothing: it only runs
# systemctl show/list, docker ps/inspect/logs, journalctl and reads files. Secrets (tunnel tokens,
# credentials) are only tested for presence and are never printed.
#
# POSIX sh: the function body is a subshell so helpers and variables do not
# leak into install.sh, and "local" is not needed.

detect_cloudflared() (
	port=${1:-3000}
	vd_net=${2:-vaultdock_default}
	n=0
	seen_units=""

	# emit KEY VALUE: one line; the value is reduced to a safe character set
	# so the output can be parsed with "IFS== read -r k v".
	emit() {
		printf '%s=%s\n' "$1" "$(printf '%s' "$2" | tr -c 'A-Za-z0-9._:/@,+=-' '_')"
	}
	have() { command -v "$1" >/dev/null 2>&1; }

	# arg_value ARGS FLAG: value of "--flag X" or "--flag=X" in ARGS.
	arg_value() {
		printf '%s\n' "$1" | tr ' ' '\n' | awk -v f="$2" '
			p { print; exit } $0 == f { p = 1; next }
			index($0, f "=") == 1 { print substr($0, length(f) + 2); exit }'
	}

	# describe_config FILE PREFIX: ingress facts of a locally managed config.
	describe_config() {
		emit "$2.config" "$1"
		if [ -z "$1" ]; then
			emit "$2.config_readable" no; return
		fi
		if [ ! -r "$1" ]; then
			if [ -e "$1" ]; then emit "$2.config_readable" denied; else emit "$2.config_readable" missing; fi
			return
		fi
		emit "$2.config_readable" yes
		if grep -q '^[[:space:]]*ingress:' "$1"; then cfg_ingress=yes; else cfg_ingress=no; fi
		emit "$2.ingress" "$cfg_ingress"
		emit "$2.ingress_hostnames" "$(grep -c '^[[:space:]]*-[[:space:]]*hostname:' "$1")"
		# A catch-all is a rule with "service:" and no hostname/path, normally last.
		if awk '/^[[:space:]]*-[[:space:]]*service:/ { c = 1 } END { exit !c }' "$1"; then
			emit "$2.ingress_catchall" yes
		else
			emit "$2.ingress_catchall" no
		fi
		if grep -Eq "(127\.0\.0\.1|localhost|\[::1\]):$port([^0-9]|$)|//vaultdock:8080" "$1"; then
			emit "$2.has_vaultdock_rule" yes
		else
			emit "$2.has_vaultdock_rule" no
		fi
		if grep -q '^[[:space:]]*credentials-file:' "$1"; then emit "$2.credentials_file" yes; else emit "$2.credentials_file" no; fi
		if grep -q '^[[:space:]]*tunnel:' "$1"; then emit "$2.tunnel_named" yes; else emit "$2.tunnel_named" no; fi
	}

	# classify ARGS TOKENFLAG CONFIGFILE -> sets mode.
	#   token  remotely managed (dashboard): --token/--token-file/TUNNEL_TOKEN
	#   local  locally managed: config.yml with ingress (or --url on a named tunnel)
	#   quick  "cloudflared tunnel --url X" without a named tunnel (TryCloudflare)
	#   unknown anything else (for example a config we cannot read)
	classify() {
		mode=unknown
		case " $1 " in
		*" --token "* | *" --token="* | *" --token-file "* | *" --token-file="*) mode=token; return ;;
		esac
		if [ "$2" = yes ]; then mode=token; return; fi
		if [ -n "$3" ] && [ -r "$3" ] && grep -q '^[[:space:]]*ingress:' "$3"; then mode=local; return; fi
		case " $1 " in
		*" --url "* | *" --url="*)
			if [ -n "$3" ] && [ -r "$3" ] && grep -q '^[[:space:]]*tunnel:' "$3"; then mode=local; else mode=quick; fi
			return ;;
		esac
		if [ -n "$3" ] && [ -r "$3" ] && grep -q '^[[:space:]]*tunnel:' "$3"; then mode=local; fi
	}

	# Default config search order of cloudflared for a root service.
	default_config() {
		for c in /etc/cloudflared/config.yml /etc/cloudflared/config.yaml \
			/usr/local/etc/cloudflared/config.yml /root/.cloudflared/config.yml; do
			if [ -e "$c" ]; then printf '%s' "$c"; return; fi
		done
	}

	emit schema vaultdock-cloudflared-detect/1
	emit vaultdock_port "$port"
	emit vaultdock_network "$vd_net"
	if [ "$(id -u 2>/dev/null)" = 0 ]; then emit root yes; else emit root no; fi

	# --- Binary and package ---------------------------------------------------
	bin=$(command -v cloudflared 2>/dev/null || true)
	emit binary "$bin"
	if [ -n "$bin" ]; then
		emit binary_version "$("$bin" --version 2>/dev/null | awk '{ print $3; exit }')"
	fi
	pkg=none
	if have dpkg-query && dpkg-query -W -f='${Status}' cloudflared 2>/dev/null | grep -q 'ok installed'; then
		pkg=deb
	elif have rpm && rpm -q cloudflared >/dev/null 2>&1; then
		pkg=rpm
	elif have snap && snap list cloudflared >/dev/null 2>&1; then
		pkg=snap
	elif have apk && apk info -e cloudflared >/dev/null 2>&1; then
		pkg=apk
	elif [ -n "$bin" ]; then
		pkg=binary
	fi
	emit package "$pkg"

	# --- systemd services (cloudflared*.service, cloudflared@X, snap.cloudflared*) ---
	if have systemctl && [ -d /run/systemd/system ]; then
		emit systemd yes
		units=$( { systemctl list-units --all --type=service --no-legend --plain \
			'cloudflared*' 'snap.cloudflared*' 2>/dev/null
			systemctl list-unit-files --type=service --no-legend \
				'cloudflared*' 'snap.cloudflared*' 2>/dev/null; } |
			awk '{ print $1 }' | grep -v '@\.service$' | sort -u)
		for u in $units; do
			n=$((n + 1)); p="instance.$n"
			seen_units="$seen_units $u"
			case $u in snap.*) emit "$p.kind" snap ;; *) emit "$p.kind" systemd ;; esac
			emit "$p.id" "$u"
			emit "$p.active" "$(systemctl is-active "$u" 2>/dev/null || true)"
			emit "$p.enabled" "$(systemctl is-enabled "$u" 2>/dev/null || true)"
			args=$(systemctl show -p ExecStart --value "$u" 2>/dev/null |
				sed -n 's/.*argv\[\]=\([^;]*\);.*/\1/p' | head -n 1)
			tok=no
			if systemctl show -p Environment --value "$u" 2>/dev/null | grep -q 'TUNNEL_TOKEN'; then tok=yes; fi
			for ef in $(systemctl show -p EnvironmentFiles --value "$u" 2>/dev/null | sed 's/ (ignore_errors=[a-z]*)//g'); do
				if [ -r "$ef" ] && grep -q '^[[:space:]]*\(export[[:space:]]\{1,\}\)\{0,1\}TUNNEL_TOKEN' "$ef"; then tok=yes; fi
			done
			cfg=$(arg_value "$args" --config)
			[ -n "$cfg" ] || cfg=$(default_config)
			classify "$args" "$tok" "$cfg"
			emit "$p.mode" "$mode"
			describe_config "$cfg" "$p"
			if have journalctl && journalctl -u "$u" -n 500 -q --no-pager 2>/dev/null |
				grep -q 'Updated to new configuration'; then
				emit "$p.remote_config_seen" yes
			else
				emit "$p.remote_config_seen" unknown
			fi
			emit "$p.network" host
			emit "$p.vaultdock_bind" 127.0.0.1
			emit "$p.vaultdock_service" "http://127.0.0.1:$port"
			case $mode in
			token) emit "$p.action" dashboard ;;
			local) emit "$p.action" edit-ingress ;;
			*) emit "$p.action" manual ;;
			esac
		done
	else
		emit systemd no
	fi

	# --- Docker containers ----------------------------------------------------
	if ! have docker; then
		emit docker no
	elif ! docker ps -q >/dev/null 2>&1; then
		emit docker denied
	else
		emit docker yes
		emit vaultdock_network_exists "$(docker network inspect "$vd_net" >/dev/null 2>&1 && echo yes || echo no)"
		# Match by image name or by command (custom images that wrap cloudflared).
		ids=$(docker ps -a --no-trunc --format '{{.ID}}|{{.Image}}|{{.Command}}' 2>/dev/null |
			awk -F'|' 'tolower($2 "|" $3) ~ /cloudflared/ { print $1 }')
		for c in $ids; do
			n=$((n + 1)); p="instance.$n"
			emit "$p.kind" docker
			emit "$p.id" "$(docker inspect -f '{{.Name}}' "$c" 2>/dev/null | sed 's#^/##')"
			emit "$p.image" "$(docker inspect -f '{{.Config.Image}}' "$c" 2>/dev/null)"
			emit "$p.active" "$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null)"
			emit "$p.restart" "$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$c" 2>/dev/null)"
			emit "$p.compose_project" "$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$c" 2>/dev/null)"
			emit "$p.compose_file" "$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$c" 2>/dev/null)"
			args=$(docker inspect -f '{{.Path}} {{join .Args " "}}' "$c" 2>/dev/null)
			tok=no
			if docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$c" 2>/dev/null |
				grep -q '^TUNNEL_TOKEN\(_FILE\)\{0,1\}='; then tok=yes; fi
			# Map the in-container config path to the host via the mounts.
			ccfg=$(arg_value "$args" --config)
			[ -n "$ccfg" ] || ccfg=/etc/cloudflared/config.yml
			mounts=$(docker inspect -f '{{range .Mounts}}{{.Source}}|{{.Destination}}{{println}}{{end}}' "$c" 2>/dev/null)
			hcfg=$(printf '%s\n' "$mounts" | awk -F'|' -v f="$ccfg" '
				$2 != "" && (f == $2 || index(f, $2 "/") == 1) && length($2) > best {
					best = length($2); out = $1 substr(f, length($2) + 1) }
				END { print out }')
			emit "$p.config_in_container" "$ccfg"
			classify "$args" "$tok" "$hcfg"
			emit "$p.mode" "$mode"
			describe_config "$hcfg" "$p"
			if docker logs --tail 500 "$c" 2>&1 | grep -q 'Updated to new configuration'; then
				emit "$p.remote_config_seen" yes
			else
				emit "$p.remote_config_seen" unknown
			fi
			netmode=$(docker inspect -f '{{.HostConfig.NetworkMode}}' "$c" 2>/dev/null)
			nets=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}},{{end}}' "$c" 2>/dev/null | sed 's/,$//')
			emit "$p.network_mode" "$netmode"
			emit "$p.networks" "$nets"
			if [ "$netmode" = host ]; then
				emit "$p.network" host
				emit "$p.vaultdock_bind" 127.0.0.1
				emit "$p.vaultdock_service" "http://127.0.0.1:$port"
			else
				# 127.0.0.1 inside a bridge container is the container itself.
				# VaultDock joins a user-defined network of cloudflared instead.
				shared=$(printf '%s\n' "$nets" | tr ',' '\n' | grep -v -x -e bridge -e none -e host | head -n 1)
				if [ -n "$shared" ]; then
					emit "$p.network" "$shared"
				else
					emit "$p.network" bridge-default
				fi
				emit "$p.vaultdock_bind" 127.0.0.1
				emit "$p.vaultdock_service" "http://vaultdock:8080"
				emit "$p.network_subnet" "$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}},{{end}}' "${shared:-bridge}" 2>/dev/null | sed 's/,$//')"
			fi
			case $mode in
			token) emit "$p.action" dashboard ;;
			local) emit "$p.action" edit-ingress ;;
			*) emit "$p.action" manual ;;
			esac
		done
	fi

	# --- Stray processes (nohup, screen, supervisord, other init systems) ------
	if [ -d /proc ]; then
		for d in /proc/[0-9]*; do
			[ -r "$d/cmdline" ] || continue
			cmd=$(tr '\0' ' ' <"$d/cmdline" 2>/dev/null)
			case $cmd in *cloudflared*) ;; *) continue ;; esac
			case $cmd in *detect.sh* | *install.sh* | *grep*) continue ;; esac
			cg=$(cat "$d/cgroup" 2>/dev/null)
			case $cg in *docker* | *containerd* | *libpod* | *kubepods*) continue ;; esac
			known=no
			for u in $seen_units; do
				case $cg in *"$u"*) known=yes ;; esac
			done
			[ "$known" = no ] || continue
			n=$((n + 1)); p="instance.$n"
			emit "$p.kind" process
			emit "$p.id" "${d#/proc/}"
			emit "$p.active" active
			tok=no
			if [ -r "$d/environ" ] && tr '\0' '\n' <"$d/environ" | grep -q '^TUNNEL_TOKEN'; then tok=yes; fi
			cfg=$(arg_value "$cmd" --config)
			classify "$cmd" "$tok" "$cfg"
			emit "$p.mode" "$mode"
			describe_config "$cfg" "$p"
			emit "$p.network" host
			emit "$p.vaultdock_bind" 127.0.0.1
			emit "$p.vaultdock_service" "http://127.0.0.1:$port"
			emit "$p.action" manual
		done
	fi

	emit count "$n"
	if [ "$n" -eq 0 ]; then emit action install-outside-compose; fi
)

if [ "${DETECT_CLOUDFLARED_LIB:-0}" != 1 ]; then
	detect_cloudflared "$@"
fi
