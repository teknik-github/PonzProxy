#!/bin/sh
# ponzproxy installer.
#
#   curl -fsSL https://raw.githubusercontent.com/teknik-github/PonzProxy/main/install.sh | sh
#
# Installs Docker Engine if it is missing, then brings ponzproxy up from the
# published image. Re-running it upgrades an existing installation in place
# rather than replacing it, so it is safe to run twice.
#
# Run `sh install.sh --help` for the options, and `--dry-run` to see every
# command it would run without running any of them. Reading a script before
# piping it into a shell is a good habit; this one is written to be read.

set -eu

REPO_RAW="https://raw.githubusercontent.com/teknik-github/PonzProxy/main"

# Defaults, all overridable by flag.
DIR="${PONZ_INSTALL_DIR:-/opt/ponzproxy}"
VERSION=""
HTTP_PORT="80"
HTTPS_PORT="443"
CONSOLE_PORT="8080"
ACME_EMAIL=""
ASSUME_YES="0"
DRY_RUN="0"
SKIP_DOCKER="0"

# --------------------------------------------------------------- output --

# Colour only when stdout is a terminal, so a log file or a CI job does not
# fill up with escape sequences.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	B="$(printf '\033[1m')"; DIM="$(printf '\033[2m')"
	RED="$(printf '\033[31m')"; GRN="$(printf '\033[32m')"
	YEL="$(printf '\033[33m')"; RST="$(printf '\033[0m')"
else
	B=""; DIM=""; RED=""; GRN=""; YEL=""; RST=""
fi

say()  { printf '%s\n' "$*"; }
step() { printf '%s==>%s %s\n' "$B" "$RST" "$*"; }
ok()   { printf '  %s✓%s %s\n' "$GRN" "$RST" "$*"; }
warn() { printf '  %s!%s %s\n' "$YEL" "$RST" "$*" >&2; }
die()  { printf '%serror:%s %s\n' "$RED" "$RST" "$*" >&2; exit 1; }

usage() {
	cat <<EOF
ponzproxy installer

Usage: install.sh [options]

  --dir PATH            where to keep docker-compose.yml and .env
                        (default: $DIR)
  --version TAG         image tag to install, e.g. 0.3.0 (default: latest)
  --http-port PORT      port for proxied HTTP     (default: $HTTP_PORT)
  --https-port PORT     port for proxied HTTPS    (default: $HTTPS_PORT)
  --console-port PORT   port for the console      (default: $CONSOLE_PORT,
                        published on 127.0.0.1 only)
  --acme-email ADDR     where Let's Encrypt sends expiry warnings
  --skip-docker         fail instead of installing Docker when it is missing
  -y, --yes             do not ask anything; assume yes
  -n, --dry-run         print what would happen and change nothing
  -h, --help            this text

Environment: PONZ_INSTALL_DIR sets --dir.
EOF
}

# run executes a command, or prints it under --dry-run. Every side effect in
# this script goes through here, so --dry-run is honest by construction.
run() {
	if [ "$DRY_RUN" = "1" ]; then
		printf '  %s$ %s%s\n' "$DIM" "$*" "$RST"
		return 0
	fi
	"$@"
}

# ask returns true for yes. $2 is what to answer when there is nobody to ask —
# `curl | sh` has the script itself on stdin, and a cron job has no terminal at
# all. Each caller states its own fallback rather than sharing one, because the
# safe answer differs: installing Docker is what this script is for, while
# carrying on past a port clash only swaps a clear error for a confusing one.
ask() {
	_prompt="$1"
	_fallback="${2:-yes}"
	[ "$ASSUME_YES" = "1" ] && return 0

	if [ -t 0 ]; then
		printf '%s [Y/n] ' "$_prompt" >&2
		read -r _reply || _reply=""
	elif [ -r /dev/tty ] && [ -t 1 ]; then
		# Piped from curl, but a human is still watching.
		printf '%s [Y/n] ' "$_prompt" >&2
		read -r _reply </dev/tty || _reply=""
	else
		[ "$_fallback" = "yes" ] && return 0
		return 1
	fi

	case "$_reply" in
		[nN]|[nN][oO]) return 1 ;;
		*) return 0 ;;
	esac
}

have() { command -v "$1" >/dev/null 2>&1; }

# ------------------------------------------------------------ privilege --

SUDO=""
need_root() {
	[ "$(id -u)" = "0" ] && return 0
	have sudo || die "this step needs root, and sudo is not installed. Re-run as root."
	SUDO="sudo"
}

# ----------------------------------------------------------------- args --

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
			--dir)          DIR="${2:?--dir needs a path}"; shift 2 ;;
			--version)      VERSION="${2:?--version needs a tag}"; shift 2 ;;
			--http-port)    HTTP_PORT="${2:?--http-port needs a number}"; shift 2 ;;
			--https-port)   HTTPS_PORT="${2:?--https-port needs a number}"; shift 2 ;;
			--console-port) CONSOLE_PORT="${2:?--console-port needs a number}"; shift 2 ;;
			--acme-email)   ACME_EMAIL="${2:?--acme-email needs an address}"; shift 2 ;;
			--skip-docker)  SKIP_DOCKER="1"; shift ;;
			-y|--yes)       ASSUME_YES="1"; shift ;;
			-n|--dry-run)   DRY_RUN="1"; shift ;;
			-h|--help)      usage; exit 0 ;;
			*)              usage >&2; die "unknown option: $1" ;;
		esac
	done

	for p in "$HTTP_PORT" "$HTTPS_PORT" "$CONSOLE_PORT"; do
		case "$p" in
			''|*[!0-9]*) die "not a port number: $p" ;;
		esac
		[ "$p" -ge 1 ] && [ "$p" -le 65535 ] || die "port out of range: $p"
	done
}

# ------------------------------------------------------------ platform --

check_platform() {
	[ "$(uname -s)" = "Linux" ] || die "this installer is for Linux. On macOS or Windows,
install Docker Desktop and then follow the README's compose instructions."

	case "$(uname -m)" in
		x86_64|amd64|aarch64|arm64) : ;;
		*) die "no published image for $(uname -m); only amd64 and arm64 are built.
Build from source instead: see the README." ;;
	esac
}

# -------------------------------------------------------------- docker --

install_docker() {
	if have docker; then
		ok "Docker is already installed ($(docker --version 2>/dev/null | head -1))"
	else
		[ "$SKIP_DOCKER" = "0" ] || die "Docker is not installed and --skip-docker was given."

		say ""
		say "  Docker Engine is not installed. The installer can do it for you using"
		say "  Docker's own convenience script, ${B}https://get.docker.com${RST}, which adds"
		say "  Docker's package repository and installs from it."
		say ""
		say "  ${DIM}That script is maintained by Docker, not by this project. If you would"
		say "  rather install Docker yourself, answer no and follow"
		say "  https://docs.docker.com/engine/install/ , then run this again.${RST}"
		say ""
		ask "  Install Docker Engine now?" yes \
			|| die "Docker is required. Install it, then run this again."

		need_root
		step "Installing Docker Engine"
		if [ "$DRY_RUN" = "1" ]; then
			run curl -fsSL https://get.docker.com -o /tmp/get-docker.sh
			run $SUDO sh /tmp/get-docker.sh
		else
			_get="$(mktemp)"
			curl -fsSL https://get.docker.com -o "$_get" \
				|| die "could not download the Docker install script"
			$SUDO sh "$_get" || { rm -f "$_get"; die "the Docker install script failed"; }
			rm -f "$_get"
			ok "Docker Engine installed"
		fi
	fi

	# A package install does not always leave the daemon running or enabled.
	if have systemctl && [ "$DRY_RUN" = "0" ]; then
		if ! systemctl is-active --quiet docker 2>/dev/null; then
			need_root
			step "Starting the Docker service"
			run $SUDO systemctl enable --now docker || warn "could not start the docker service"
		fi
	fi

	if [ "$DRY_RUN" = "0" ]; then
		docker info >/dev/null 2>&1 || {
			need_root
			# Without sudo every later docker call would fail the same way, so
			# say why once here rather than at a confusing point later.
			$SUDO docker info >/dev/null 2>&1 \
				|| die "Docker is installed but not responding. Check: systemctl status docker"
			warn "your user cannot talk to Docker; continuing with sudo"
			warn "to fix it permanently: sudo usermod -aG docker $(id -un), then log out and back in"
			DOCKER="$SUDO docker"
		}
	fi

	# Compose v2 is a docker plugin. v1 was a separate python program, is end
	# of life, and does not read this project's compose file the same way.
	if [ "$DRY_RUN" = "0" ]; then
		$DOCKER compose version >/dev/null 2>&1 || die "Docker Compose v2 is missing.
Install the compose plugin: https://docs.docker.com/compose/install/linux/
(if you have the old \`docker-compose\` v1, it is end of life and not supported here)"
		ok "Docker Compose $($DOCKER compose version --short 2>/dev/null)"
	fi
}

# --------------------------------------------------------------- ports --

port_busy() {
	_p="$1"
	if have ss; then
		ss -lnt 2>/dev/null | awk -v p=":$_p\$" '$4 ~ p {found=1} END {exit !found}'
	elif have netstat; then
		netstat -lnt 2>/dev/null | awk -v p=":$_p\$" '$4 ~ p {found=1} END {exit !found}'
	else
		return 1 # cannot tell; let Docker report the clash itself
	fi
}

# owned_ports lists the host ports this installation's own container already
# publishes.
#
# Without it, upgrading an installation reported its own listeners as a clash
# and refused: the ports are "in use" precisely because the thing being
# upgraded is using them. Docker is asked rather than the ports guessed from
# .env, so a container still running an older set of ports is recognised too.
owned_ports() {
	[ "$_upgrade" = "1" ] || return 0
	[ -f "$DIR/docker-compose.yml" ] || return 0

	for _inside in 80 443 8080; do
		_published=$($DOCKER compose --project-directory "$DIR" \
			-f "$DIR/docker-compose.yml" port ponzproxy "$_inside" 2>/dev/null) || continue
		[ -n "$_published" ] || continue
		# "0.0.0.0:8180", or several lines when both stacks are bound.
		# The host port is whatever follows the last colon of each.
		printf '%s\n' "$_published" | while IFS= read -r _line; do
			[ -n "$_line" ] && printf '%s ' "${_line##*:}"
		done
	done
}

check_ports() {
	_owned="$(owned_ports)"
	_clash=""
	for pair in "HTTP:$HTTP_PORT" "HTTPS:$HTTPS_PORT" "console:$CONSOLE_PORT"; do
		_name="${pair%%:*}"; _port="${pair##*:}"
		# A port this installation already holds is not a clash; it is the
		# thing being replaced. Matched in the shell rather than with grep,
		# which this script otherwise does not need.
		case " $_owned " in
			*" $_port "*) continue ;;
		esac
		if port_busy "$_port"; then
			_clash="$_clash  $_name port $_port is already in use\n"
		fi
	done
	[ -z "$_clash" ] && return 0

	printf '%b' "$_clash" >&2
	warn "pass --http-port / --https-port / --console-port to move them"
	warn "note that Let's Encrypt only ever connects to 80 and 443, so moving"
	warn "those means HTTP-01 and TLS-ALPN-01 issuance will not work"
	ask "  Continue anyway?" no \
		|| die "nothing was changed. Free the port, or choose another one."
}

# ------------------------------------------------------------- install --

fetch() {
	_url="$1"; _dest="$2"
	run curl -fsSL "$_url" -o "$_dest" || die "could not download $_url"
}

# env_has reports whether .env already sets a key to exactly this value.
env_has() {
	[ -f "$DIR/.env" ] || return 1
	while IFS= read -r _line || [ -n "$_line" ]; do
		[ "$_line" = "$1=$2" ] && return 0
	done < "$DIR/.env"
	return 1
}

# set_env_key rewrites one key in an existing .env, leaving everything else
# byte for byte as the operator left it.
set_env_key() {
	_env="$DIR/.env"; _key="$1"; _value="$2"
	if [ "$DRY_RUN" = "1" ]; then
		printf '  %s$ set %s=%s in %s%s\n' "$DIM" "$_key" "$_value" "$_env" "$RST"
		return 0
	fi

	_tmp="$_env.tmp.$$"
	# Any existing line for this key is dropped, commented or not, and the
	# new value appended — so a key that was only present as a comment is
	# set rather than duplicated.
	while IFS= read -r _line || [ -n "$_line" ]; do
		case "$_line" in
			"$_key"=*|"#$_key"=*) ;;
			*) printf '%s\n' "$_line" ;;
		esac
	done < "$_env" > "$_tmp"
	printf '%s=%s\n' "$_key" "$_value" >> "$_tmp"
	chmod 600 "$_tmp"
	mv "$_tmp" "$_env"
}

write_env() {
	_env="$DIR/.env"
	if [ -f "$_env" ]; then
		ok "keeping your existing $_env"
		# Except for a version the operator asked for on this run. Silently
		# ignoring --version is how someone upgrades, sees "container is
		# healthy", and walks away still on the old image.
		if [ -n "$VERSION" ] && ! env_has "PONZ_VERSION" "$VERSION"; then
			set_env_key "PONZ_VERSION" "$VERSION"
			ok "pinned PONZ_VERSION=$VERSION"
		fi
		return 0
	fi
	if [ "$DRY_RUN" = "1" ]; then
		printf '  %s$ write %s%s\n' "$DIM" "$_env" "$RST"
		return 0
	fi
	# Written from scratch rather than by editing .env.example, so an upgrade
	# to the example file cannot silently change a running installation.
	{
		echo "# Written by install.sh. Every setting is optional; see .env.example"
		echo "# for the full list with defaults and what each one does."
		[ -n "$VERSION" ] && echo "PONZ_VERSION=$VERSION"
		echo "PONZ_HTTP_PORT=$HTTP_PORT"
		echo "PONZ_HTTPS_PORT=$HTTPS_PORT"
		echo "PONZ_CONSOLE_PORT=$CONSOLE_PORT"
		[ -n "$ACME_EMAIL" ] && echo "PONZ_ACME_EMAIL=$ACME_EMAIL"
		true
	} > "$_env"
	chmod 600 "$_env"
	ok "wrote $_env"
}

compose() { run $DOCKER compose --project-directory "$DIR" -f "$DIR/docker-compose.yml" "$@"; }

# compose_q is the read-only twin: it runs even under --dry-run would-be
# conditions are not involved, and its output is consumed rather than shown.
compose_q() { $DOCKER compose --project-directory "$DIR" -f "$DIR/docker-compose.yml" "$@" 2>/dev/null; }

# wait_healthy polls the container's own healthcheck. It finds the container
# through compose rather than by name, because the compose file deliberately
# does not pin one — see the comment there.
wait_healthy() {
	_n=0
	while [ "$_n" -lt 60 ]; do
		_cid="$(compose_q ps -q ponzproxy | head -1)"
		if [ -n "$_cid" ]; then
			case "$($DOCKER inspect -f '{{.State.Health.Status}}' "$_cid" 2>/dev/null || echo none)" in
				healthy) return 0 ;;
				unhealthy) return 1 ;;
			esac
		fi
		_n=$((_n + 1))
		sleep 1
	done
	return 1
}

# show_password prints the generated admin password, which the container emits
# once when its data volume is new.
#
# The logs are read only from this run ($1 is a timestamp taken just before
# `up -d`). Reading the whole log would reprint the original password on every
# upgrade — stale and wrong the moment the operator has changed it, and it
# would quietly contradict the "shown once" line underneath it.
show_password() {
	[ "$DRY_RUN" = "1" ] && return 0
	_pw="$(compose logs --since "$1" 2>/dev/null | sed -n 's/.*password: *\([^ ]*\).*/\1/p' | tail -1)"
	say ""
	if [ -n "$_pw" ]; then
		say "  ${B}username${RST}  admin"
		say "  ${B}password${RST}  $_pw"
		say ""
		say "  ${DIM}Shown once. Change it after signing in.${RST}"
	else
		say "  No new account was created, so there is no new password to show."
		say "  If you have lost the one you had:"
		say ""
		say "    cd $DIR && $DOCKER compose exec ponzproxy ponzproxy --reset-password admin"
	fi
}

main() {
	parse_args "$@"
	DOCKER="${DOCKER:-docker}"

	say ""
	say "  ${B}ponzproxy${RST} — load balancer and reverse proxy"
	[ "$DRY_RUN" = "1" ] && say "  ${YEL}dry run: nothing will be changed${RST}"
	say ""

	step "Checking the machine"
	check_platform
	ok "Linux $(uname -m)"
	have curl || die "curl is required. Install it, then run this again."

	step "Checking Docker"
	install_docker

	# Known before the port check, which needs it: see owned_ports.
	_upgrade="0"
	[ -f "$DIR/.env" ] && _upgrade="1"

	step "Checking ports"
	check_ports

	step "Installing into $DIR"
	if [ "$DRY_RUN" = "1" ]; then
		[ -d "$DIR" ] || printf '  %s$ mkdir -p %s%s\n' "$DIM" "$DIR" "$RST"
	elif [ ! -d "$DIR" ]; then
		mkdir -p "$DIR" 2>/dev/null || {
			need_root
			$SUDO mkdir -p "$DIR"
			$SUDO chown "$(id -u):$(id -g)" "$DIR"
		}
	fi
	if [ "$DRY_RUN" = "0" ]; then
		[ -w "$DIR" ] || die "$DIR is not writable by this user."
	fi

	fetch "$REPO_RAW/docker-compose.yml" "$DIR/docker-compose.yml"
	fetch "$REPO_RAW/.env.example" "$DIR/.env.example"
	ok "fetched docker-compose.yml and .env.example"
	write_env

	if [ "$_upgrade" = "1" ]; then
		step "Upgrading the running installation"
	else
		step "Starting ponzproxy"
	fi
	# Taken before the container starts, so show_password reads only what this
	# run produced. One second back absorbs any clock skew between the daemon
	# and this shell.
	_since="$(date -u -d '1 second ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null \
		|| date -u +%Y-%m-%dT%H:%M:%SZ)"
	compose pull || die "could not pull the image. Check the machine's network and DNS."
	compose up -d || die "the container did not start. See: cd $DIR && $DOCKER compose logs"

	if [ "$DRY_RUN" = "0" ]; then
		if wait_healthy; then
			ok "container is healthy"
		else
			warn "the container did not report healthy within 60s"
			warn "check it with: cd $DIR && $DOCKER compose logs"
		fi
	fi

	show_password "$_since"
	say ""
	say "  ${B}console${RST}   http://127.0.0.1:$CONSOLE_PORT"
	say "  ${DIM}The console listens on localhost only. Reach it from elsewhere over a"
	say "  VPN, or an SSH tunnel:  ssh -L $CONSOLE_PORT:127.0.0.1:$CONSOLE_PORT $(id -un)@this-machine${RST}"
	say ""
	say "  ${B}files${RST}     $DIR"
	say "  ${B}manage${RST}    cd $DIR && $DOCKER compose {ps,logs,pull,down}"
	say "  ${B}upgrade${RST}   re-run this installer; it keeps your .env and your data"
	say ""
}

main "$@"
