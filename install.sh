#!/usr/bin/env bash
# Installs canopy as a systemd service, optionally behind Caddy (automatic
# HTTPS) and Anubis (bot protection):
#
#     internet -> Caddy :443 -> Anubis 127.0.0.1:8923 -> canopy 127.0.0.1:8080
#
# Two ways to run it:
#
#   On your own machine, aimed at a server over SSH. It asks a few questions,
#   builds canopy for the server, uploads it and runs the install there:
#
#     ./install.sh root@git.example.com
#
#   On the server itself, with flags instead of questions.
#
# Settings come from config/canopy.toml (see config/README.md). Edit it,
# build, then run as root:
#
#     go build
#     sudo ./install.sh --domain git.example.com --anubis
#
# --push-key sets up pushing over SSH, and --harden locks the server down
# (firewall, key-only SSH, automatic security updates). --tailscale moves SSH
# (logins and pushes) onto a tailnet, leaving only the web site public.
#
# Run with --help for all options. Re-running upgrades in place and applies
# any changes made to config/canopy.toml.
set -euo pipefail

if ((BASH_VERSINFO[0] < 4)); then
	echo "install.sh needs bash 4 or newer (on macOS: brew install bash, then run it with that bash)" >&2
	exit 1
fi

# Go for building canopy on the server (--self-update). A newer go line in
# go.mod is fine: this Go fetches that toolchain itself (GOTOOLCHAIN=auto,
# checked against the Go checksum database).
GO_VERSION=1.27.2
declare -A GO_SHA256=(
	[amd64]=ecbadb99091a3f46e31f5f934b068b1864eafa7995211b39eaddf76996045fe5
	[arm64]=94f3e30b8e374bc285e7dadc11e0865726b9bc6e85b841ccceaabc0214c6b7c8
)

ANUBIS_VERSION=1.28.1
declare -A ANUBIS_SHA256=(
	[amd64]=adcb3c2fdfd0fb91cbf7e8376dbcc479f654c85dd3795db3e9a06004786bbdc9
	[arm64]=2eed4f6e40de35554aad6a66dfd017cdc76769c7141af3b0627824c910703060
)

HERE=$(cd "$(dirname "$0")" && pwd)
# The config folder: config/ next to this script, or CANOPY_CONFIG_DIR, e.g.
# one kept in its own repository with a server's settings.
CONFIG_DIR=$(cd "${CANOPY_CONFIG_DIR:-$HERE/config}" && pwd) ||
	{ echo "install.sh: no config folder at ${CANOPY_CONFIG_DIR:-$HERE/config}" >&2; exit 1; }

# Options and their defaults.
SOURCE_CONFIG=$CONFIG_DIR/canopy.toml
DOMAIN=
EMAIL=
CADDY=
ANUBIS=0
CHALLENGE=nojs
BINARY=
PUSH_KEYS=()
GITHUB_USERS=() # GitHub users whose public keys may push too
GH_KEYS=        # temporary file with the keys fetched from GitHub
GITHUB_SYNC=0   # fetch the GitHub keys again every 15 minutes (canopy-github-keys)
DENY_FORCE_PUSH=0
MANAGE_REPOS=0  # let push keys rename and delete repositories
SELF_UPDATE=0   # install canopy-update, which builds the newest release on the server
UPDATE_FROM=https://github.com/spinneydev/canopy
HARDEN=0
SSH_ALLOW=()
TAILSCALE=0
TS_KEY_FILE=    # auth key for joining the tailnet; read only when needed
ADMIN_USER=     # account to log in as instead of root
ADMIN_KEY=      # its SSH keys (default: those of whoever runs this)
ADMIN_GITHUB=   # GitHub user whose SSH keys can log in as it too
ADMIN_PW_HASH=  # file with its password hash, for sudo
UNDO_SSH=0
UNINSTALL=0
DRY_RUN=0
TARGET=         # user@host: deploy to that server over SSH
ASSUME_YES=0    # --yes: accept the saved or default answers without asking
GIVEN=" "       # flags given on the command line, so their questions are skipped

# Install locations.
BIN=/usr/local/bin/canopy
CONFIG=/etc/canopy/canopy.toml
# Checksums of the config files as written, so update.sh can tell edits made
# on the server from its own.
CONFIG_STAMP=/etc/canopy/.uploaded
UNIT=/etc/systemd/system/canopy.service
CACHE=/var/cache/canopy # the unit's CacheDirectory: downloaded [theme] fonts
MIRROR_UNIT=/etc/systemd/system/canopy-mirror.service
MIRROR_TIMER=/etc/systemd/system/canopy-mirror.timer
GH_SYNC_CMD=/usr/local/sbin/canopy-github-keys
GH_SYNC_CONF=/etc/canopy/github-keys.conf # the GitHub users it follows
GH_SYNC_UNIT=/etc/systemd/system/canopy-github-keys.service
GH_SYNC_TIMER=/etc/systemd/system/canopy-github-keys.timer
UPDATE_CMD=/usr/local/sbin/canopy-update
UPDATE_LIB=/usr/local/lib/canopy-update # its build script and Go
UPDATE_STATE=/var/lib/canopy-update     # the source clone and Go caches
CADDY_SNIPPET=/etc/caddy/Caddyfile.d/canopy.caddyfile
CADDY_DROPIN=/etc/systemd/system/caddy.service.d/50-canopy-sandbox.conf
CADDYFILE=/etc/caddy/Caddyfile
ANUBIS_BIN=/usr/local/bin/anubis
ANUBIS_UNIT=/etc/systemd/system/anubis@.service
ANUBIS_ENV=/etc/anubis/canopy.env
ANUBIS_POLICY=/etc/anubis/canopy.botPolicies.yaml
ANUBIS_BIND=127.0.0.1:8923
ANUBIS_METRICS=127.0.0.1:8924
SERVICE_USER=canopy
GIT_USER=git
GIT_KEYS=/etc/ssh/canopy_authorized_keys
SSHD_GIT_CONF=/etc/ssh/sshd_config.d/20-canopy-git.conf
SSHD_HARDEN_CONF=/etc/ssh/sshd_config.d/10-canopy-hardening.conf
# Kernel settings: named to sort after every distribution file (Ubuntu's
# /usr/lib/sysctl.d/99-protect-links.conf among them), as the last one wins.
SYSCTL_HARDEN_CONF=/etc/sysctl.d/99-zz-canopy-hardening.conf
MODPROBE_HARDEN_CONF=/etc/modprobe.d/canopy-hardening.conf
CADDY_ADMIN_SOCKET=/var/lib/caddy/admin.sock
# Tailscale's address ranges (CGNAT IPv4 and its IPv6 prefix).
TS_RANGES="100.64.0.0/10,fd7a:115c:a1e0::/48"

# systemd sandboxing shared by the units this script manages.
SANDBOX="NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
ProcSubset=pid
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
UMask=0077"

usage() {
	cat <<EOF
Usage: ./install.sh USER@HOST [options]    deploy to a server over SSH
       sudo ./install.sh [options]        install on this machine

With USER@HOST (or an ssh config alias, or ssh://USER@HOST:PORT), it runs on
your own machine: asks for the domain and the other choices below (flags
given on the command line answer them), builds canopy for the server,
uploads it with config/ and runs the install there over SSH. Answers are
saved in config/deploy.env and offered as defaults the next time. To keep
a server's config folder elsewhere (its own repository, say), set
CANOPY_CONFIG_DIR to it.

  -y, --yes            take the saved or default answers without asking

Installs canopy as a systemd service, optionally behind Caddy and Anubis.
Site settings (repository folder, title, ...) come from the config file;
see config/README.md.

  --config FILE        config to install (default: config/canopy.toml)
  --domain HOST        public hostname; sets up Caddy with automatic HTTPS
  --email ADDR         email for Let's Encrypt notices (optional)
  --no-caddy           don't set up Caddy even though --domain is given
  --anubis             put Anubis in front of canopy (needs Caddy)
  --challenge KIND     Anubis challenge for browsers:
                         nojs  meta-refresh, no JavaScript needed (default)
                         pow   JavaScript proof-of-work, stronger
  --binary FILE        canopy binary to install (default: ./canopy)
  --self-update        install canopy-update, a root-only command that builds
                       the newest canopy release on the server and switches
                       to it (sudo canopy-update); installs Go $GO_VERSION
  --update-from URL    where canopy-update gets releases (https only;
                       default: $UPDATE_FROM)

Pushing over SSH:
  --push-key FILE      allow pushes from the public key(s) in FILE; may be
                       repeated. Creates a '$GIT_USER' user that can only run
                       git (git-shell). Replaces the previously allowed keys.
                       Pushing to a repository that doesn't exist yet
                       creates it.
  --github-keys USER   also allow pushes from the SSH keys USER has on GitHub
                       (https://github.com/USER.keys); may be repeated. The
                       keys are fetched on every run.
  --github-sync        keep the keys from GitHub (--github-keys and
                       --admin-github) in sync: the server fetches them
                       again every 15 minutes, so a key added to or removed
                       from GitHub counts without running this again
  --deny-force-push    refuse pushes that rewrite or delete branches
  --manage-repos       let push keys rename and delete repositories
                       (ssh $GIT_USER@HOST rename / delete). Off by default:
                       with it, a stolen push key can erase a repository.

Server hardening:
  --harden             firewall that only lets in SSH and the web site,
                       key-only SSH limited to accounts that have keys,
                       automatic security updates, Caddy admin API locked
                       to a root-only socket, and a report of anything else
                       that is listening
  --ssh-allow USER     with --harden: also let USER log in over SSH; may be
                       repeated. The first --harden run allows the accounts
                       that have SSH keys at that point; later runs keep
                       that list and only add accounts named here.
  --tailscale          with --harden: join a tailnet and accept SSH (logins
                       and pushes) only from it; the firewall leaves only
                       the web site open to the internet
  --tailscale-key FILE auth key (tskey-auth-...) to join the tailnet with;
                       needed only while this server isn't in one
  --no-tailscale       keep SSH open to the internet (when deploying, this
                       answers the Tailscale question with no)
  --admin-user NAME    an admin account to log in as instead of root: created
                       if missing, in the sudo group, with the SSH keys of
                       whoever runs this (or --admin-key). With --harden,
                       root logins over SSH are turned off once that
                       account has a key and a password.
  --admin-key FILE     public key(s) for a new --admin-user
  --admin-github USER  also let the SSH keys USER has on GitHub log in as
                       --admin-user (sudo still asks for its password)
  --admin-password-hash FILE
                       crypt hash (openssl passwd -6) to set as the new
                       --admin-user's password, which sudo asks for
  --undo-ssh-hardening remove the SSH hardening and open SSH in the firewall
                       again (the way back in after a lockout)

  --uninstall          remove what this script installed (keeps
                       repositories and $CONFIG)
  --dry-run            print what would be done without changing anything
  -h, --help           show this help

Examples:
  ./install.sh root@git.example.com            # asks, then deploys over SSH
  ./install.sh root@git.example.com --yes      # re-deploy with the saved answers
  sudo ./install.sh --domain git.example.com --email you@example.com --anubis
  sudo ./install.sh --domain git.example.com --anubis \
      --push-key ~/.ssh/id_ed25519.pub --deny-force-push --harden \
      --tailscale --tailscale-key ts-authkey
  sudo ./install.sh                            # canopy only, no Caddy
EOF
}

die() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}
say() { printf '\n==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

# run executes a command, or prints it in dry-run mode.
run() {
	if ((DRY_RUN)); then
		printf '    $ %s\n' "$*"
	else
		"$@"
	fi
}

# write <path> <mode>: write stdin to path, creating parent directories.
# The content goes to a temporary file that is then renamed into place: a
# rename replaces a symbolic link instead of following it, so a link planted
# at path (say by the git user, in the repository folder) can't redirect a
# write made as root.
write() {
	local path=$1 mode=$2 dir tmp
	if ((DRY_RUN)); then
		printf '    write %s (mode %s):\n' "$path" "$mode"
		sed 's/^/      | /'
		return
	fi
	dir=$(dirname "$path")
	mkdir -p "$dir"
	[[ -L $dir ]] && die "refusing to write into $dir: it is a symbolic link"
	tmp=$(mktemp "$dir/.install.XXXXXX")
	cat >"$tmp"
	chmod "$mode" "$tmp"
	mv -fT "$tmp" "$path"
}

# github_keys <user> <file>: append the SSH keys GitHub lists for user
# (https://github.com/USER.keys) to file, each labelled github:USER.
github_keys() {
	local user=$1 file=$2 body line
	[[ $user =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$ ]] ||
		die "--github-keys: '$user' isn't a GitHub user name"
	command -v curl >/dev/null || die "curl is needed to fetch keys from GitHub"
	body=$(curl -fsSL --max-time 20 "https://github.com/$user.keys" | tr -d '\r') ||
		die "--github-keys: couldn't fetch https://github.com/$user.keys (no such user, or no network)"
	body=$(grep -E '^(ssh-|ecdsa-|sk-)' <<<"$body" || true)
	[[ -n $body ]] || die "--github-keys: $user has no SSH keys on GitHub"
	while IFS= read -r line; do
		printf '%s github:%s\n' "$line" "$user"
	done <<<"$body" >>"$file"
	note "$(grep -c . <<<"$body") key(s) from github.com/$user"
}

while (($#)); do
	case $1 in
	--config) SOURCE_CONFIG=${2:?--config needs a file}; shift ;;
	--root | --title) die "$1 is now set in config/canopy.toml (see config/README.md)" ;;
	--domain) DOMAIN=${2:?--domain needs a hostname}; shift ;;
	--email) EMAIL=${2:?--email needs an address}; GIVEN+="email "; shift ;;
	--no-caddy) CADDY=0 ;;
	--anubis) ANUBIS=1; GIVEN+="anubis " ;;
	--challenge) CHALLENGE=${2:?--challenge needs nojs or pow}; GIVEN+="challenge "; shift ;;
	--binary) BINARY=${2:?--binary needs a file}; shift ;;
	--push-key) PUSH_KEYS+=("${2:?--push-key needs a file}"); shift ;;
	--github-keys) GITHUB_USERS+=("${2:?--github-keys needs a GitHub user name}"); GIVEN+="github "; shift ;;
	--github-sync) GITHUB_SYNC=1; GIVEN+="github_sync " ;;
	--deny-force-push) DENY_FORCE_PUSH=1; GIVEN+="deny_force_push " ;;
	--manage-repos) MANAGE_REPOS=1; GIVEN+="manage_repos " ;;
	--self-update) SELF_UPDATE=1; GIVEN+="self_update " ;;
	--update-from) UPDATE_FROM=${2:?--update-from needs a URL}; SELF_UPDATE=1; GIVEN+="self_update update_from "; shift ;;
	--harden) HARDEN=1; GIVEN+="harden " ;;
	--ssh-allow) SSH_ALLOW+=("${2:?--ssh-allow needs a user name}"); shift ;;
	--tailscale) TAILSCALE=1; GIVEN+="tailscale " ;;
	--no-tailscale) TAILSCALE=0; GIVEN+="tailscale " ;;
	--tailscale-key) TS_KEY_FILE=${2:?--tailscale-key needs a file}; TAILSCALE=1; GIVEN+="tailscale "; shift ;;
	--admin-user) ADMIN_USER=${2:?--admin-user needs a user name}; GIVEN+="admin_user "; shift ;;
	--admin-key) ADMIN_KEY=${2:?--admin-key needs a file}; shift ;;
	--admin-github) ADMIN_GITHUB=${2:?--admin-github needs a GitHub user name}; GIVEN+="admin_github "; shift ;;
	--admin-password-hash) ADMIN_PW_HASH=${2:?--admin-password-hash needs a file}; shift ;;
	--undo-ssh-hardening) UNDO_SSH=1 ;;
	--uninstall) UNINSTALL=1 ;;
	--dry-run) DRY_RUN=1 ;;
	-y | --yes) ASSUME_YES=1 ;;
	-h | --help) usage; exit 0 ;;
	-*@*) die "unknown option: $1 (to deploy to a server, leave out the dash: ./install.sh ${1#-})" ;;
	-*) usage >&2; die "unknown option: $1" ;;
	*)
		[[ -z $TARGET ]] || die "only one server at a time ($TARGET and $1)"
		TARGET=$1
		;;
	esac
	shift
done

DEPLOY=0
[[ -n $TARGET ]] && DEPLOY=1

# check_options validates the options (given as flags, or answered when
# deploying over SSH).
check_options() {
[[ -z $CADDY ]] && { [[ -n $DOMAIN ]] && CADDY=1 || CADDY=0; }
[[ $CHALLENGE == nojs || $CHALLENGE == pow ]] || die "--challenge must be nojs or pow"
((CADDY)) && [[ -z $DOMAIN ]] && die "Caddy needs --domain"
((ANUBIS && !CADDY)) && die "--anubis needs Caddy in front of it (use --domain)"
# These end up inside TOML and Caddyfile syntax, so keep them plain.
[[ -z $DOMAIN || $DOMAIN =~ ^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$ ]] ||
	die "--domain should be a bare hostname like git.example.com"
[[ -z $EMAIL || $EMAIL =~ ^[^[:space:]\"{}]+@[A-Za-z0-9.-]+$ ]] || die "--email doesn't look like an email address"
((DENY_FORCE_PUSH && ${#PUSH_KEYS[@]} == 0)) && die "--deny-force-push goes with --push-key"
((MANAGE_REPOS && ${#PUSH_KEYS[@]} == 0)) && die "--manage-repos goes with --push-key"
# Written into canopy-update and handed to git as root's choice: keep it a
# plain https URL (no options, no ext:: or file transports).
[[ $UPDATE_FROM =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?(/[A-Za-z0-9._~/-]*)?$ ]] ||
	die "--update-from must be an https URL like https://git.example.com/canopy"
((${#SSH_ALLOW[@]} && !HARDEN)) && die "--ssh-allow goes with --harden"
# Closing SSH to the internet is done by the hardening's firewall and sshd
# settings, so Tailscale on its own would protect nothing.
((TAILSCALE && !HARDEN)) && die "--tailscale goes with --harden"
if [[ -n $ADMIN_USER ]]; then
	[[ $ADMIN_USER =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || die "--admin-user: '$ADMIN_USER' isn't a valid user name"
	case $ADMIN_USER in root | "$GIT_USER" | "$SERVICE_USER") die "--admin-user can't be $ADMIN_USER" ;; esac
fi
[[ -z $ADMIN_KEY && -z $ADMIN_PW_HASH && -z $ADMIN_GITHUB ]] || [[ -n $ADMIN_USER ]] ||
	die "--admin-key, --admin-github and --admin-password-hash go with --admin-user"
[[ -z $ADMIN_GITHUB || $ADMIN_GITHUB =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$ ]] ||
	die "--admin-github: '$ADMIN_GITHUB' isn't a GitHub user name"
((GITHUB_SYNC && ${#GITHUB_USERS[@]} == 0)) && [[ -z $ADMIN_GITHUB ]] &&
	die "--github-sync goes with --github-keys or --admin-github"
[[ -z $ADMIN_KEY || -r $ADMIN_KEY ]] || die "--admin-key: can't read $ADMIN_KEY"
for u in "${SSH_ALLOW[@]}"; do
	[[ $u =~ ^[a-z_][a-z0-9_.-]*$ ]] || die "--ssh-allow: '$u' isn't a user name"
	((DRY_RUN || DEPLOY)) || id "$u" >/dev/null 2>&1 || die "--ssh-allow: no user '$u'"
done
for f in "${PUSH_KEYS[@]}"; do
	[[ -r $f ]] || die "--push-key: can't read $f"
done
return 0
}

if ((!DEPLOY && ${#GITHUB_USERS[@]})); then
	GH_KEYS=$(mktemp)
	for u in "${GITHUB_USERS[@]}"; do github_keys "$u" "$GH_KEYS"; done
	PUSH_KEYS+=("$GH_KEYS")
fi
((DEPLOY)) || check_options

# ---------------------------------------------------------------------------
# Deploying to a server over SSH (./install.sh user@host)

if ((DEPLOY)); then
	ANSWERS=$CONFIG_DIR/deploy.env
	# Extra ssh options for every connection, e.g. DEPLOY_SSH_OPTS="-F ./ssh_config".
	read -ra ssh_opts <<<"${DEPLOY_SSH_OPTS:-}"

	# ask <var> <question> [default]: read an answer; Enter (or --yes) takes the default.
	ask() {
		local -n out=$1
		local answer=
		if ((ASSUME_YES)); then
			answer=${3:-}
		else
			read -r -p "    $2${3:+ [$3]}: " answer || die "no answer (use --yes to accept the defaults)"
			answer=${answer:-${3:-}}
		fi
		out=$answer
	}
	# ask_yn <var> <question> <default 0|1>: sets var to 1 for yes, 0 for no.
	# shellcheck disable=SC2034 # flag is a reference to the caller's variable
	ask_yn() {
		local -n flag=$1
		local hint=y/N answer
		(($3)) && hint=Y/n
		if ((ASSUME_YES)); then
			flag=$3
			return
		fi
		while :; do
			read -r -p "    $2 [$hint]: " answer || die "no answer (use --yes to accept the defaults)"
			case ${answer,,} in
			"") flag=$3; return ;;
			y | yes) flag=1; return ;;
			n | no) flag=0; return ;;
			esac
			note "please answer y or n"
		done
	}
	given() { [[ $GIVEN == *" $1 "* ]]; }

	# Answers from the last run, offered as defaults.
	declare -A saved=()
	if [[ -f $ANSWERS ]]; then
		while IFS='=' read -r k v; do
			[[ $k =~ ^[a-z_]+$ ]] && saved[$k]=$v
		done <"$ANSWERS"
	fi

	[[ $SOURCE_CONFIG == "$CONFIG_DIR/canopy.toml" ]] ||
		die "--config isn't used when deploying; the config folder is uploaded as it is (set CANOPY_CONFIG_DIR to use another)"
	((UNDO_SSH)) && die "--undo-ssh-hardening runs on the server itself (sudo ~/canopy/install.sh --undo-ssh-hardening)"
	command -v ssh >/dev/null || die "ssh not found"

	# One SSH connection carries the whole run. It also stays open while the
	# hardening is applied, so a failed new login can still be undone.
	ctl=$(mktemp -u "${TMPDIR:-/tmp}/canopy-ssh.XXXXXX")
	stage=
	keys=
	gh=
	push_files=()
	title=
	set_clone=0
	set_about=0
	set_signers=0
	admin_hash=
	proceed=0
	# shellcheck disable=SC2329 # run by the EXIT trap
	cleanup() {
		ssh -o ControlPath="$ctl" -O exit "${ssh_opts[@]}" "$TARGET" 2>/dev/null || true
		[[ -n $GH_KEYS ]] && rm -f "$GH_KEYS"
		[[ -n $stage ]] && rm -rf "$stage"
	}
	trap cleanup EXIT
	say "Connecting to $TARGET"
	ssh -o ControlMaster=yes -o ControlPath="$ctl" -o ControlPersist=yes -fN "${ssh_opts[@]}" "$TARGET" ||
		die "can't connect to $TARGET over SSH; check that 'ssh $TARGET' works"
	# remote [-t] <command>: run a command on the server over that connection.
	remote() {
		local opts=(-o ControlPath="$ctl")
		[[ ${1:-} == -t ]] && { opts+=(-t); shift; }
		# shellcheck disable=SC2029 # the command is meant to run on the server
		ssh "${opts[@]}" "${ssh_opts[@]}" "$TARGET" "$@"
	}
	remote_user=$(remote 'id -un' </dev/null)
	remote_arch=$(remote 'uname -m' </dev/null)
	# shellcheck disable=SC2016 # expanded on the server
	remote_os=$(remote '. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME" || uname -s' </dev/null)
	note "$remote_os, $remote_arch, logged in as $remote_user"
	case $remote_arch in
	x86_64) goarch=amd64 ;;
	aarch64 | arm64) goarch=arm64 ;;
	*) die "no build for $remote_arch servers" ;;
	esac
	sudo=sudo
	[[ $remote_user == root ]] && sudo=
	tty=()
	[[ -t 0 ]] && tty=(-t)

	# The host name in the target is the first guess for the domain.
	host=${TARGET#ssh://}
	host=${host#*@}
	host=${host%%[:/]*}
	guess=${saved[domain]:-}
	[[ -z $guess && $host == *.* && $host != *.ts.net && ! $host =~ ^[0-9.]+$ ]] && guess=$host

	# Tailscale is offered by default when this machine is in a tailnet
	# itself: after the switch, SSH to the server works only from there.
	local_ts=0
	command -v tailscale >/dev/null && tailscale ip -4 >/dev/null 2>&1 && local_ts=1
	remote_ts=$(remote 'tailscale ip -4 2>/dev/null | head -1' </dev/null || true)
	ts_key=

	default_key=
	for k in ~/.ssh/id_ed25519.pub ~/.ssh/id_ecdsa.pub ~/.ssh/id_rsa.pub; do
		[[ -r $k ]] && { default_key=$k; break; }
	done

	if ((!UNINSTALL)); then
		say "Settings (Enter takes the [default])"
		[[ -n $DOMAIN ]] || ask DOMAIN "Domain name for the site" "$guess"
		[[ -n $DOMAIN ]] || die "a domain is needed (its DNS must point at the server)"
		given email || ask EMAIL "Email for Let's Encrypt notices (optional)" "${saved[email]:-}"
		given anubis || ask_yn ANUBIS "Put Anubis bot protection in front" "${saved[anubis]:-1}"
		if ((ANUBIS)) && ! given challenge; then
			ask CHALLENGE "Anubis challenge: nojs (no JavaScript) or pow (proof-of-work)" "${saved[challenge]:-nojs}"
		fi
		if ((${#PUSH_KEYS[@]} == 0)); then
			ask keys "Public key files allowed to push, comma-separated (none: no pushing)" \
				"${saved[push_keys]:-$default_key}"
			if [[ $keys != none ]]; then
				IFS=, read -ra PUSH_KEYS <<<"$keys"
				for i in "${!PUSH_KEYS[@]}"; do
					k=${PUSH_KEYS[i]# }
					PUSH_KEYS[i]=${k/#\~/$HOME}
				done
			fi
		fi
		if ! given github; then
			ask gh "Also allow the SSH keys of GitHub users to push, comma-separated (none: no)" \
				"${saved[github]:-none}"
			[[ $gh == none ]] || read -ra GITHUB_USERS <<<"${gh//,/ }"
		fi
		if ((${#GITHUB_USERS[@]})) && ! given github_sync; then
			ask_yn GITHUB_SYNC "Keep them in sync (the server checks GitHub every 15 minutes, so keys added or removed there count without deploying again)" \
				"${saved[github_sync]:-1}"
		fi
		push_files=("${PUSH_KEYS[@]}")
		if ((${#GITHUB_USERS[@]})); then
			GH_KEYS=$(mktemp)
			for u in "${GITHUB_USERS[@]}"; do github_keys "$u" "$GH_KEYS"; done
			PUSH_KEYS+=("$GH_KEYS")
		fi
		if ((${#PUSH_KEYS[@]})) && ! given deny_force_push; then
			ask_yn DENY_FORCE_PUSH "Refuse pushes that rewrite or delete branches" "${saved[deny_force_push]:-1}"
		fi
		if ((${#PUSH_KEYS[@]})) && ! given manage_repos; then
			ask_yn MANAGE_REPOS "Let push keys rename and delete repositories" "${saved[manage_repos]:-0}"
		fi
		if ! given self_update; then
			ask_yn SELF_UPDATE "Install canopy-update, so the server can update canopy itself (installs Go)" \
				"${saved[self_update]:-0}"
		fi
		if ((SELF_UPDATE)) && ! given update_from; then
			UPDATE_FROM=${saved[update_from]:-$UPDATE_FROM}
		fi
		given harden || ask_yn HARDEN "Harden the server (firewall, key-only SSH, automatic updates)" "${saved[harden]:-1}"
		if ((HARDEN)) && ! given tailscale; then
			ask_yn TAILSCALE "SSH and pushes only over Tailscale (only the web site stays public)" \
				"${saved[tailscale]:-$local_ts}"
		fi
		if ((TAILSCALE)); then
			((local_ts)) || note "warning: this machine doesn't seem to be in a tailnet; afterwards SSH to the server only works from one"
			if [[ -n $remote_ts ]]; then
				note "the server is already in a tailnet ($remote_ts); no key needed"
			elif [[ -n $TS_KEY_FILE ]]; then
				ts_key=$(tr -d '[:space:]' <"$TS_KEY_FILE") || die "--tailscale-key: can't read $TS_KEY_FILE"
			elif ((DRY_RUN)); then
				note "(a real run asks for a Tailscale auth key here)"
			elif [[ -n ${TS_AUTHKEY:-} ]]; then
				ts_key=$TS_AUTHKEY
			elif ((ASSUME_YES)); then
				die "the server needs a Tailscale auth key: pass --tailscale-key FILE or set TS_AUTHKEY"
			else
				note "The server joins your tailnet with an auth key. Make one at"
				note "https://login.tailscale.com/admin/settings/keys (one-off, pre-approved;"
				note "a tagged key keeps the server from dropping off when its key expires)."
				note "It goes to the server only for this run and is never saved."
				read -r -s -p "    Tailscale auth key: " ts_key || die "no answer"
				echo
			fi
			if ((!DRY_RUN)) && [[ -z $remote_ts ]]; then
				case $ts_key in
				tskey-api-*) die "that is an API access token; the server needs an auth key (tskey-auth-...)" ;;
				tskey-auth-* | tskey-client-*) ;;
				*) die "that doesn't look like a Tailscale auth key (tskey-auth-...)" ;;
				esac
				[[ $ts_key =~ ^[A-Za-z0-9_-]+$ ]] || die "the Tailscale key has unexpected characters"
			fi
		fi

		# An admin account to log in as instead of root. Only asked while
		# deploying as root. Deploying as another account makes that one the
		# admin, so that re-runs keep root logins off.
		if ! given admin_user; then
			if [[ $remote_user == root ]]; then
				local_user=$(id -un)
				[[ $local_user =~ ^[a-z_][a-z0-9_-]{0,31}$ && $local_user != root ]] || local_user=none
				ask ADMIN_USER "Admin account to log in as instead of root (none: keep using root)" \
					"${saved[admin_user]:-$local_user}"
				[[ $ADMIN_USER == none ]] && ADMIN_USER=
			else
				ADMIN_USER=$remote_user
			fi
		fi
		if [[ -n $ADMIN_USER ]] && ! given admin_github; then
			ask ADMIN_GITHUB "GitHub user whose SSH keys can also log in as $ADMIN_USER (none: no)" \
				"${saved[admin_github]:-none}"
			[[ $ADMIN_GITHUB == none ]] && ADMIN_GITHUB=
			if [[ -n $ADMIN_GITHUB ]]; then
				# Fetched here too, so a typo stops the run before anything is uploaded.
				gh_check=$(mktemp)
				github_keys "$ADMIN_GITHUB" "$gh_check"
				rm -f "$gh_check"
				if ((${#GITHUB_USERS[@]} == 0)) && ! given github_sync; then
					ask_yn GITHUB_SYNC "Keep them in sync (the server checks GitHub every 15 minutes)" \
						"${saved[github_sync]:-1}"
				fi
			fi
		fi
		admin_exists=0
		if [[ -n $ADMIN_USER ]] && remote "id -u $(printf %q "$ADMIN_USER")" </dev/null >/dev/null 2>&1; then
			admin_exists=1
		fi
		if [[ -n $ADMIN_USER ]] && ((!admin_exists && !DRY_RUN)); then
			# sudo asks for this password. Only its hash leaves this machine.
			pw=
			if ((!ASSUME_YES)); then
				note "$ADMIN_USER gets $([[ -n $ADMIN_KEY ]] && echo "the keys in $ADMIN_KEY" || echo "the SSH keys root logs in with") and a password for sudo."
				while :; do
					read -r -s -p "    Sudo password for $ADMIN_USER: " pw || die "no answer"
					echo
					read -r -s -p "    Again: " pw2 || die "no answer"
					echo
					[[ $pw == "$pw2" ]] && ((${#pw} >= 8)) && break
					note "the passwords differ or are shorter than 8 characters; try again"
				done
			fi
			if [[ -n $pw ]]; then
				command -v openssl >/dev/null || die "openssl is needed to hash the password"
				admin_hash=$(printf '%s' "$pw" | openssl passwd -6 -stdin) ||
					die "openssl couldn't hash the password (on macOS, use Homebrew's openssl)"
			else
				note "no password set (--yes): root logins stay on until $ADMIN_USER has one"
			fi
			unset pw pw2
		fi

		# The site title lives in the config; offer to set it while it's the
		# default. The config is only changed once the summary is confirmed.
		cfg=$CONFIG_DIR/canopy.toml
		if grep -qx 'title = "git"' "$cfg"; then
			ask title "Site title" "$DOMAIN"
			[[ $title != *[\"\\]* && $title != *[[:cntrl:]]* ]] || die "the title can't contain quotes or backslashes"
		fi
		if ! grep -Eq '^[[:space:]]*http[[:space:]]*=' "$cfg" && grep -q '^# http = ' "$cfg"; then
			set_clone=1
		fi
		# The about page's example clone command, while it's the placeholder.
		about=$CONFIG_DIR/about.md
		[[ $DOMAIN != git.example.com ]] && grep -qF 'git.example.com' "$about" 2>/dev/null && set_about=1
		# Signature badges from the same GitHub users' signing keys, while the
		# config doesn't name GitHub users for that itself.
		if ((${#GITHUB_USERS[@]})) && ! grep -Eq '^[[:space:]]*(\[signatures\.github\]|github[[:space:]]*=)' "$cfg"; then
			ask_yn set_signers "Show their commits and tags signed with an SSH signing key they keep on GitHub as verified" \
				"${saved[signers]:-1}"
		fi
	fi
	check_options

	# The installer's flags, in the order they'd be typed.
	args=()
	if ((UNINSTALL)); then
		args=(--uninstall)
	else
		args=(--domain "$DOMAIN")
		[[ -n $EMAIL ]] && args+=(--email "$EMAIL")
		((ANUBIS)) && args+=(--anubis --challenge "$CHALLENGE")
		((${#PUSH_KEYS[@]})) && args+=(--push-key push-keys.pub)
		# In sync, the server fetches them itself from now on.
		if ((GITHUB_SYNC)); then
			for u in "${GITHUB_USERS[@]}"; do args+=(--github-keys "$u"); done
			args+=(--github-sync)
		fi
		((DENY_FORCE_PUSH)) && args+=(--deny-force-push)
		((MANAGE_REPOS)) && args+=(--manage-repos)
		((SELF_UPDATE)) && args+=(--update-from "$UPDATE_FROM")
		((HARDEN)) && args+=(--harden)
		((TAILSCALE)) && args+=(--tailscale)
		[[ -n $ADMIN_USER ]] && args+=(--admin-user "$ADMIN_USER")
		[[ -n $ADMIN_USER && -n $ADMIN_KEY ]] && args+=(--admin-key admin-keys.pub)
		[[ -n $ADMIN_USER && -n $ADMIN_GITHUB ]] && args+=(--admin-github "$ADMIN_GITHUB")
		for u in "${SSH_ALLOW[@]}"; do args+=(--ssh-allow "$u"); done
	fi

	say "About to deploy"
	note "server:  $TARGET ($remote_os)"
	if ((UNINSTALL)); then
		note "action:  uninstall canopy"
	else
		note "site:    https://$DOMAIN$( ((ANUBIS)) && echo " with Anubis ($CHALLENGE)")"
		push_desc=("${push_files[@]}" "${GITHUB_USERS[@]/#/github.com/}")
		note "push:    ${push_desc[*]:-nobody (no --push-key)}$( ((DENY_FORCE_PUSH)) && echo ', no force pushes')$( ((MANAGE_REPOS)) && echo ', rename/delete allowed')"
		((GITHUB_SYNC)) && note "github:  keys kept in sync, checked every 15 minutes"
		note "harden:  $( ((HARDEN)) && echo yes || echo no)"
		((SELF_UPDATE)) && note "update:  sudo canopy-update on the server, releases from $UPDATE_FROM"
		if [[ -n $ADMIN_USER ]]; then
			note "admin:   $ADMIN_USER$( ((admin_exists)) && echo " (exists)" || echo " (new, with $([[ -n $ADMIN_KEY ]] && echo "the keys in $ADMIN_KEY" || echo "root's SSH keys") and a sudo password)")$([[ -n $ADMIN_GITHUB ]] && echo ", also github.com/$ADMIN_GITHUB's keys")$( ((HARDEN)) && echo ", root logins off")"
		fi
		if ((TAILSCALE)); then
			note "ssh:     only over Tailscale$([[ -n $remote_ts ]] && echo " ($remote_ts)" || echo ", joining with an auth key (not saved)")"
		fi
		[[ -n $title ]] && note "config:  title = \"$title\""
		((set_clone)) && note "config:  [clone] http = \"https://$DOMAIN\""
		((set_about)) && note "config:  git.example.com -> $DOMAIN in about.md"
		((set_signers)) && note "config:  [signatures.github] for ${GITHUB_USERS[*]}"
	fi
	note "runs:    ./install.sh ${args[*]}$( ((DRY_RUN)) && echo " --dry-run")"
	ask_yn proceed "Go ahead" 1
	((proceed)) || die "cancelled; nothing was changed"

	if ((!UNINSTALL)); then
		# A dry run changes nothing here either: no config edits, no saved answers.
		if ((DRY_RUN)); then
			[[ -n $title ]] && note "would set title = \"$title\" in config/canopy.toml"
			((set_clone)) && note "would set [clone] http = \"https://$DOMAIN\" in config/canopy.toml"
			((set_about)) && note "would replace git.example.com with $DOMAIN in config/about.md"
			((set_signers)) && note "would add [signatures.github] for ${GITHUB_USERS[*]} to config/canopy.toml"
		fi
		if ((set_signers && !DRY_RUN)); then
			{
				echo
				echo "# Added by install.sh: commits and tags signed with an SSH signing key"
				echo "# these GitHub users keep on GitHub show as verified. canopy fetches the"
				echo "# keys itself, every hour. Badge name = GitHub user."
				echo "[signatures.github]"
				for u in "${GITHUB_USERS[@]}"; do printf '"%s" = "%s"\n' "$u" "$u"; done
			} >>"$cfg"
			note "added [signatures.github] for ${GITHUB_USERS[*]} to config/canopy.toml"
		fi
		if [[ -n $title ]] && ((!DRY_RUN)); then
			sed "s|^title = \"git\"\$|title = \"$title\"|" "$cfg" >"$cfg.tmp" && mv "$cfg.tmp" "$cfg"
			note "set title = \"$title\" in config/canopy.toml"
		fi
		if ((set_clone && !DRY_RUN)); then
			sed "s|^# http = .*|http = \"https://$DOMAIN\"|" "$cfg" >"$cfg.tmp" && mv "$cfg.tmp" "$cfg"
			note "set [clone] http = \"https://$DOMAIN\" in config/canopy.toml"
		fi
		if ((set_about && !DRY_RUN)); then
			sed "s|git\.example\.com|$DOMAIN|g" "$about" >"$about.tmp" && mv "$about.tmp" "$about"
			note "replaced git.example.com with $DOMAIN in config/about.md"
		fi
		if ((!DRY_RUN)); then
			{
				echo "# Answers from the last ./install.sh $TARGET run, offered as defaults next time."
				echo "target=$TARGET"
				echo "domain=$DOMAIN"
				echo "email=$EMAIL"
				echo "anubis=$ANUBIS"
				echo "challenge=$CHALLENGE"
				echo "push_keys=$(IFS=,; echo "${push_files[*]:-none}")"
				echo "github=$(IFS=,; echo "${GITHUB_USERS[*]:-none}")"
				echo "github_sync=$GITHUB_SYNC"
				echo "signers=$set_signers"
				echo "admin_github=${ADMIN_GITHUB:-none}"
				echo "deny_force_push=$DENY_FORCE_PUSH"
				echo "manage_repos=$MANAGE_REPOS"
				echo "self_update=$SELF_UPDATE"
				echo "update_from=$UPDATE_FROM"
				echo "harden=$HARDEN"
				echo "tailscale=$TAILSCALE"
				echo "admin_user=${ADMIN_USER:-none}"
			} >"$ANSWERS"
		fi

		say "Building canopy for linux/$goarch"
		stage=$(mktemp -d)
		if [[ -n $BINARY ]]; then
			cp "$BINARY" "$stage/canopy"
		else
			command -v go >/dev/null || die "go not found; install Go, or pass --binary with a linux/$goarch build"
			(cd "$HERE" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -trimpath -o "$stage/canopy" .)
		fi
		cp "$HERE/install.sh" "$stage/"
		mkdir "$stage/config"
		cp -R "$CONFIG_DIR/." "$stage/config/"
		rm -f "$stage/config/deploy.env"
		if ((${#PUSH_KEYS[@]})); then
			cat "${PUSH_KEYS[@]}" >"$stage/push-keys.pub"
		fi
		# Public keys, so kept with install-command.sh like push-keys.pub.
		[[ -n $ADMIN_USER && -n $ADMIN_KEY ]] && cp "$ADMIN_KEY" "$stage/admin-keys.pub"
		# The auth key travels with this run only: it isn't part of
		# install-command.sh and is deleted on the server afterwards.
		ts_arg=
		if [[ -n $ts_key ]] && ((!DRY_RUN)); then
			(umask 077 && printf '%s\n' "$ts_key" >"$stage/tailscale-authkey")
			ts_arg=" --tailscale-key tailscale-authkey"
		fi
		if [[ -n $admin_hash ]]; then
			(umask 077 && printf '%s\n' "$admin_hash" >"$stage/admin-password.hash")
			ts_arg+=" --admin-password-hash admin-password.hash"
		fi
		# Kept on the server so it can be re-run there with the same flags.
		{
			echo '#!/usr/bin/env bash'
			echo "# Written by install.sh $TARGET; re-run it to apply changes."
			# shellcheck disable=SC2016 # literal text for the generated script
			printf 'cd "$(dirname "$0")" && exec %s./install.sh' "${sudo:+sudo }"
			printf ' %q' "${args[@]}"
			echo ' "$@"'
		} >"$stage/install-command.sh"
		chmod +x "$stage/install-command.sh"

		# A dry run uploads to a temporary folder, so ~/canopy (and its
		# install-command.sh) on the server stay as they are.
		remote_dir=canopy
		if ((DRY_RUN)); then
			remote_dir=$(remote 'mktemp -d' </dev/null)
		fi
		say "Uploading to $TARGET:$remote_dir"
		tar -C "$stage" -czf - . | remote "mkdir -p $remote_dir && tar -C $remote_dir -xzf -" ||
			die "upload failed"
		run_remote="$remote_dir/install-command.sh$ts_arg"
	else
		run_remote="${sudo:+sudo }canopy/install.sh --uninstall"
	fi

	say "Running the installer on $TARGET"
	((DRY_RUN)) && run_remote+=" --dry-run"
	ok=1
	remote "${tty[@]}" "$run_remote" || ok=0
	[[ -n $ts_arg ]] && remote "rm -f $remote_dir/tailscale-authkey $remote_dir/admin-password.hash" </dev/null
	((ok)) || die "the installer stopped with an error on $TARGET (see above)"
	if ((DRY_RUN && !UNINSTALL)); then
		remote "rm -rf $remote_dir" </dev/null
	fi

	# With Tailscale, new SSH connections only work over the tailnet: aim
	# the checks below at the server's tailnet address. That address is
	# authenticated by Tailscale itself, so its host key is accepted when new.
	via=()
	ts_ip=
	ts_host=
	if ((TAILSCALE && !DRY_RUN && !UNINSTALL)); then
		ts_ip=$(remote 'tailscale ip -4 | head -1' </dev/null)
		# shellcheck disable=SC2016 # expanded on the server
		ts_host=$(remote 'tailscale status --json --peers=false |
			awk -F\" '\''/"Self":/ { s = 1 } s && $2 == "DNSName" { sub(/\.$/, "", $4); print $4; exit }'\''' </dev/null || true)
		[[ $ts_ip =~ ^100\.[0-9.]+$ ]] || die "couldn't read the server's Tailscale address"
		ts_host=${ts_host:-$ts_ip}
		via=(-o HostName="$ts_ip" -o StrictHostKeyChecking=accept-new)
	fi

	# with_user <user>: the target, logging in as another user.
	with_user() {
		case $TARGET in
		ssh://*@*) echo "ssh://$1@${TARGET#ssh://*@}" ;;
		ssh://*) echo "ssh://$1@${TARGET#ssh://}" ;;
		*@*) echo "$1@${TARGET#*@}" ;;
		*) echo "$1@$TARGET" ;;
		esac
	}
	# With an admin account, root logins may be off now: check as that account.
	login_target=$TARGET
	[[ -n $ADMIN_USER && $ADMIN_USER != "$remote_user" ]] && login_target=$(with_user "$ADMIN_USER")

	if ((HARDEN && !DRY_RUN && !UNINSTALL)); then
		say "Checking that a new SSH login still works${ts_ip:+ (over Tailscale, $ts_ip)}"
		if ssh -o ControlPath=none -o ConnectTimeout=20 "${via[@]}" "${ssh_opts[@]}" "$login_target" true </dev/null; then
			note "yes: $login_target accepts new logins${ts_ip:+ over Tailscale}"
		else
			note "NO: a new login to $login_target${ts_ip:+ over Tailscale} failed. Undoing the SSH"
			note "hardening over the connection that is still open..."
			remote "${tty[@]}" "${sudo:+sudo }canopy/install.sh --undo-ssh-hardening" ||
				die "couldn't undo it automatically; use your provider's console (see deploy.md)"
			if ssh -o ControlPath=none -o ConnectTimeout=20 "${ssh_opts[@]}" "$TARGET" true </dev/null; then
				if ((TAILSCALE)); then
					die "SSH is open to the internet again and new logins work. Check that this machine is in the server's tailnet (tailscale ping $ts_ip), then run this again."
				fi
				die "the SSH hardening was rolled back and new logins work again. Check that your key works for $TARGET, then run this again."
			fi
			die "the SSH hardening was rolled back, but new logins still fail, so the cause is something else. Don't close other sessions to the server; if you have none, use your provider's console."
		fi
	fi

	if ((${#PUSH_KEYS[@]} && !DRY_RUN && !UNINSTALL)); then
		# Same address and port as the target, as the git user.
		git_target=git@$host
		case $TARGET in ssh://*) git_target=ssh://git@${TARGET#ssh://*@} ;; esac
		[[ $TARGET == *@* ]] || git_target=git@$DOMAIN
		if ssh -o ControlPath=none -o BatchMode=yes -o ConnectTimeout=20 "${via[@]}" "${ssh_opts[@]}" "$git_target" list </dev/null >/dev/null 2>&1; then
			note "pushing works: ssh $git_target list${ts_ip:+ (over Tailscale)}"
		else
			note "note: couldn't reach $git_target with your key yet (try: ssh git@${ts_host:-$host} help)"
		fi
	fi

	say "Done"
	next=$login_target
	login_user=${ADMIN_USER:-$remote_user}
	if [[ -n $ts_host ]]; then
		# The old address no longer answers SSH; the tailnet name does.
		old=$next
		case $old in
		ssh://*@*) next=${old%%@*}@$ts_host; [[ ${old##*@} == *:* ]] && next+=:${old##*:} ;;
		*@*) next=${old%%@*}@$ts_host ;;
		*) next="$old (first point its HostName at $ts_host in ~/.ssh/config)" ;;
		esac
	fi
	if ((!UNINSTALL)); then
		note "site: https://$DOMAIN"
		[[ -n $ADMIN_USER ]] && note "admin: log in as $ADMIN_USER and use sudo$( ((HARDEN)) && echo "; root can't log in over SSH once $ADMIN_USER has a password")"
		if [[ -n $ts_host ]]; then
			note "ssh:  only over Tailscale: ssh $login_user@$ts_host"
			((${#PUSH_KEYS[@]})) && note "push: git remote add origin git@$ts_host:NAME.git  (new names are created on push)"
			note ""
			note "Turn off key expiry for this machine in the Tailscale admin console"
			note "(Machines, then the machine's menu: Disable key expiry), or SSH stops"
			note "working when its key expires. Tagged machines don't expire."
		elif ((${#PUSH_KEYS[@]})); then
			note "push: git remote add origin git@$DOMAIN:NAME.git  (new names are created on push)"
		fi
	fi
	note "deploy again (same answers): ./install.sh $next --yes"
	exit 0
fi

if ((!DRY_RUN)); then
	((EUID == 0)) || die "run as root (sudo ./install.sh ...), or try --dry-run"
	[[ -d /run/systemd/system ]] || die "systemd is required"
fi

# ---------------------------------------------------------------------------
# Helpers

PM=
for candidate in apt-get dnf pacman; do
	if command -v "$candidate" >/dev/null; then
		PM=$candidate
		break
	fi
done

pkg_install() {
	case $PM in
	apt-get)
		run apt-get update -qq
		run env DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends "$@"
		;;
	dnf) run dnf install -y -q --setopt=install_weak_deps=False "$@" ;;
	pacman) run pacman -S --noconfirm --needed "$@" ;;
	*) die "no supported package manager found; install $* yourself and re-run" ;;
	esac
}

need() { # need <command> [package]
	command -v "$1" >/dev/null || pkg_install "${2:-$1}"
}

# wait_http <url> [curl args...]: wait up to 15s for url to answer.
wait_http() {
	((DRY_RUN)) && return 0
	local url=$1
	shift
	for _ in $(seq 30); do
		curl -fsS -o /dev/null "$@" "$url" 2>/dev/null && return 0
		sleep 0.5
	done
	return 1
}

upstream_of() { # turn ":8080" into "127.0.0.1:8080"
	[[ $1 == :* ]] && echo "127.0.0.1$1" || echo "$1"
}

# sshd_settings [connection spec]: the effective sshd settings as sshd -T
# prints them, ignoring the files this script writes, so that re-runs see
# the system's own configuration. The live files are never touched (moving
# them aside would briefly lift the hardening for new connections); sshd -T
# reads a temporary copy of the configuration without them instead. Empty if
# sshd can't be queried (e.g. in a dry run without root).
sshd_settings() {
	local tmp f out
	sshd_privsep
	tmp=$(mktemp -d)
	for f in /etc/ssh/sshd_config.d/*.conf; do
		[[ -e $f && $f != "$SSHD_HARDEN_CONF" && $f != "$SSHD_GIT_CONF" ]] && cp "$f" "$tmp/"
	done
	sed -E "s#^([[:space:]]*Include[[:space:]]+)(/etc/ssh/)?sshd_config\.d/\*\.conf#\1$tmp/*.conf#I" \
		/etc/ssh/sshd_config >"$tmp/sshd_config" 2>/dev/null
	if [[ -n ${1:-} ]]; then
		out=$(sshd -T -f "$tmp/sshd_config" -C "$1" 2>/dev/null || true)
	else
		out=$(sshd -T -f "$tmp/sshd_config" 2>/dev/null || true)
	fi
	rm -rf "$tmp"
	printf '%s\n' "$out"
}

# sshd_apply <files...>: check the sshd configuration and reload sshd. If the
# check fails the given files are removed again, so a broken file can never
# stop sshd from starting (and lock everyone out).
sshd_apply() {
	if ((DRY_RUN)); then
		run sshd -t
		run systemctl reload ssh.service "(or sshd.service)"
		return
	fi
	local out unit
	sshd_privsep
	if ! out=$(sshd -t 2>&1); then
		rm -f "$@"
		printf '%s\n' "$out" >&2
		die "the SSH configuration didn't validate; removed $* again, sshd untouched"
	fi
	for unit in ssh sshd; do
		if systemctl is-active --quiet "$unit.service"; then
			systemctl reload "$unit.service"
			return
		fi
	done
	note "sshd isn't running right now; the settings apply when it starts"
}

# sshd_privsep: sshd -t and -T refuse to run without sshd's privilege
# separation folder. systemd keeps it for ssh.service, and it can be missing
# (seen right after a reload during a run); create it as the unit would.
sshd_privsep() {
	((DRY_RUN)) || [[ -d /run/sshd ]] || install -d -m 0755 /run/sshd
}

is_key_line() { [[ $1 =~ ^(ssh-|ecdsa-|sk-) ]]; }

# remove_self_update: canopy-update, its Go, and its clone and caches (kept
# by systemd under /var/lib/private, as it runs with DynamicUser).
remove_self_update() {
	run rm -rf "$UPDATE_CMD" "$UPDATE_LIB" "$UPDATE_STATE" "/var/lib/private/${UPDATE_STATE##*/}"
}

# firewalld_on / ufw_on: whether that firewall is active.
firewalld_on() { command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; }
ufw_on() { command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; }

# ---------------------------------------------------------------------------
# Undoing the SSH hardening: the way back in after a lockout, run from a
# session that is still open or from the provider's console. The deploy mode
# runs it when a new login fails after hardening.

if ((UNDO_SSH)); then
	say "Undoing the SSH hardening"
	run rm -f "$SSHD_HARDEN_CONF"
	sshd_apply
	ports=$(sshd_settings | awk '$1 == "port" { print $2 }' | sort -un)
	[[ -n $ports ]] || ports=22
	if firewalld_on; then
		zone=public
		((DRY_RUN)) || zone=$(firewall-cmd --get-default-zone)
		for p in $ports; do
			note "opening $p/tcp in firewalld zone $zone"
			run firewall-cmd --quiet --permanent --zone="$zone" --add-port="$p/tcp"
		done
		run firewall-cmd --quiet --reload
	elif ufw_on; then
		for p in $ports; do
			note "opening $p/tcp in ufw"
			run ufw allow "$p/tcp"
		done
	fi
	say "Done: sshd uses the system's own settings again and SSH is open in the firewall."
	note "Fix the problem, then run ~/canopy/install-command.sh again."
	exit 0
fi

# ---------------------------------------------------------------------------
# Uninstall

if ((UNINSTALL)); then
	say "Removing canopy"
	run systemctl disable --now canopy.service 2>/dev/null || true
	run systemctl disable --now canopy-mirror.timer 2>/dev/null || true
	run systemctl disable --now canopy-github-keys.timer 2>/dev/null || true
	run rm -f "$UNIT" "$MIRROR_UNIT" "$MIRROR_TIMER" "$BIN"
	run rm -f "$GH_SYNC_UNIT" "$GH_SYNC_TIMER" "$GH_SYNC_CMD" "$GH_SYNC_CONF"
	run rm -rf "$CACHE"
	if [[ -e $UPDATE_CMD || -e $UPDATE_LIB ]]; then
		say "Removing canopy-update and its Go"
		remove_self_update
	fi
	if [[ -f $ANUBIS_ENV ]]; then
		say "Removing the canopy Anubis instance"
		run systemctl disable --now anubis@canopy.service 2>/dev/null || true
		run rm -f "$ANUBIS_ENV" "$ANUBIS_POLICY"
		if ! compgen -G "/etc/anubis/*.env" >/dev/null; then
			note "no other Anubis instances; removing Anubis"
			run rm -f "$ANUBIS_BIN" "$ANUBIS_UNIT"
		fi
	fi
	if [[ -f $CADDY_SNIPPET ]]; then
		say "Removing the Caddy site"
		run rm -f "$CADDY_SNIPPET"
		run systemctl reload caddy.service 2>/dev/null || true
		note "Caddy itself is still installed"
	fi
	run systemctl daemon-reload
	if id "$SERVICE_USER" >/dev/null 2>&1; then
		run userdel "$SERVICE_USER"
	fi
	say "Done. Kept $CONFIG and the repositories."
	if [[ -f $GIT_KEYS || -f $SSHD_HARDEN_CONF ]]; then
		note "SSH pushing and server hardening were left in place."
	fi
	exit 0
fi

# ---------------------------------------------------------------------------
# canopy

say "Installing canopy"

need git
need curl

if [[ -z $BINARY ]]; then
	BINARY=$HERE/canopy
	[[ -x $BINARY ]] || die "no canopy binary at $BINARY; run 'go build' first or pass --binary"
fi
[[ -x $BINARY ]] || die "$BINARY is not an executable"
note "binary: $BINARY ($(date -r "$BINARY" '+built %Y-%m-%d %H:%M'))"

# Validate the config with the binary being installed, and learn where it
# listens and which folder it serves.
[[ -f $SOURCE_CONFIG ]] || die "no config at $SOURCE_CONFIG (see config/README.md)"
checked=$("$BINARY" -config "$SOURCE_CONFIG" -check 2>/dev/null) ||
	die "$SOURCE_CONFIG is invalid: $("$BINARY" -config "$SOURCE_CONFIG" -check 2>&1 | tail -1)"
setting() { sed -n "s/^$1: //p" <<<"$checked"; }
LISTEN=$(setting listen)
ROOT=$(setting root)
# The file moves to /etc/canopy, so a relative root would point elsewhere.
grep -Eq '^[[:space:]]*root[[:space:]]*=[[:space:]]*"/' "$SOURCE_CONFIG" ||
	die "root in $SOURCE_CONFIG must be an absolute path, like \"/srv/git\""
note "config: $SOURCE_CONFIG (serves $ROOT on $LISTEN)"
if ((CADDY)) && [[ $LISTEN != 127.0.0.1:* && $LISTEN != localhost:* && $LISTEN != "[::1]:"* ]]; then
	note "warning: listen = \"$LISTEN\" also exposes canopy directly, bypassing Caddy;"
	note "         set listen = \"127.0.0.1:${LISTEN##*:}\" in the config to prevent that"
fi

run install -m 0755 "$BINARY" "$BIN"

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
	run useradd --system --no-create-home --home-dir /nonexistent \
		--shell "$(command -v nologin || echo /bin/false)" "$SERVICE_USER"
fi

if [[ ! -d $ROOT ]]; then
	note "creating $ROOT (put bare repositories here)"
	run mkdir -p "$ROOT"
fi

# Files the config refers to (favicon, about page) go next to the installed
# config, keeping their path relative to it.
source_dir=$(cd "$(dirname "$SOURCE_CONFIG")" && pwd)
stamp=$(printf '%s  %s\n' "$(sha256sum <"$SOURCE_CONFIG" | cut -d' ' -f1)" "$CONFIG")
for key in favicon about; do
	file=$(setting "$key")
	[[ -n $file ]] || continue
	case $file in
	"$source_dir"/*)
		rel=${file#"$source_dir"/}
		note "$key: copying $rel"
		write "$(dirname "$CONFIG")/$rel" 0644 <"$file"
		stamp+=$'\n'$(printf '%s  %s' "$(sha256sum <"$file" | cut -d' ' -f1)" "$(dirname "$CONFIG")/$rel")
		;;
	*) note "$key: $file is outside $source_dir; it must exist at that path on this server" ;;
	esac
done

if [[ -f $CONFIG ]] && ! cmp -s "$SOURCE_CONFIG" "$CONFIG"; then
	note "updating $CONFIG (previous version kept as $CONFIG.bak)"
	run cp "$CONFIG" "$CONFIG.bak"
fi
write "$CONFIG" 0644 <"$SOURCE_CONFIG"
write "$CONFIG_STAMP" 0644 <<<"$stamp"

# Ports below 1024 need a capability when canopy serves directly.
port=${LISTEN##*:}
caps=
if [[ $port =~ ^[0-9]+$ ]] && ((port < 1024)); then
	caps=$'AmbientCapabilities=CAP_NET_BIND_SERVICE\nCapabilityBoundingSet=CAP_NET_BIND_SERVICE'
else
	caps='CapabilityBoundingSet='
fi

write "$UNIT" 0644 <<EOF
[Unit]
Description=canopy, a read-only web interface for git repositories
After=network.target

[Service]
User=$SERVICE_USER
Group=$SERVICE_USER
ExecStart=$BIN -config $CONFIG
Restart=on-failure

# canopy only reads repositories; lock down everything else. The one
# folder it writes is $CACHE, for fonts set in [theme].
$caps
CacheDirectory=${CACHE##*/}
ProtectSystem=strict
ProtectHome=read-only
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
$SANDBOX

[Install]
WantedBy=multi-user.target
EOF

run systemctl daemon-reload
run systemctl enable canopy.service
run systemctl restart canopy.service

UPSTREAM=$(upstream_of "$LISTEN")
wait_http "http://$UPSTREAM/" || die "canopy did not start; see: journalctl -u canopy"

if ((!DRY_RUN)) && ! runuser -u "$SERVICE_USER" -- test -r "$ROOT" -a -x "$ROOT"; then
	note "warning: the $SERVICE_USER user cannot read $ROOT; repositories there won't show"
fi

# ---------------------------------------------------------------------------
# Anubis

if ((ANUBIS)); then
	say "Installing Anubis $ANUBIS_VERSION"

	case $(uname -m) in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "no pinned Anubis build for $(uname -m)" ;;
	esac

	installed=$("$ANUBIS_BIN" --version 2>/dev/null || true)
	if [[ $installed == *"$ANUBIS_VERSION"* ]]; then
		note "Anubis $ANUBIS_VERSION is already installed"
	else
		need sha256sum coreutils
		need tar
		tmp=$(mktemp -d)
		trap 'rm -rf "$tmp"' EXIT
		name=anubis-$ANUBIS_VERSION-linux-$arch
		url=https://github.com/TecharoHQ/anubis/releases/download/v$ANUBIS_VERSION/$name.tar.gz
		note "downloading $url"
		run curl -fsSL -o "$tmp/$name.tar.gz" "$url"
		if ((!DRY_RUN)); then
			echo "${ANUBIS_SHA256[$arch]}  $tmp/$name.tar.gz" | sha256sum -c --quiet ||
				die "checksum mismatch for $name.tar.gz; refusing to install"
		fi
		run tar -xzf "$tmp/$name.tar.gz" -C "$tmp"
		run install -m 0755 "$tmp/$name/bin/anubis" "$ANUBIS_BIN"
		# The shipped unit expects /usr/bin/anubis.
		if ((DRY_RUN)); then
			run install -m 0644 "$tmp/$name/run/anubis@.service" "$ANUBIS_UNIT"
		else
			sed "s|/usr/bin/anubis|$ANUBIS_BIN|g" "$tmp/$name/run/anubis@.service" | write "$ANUBIS_UNIT" 0644
		fi
	fi

	# A fixed signing key keeps visitors' passes valid across restarts.
	# A dry run prints the files it would write, so it never reads the key.
	key=
	if ((DRY_RUN)); then
		key='<signing key, not shown>'
	else
		[[ -f $ANUBIS_ENV ]] && key=$(sed -n 's/^ED25519_PRIVATE_KEY_HEX=//p' "$ANUBIS_ENV")
		[[ -n $key ]] || key=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
	fi

	write "$ANUBIS_ENV" 0600 <<EOF
# Anubis instance protecting canopy, written by install.sh.
BIND=$ANUBIS_BIND
BIND_NETWORK=tcp
TARGET=http://$UPSTREAM
POLICY_FNAME=$ANUBIS_POLICY
SERVE_ROBOTS_TXT=false
REDIRECT_DOMAINS=$DOMAIN
ED25519_PRIVATE_KEY_HEX=$key
EOF

	# Browsers weigh 5 with nojs (meta-refresh, no JavaScript) and 10 with
	# pow (JavaScript proof-of-work); see the thresholds below.
	weight=5
	[[ $CHALLENGE == pow ]] && weight=10

	write "$ANUBIS_POLICY" 0644 <<EOF
# Anubis policy for canopy, written by install.sh. Based on the default
# policy shipped with Anubis $ANUBIS_VERSION. Rules are checked in order.
bots:
  # git clone and fetch over HTTP must never see a challenge. (Clients that
  # don't claim to be a browser are let through by the rules below anyway;
  # this keeps the clone endpoints open whatever the user agent.)
  - name: git-smart-http
    path_regex: ^/.+/(info/refs|git-upload-pack)\$
    action: ALLOW

  - import: (data)/bots/_deny-pathological.yaml
  - import: (data)/bots/aggressive-brazilian-scrapers.yaml
  - import: (data)/common/domain-fronting.yaml
  - import: (data)/meta/ai-block-aggressive.yaml
  - import: (data)/crawlers/_allow-good.yaml
  - import: (data)/clients/x-firefox-ai.yaml
  - import: (data)/crawlers/xai.yaml
  - import: (data)/common/keep-internet-working.yaml

  - name: generic-browser
    user_agent_regex: >-
      Mozilla|Opera
    action: WEIGH
    weight:
      adjust: $weight

dnsbl: false

metrics:
  bind: $ANUBIS_METRICS
  network: tcp

store:
  backend: memory
  parameters: {}

thresholds:
  - name: minimal-suspicion
    expression: weight <= 0
    action: ALLOW
  - name: mild-suspicion
    expression:
      all:
        - weight > 0
        - weight < 10
    action: CHALLENGE
    challenge:
      algorithm: metarefresh
      difficulty: 1
  - name: moderate-suspicion
    expression:
      all:
        - weight >= 10
        - weight < 20
    action: CHALLENGE
    challenge:
      algorithm: fast
      difficulty: 2
  - name: mild-proof-of-work
    expression:
      all:
        - weight >= 20
        - weight < 30
    action: CHALLENGE
    challenge:
      algorithm: fast
      difficulty: 4
  - name: extreme-suspicion
    expression: weight >= 30
    action: CHALLENGE
    challenge:
      algorithm: fast
      difficulty: 6
EOF

	write /etc/systemd/system/anubis@canopy.service.d/50-sandbox.conf 0644 <<EOF
# Written by install.sh: sandboxing on top of the unit Anubis ships.
[Service]
ProtectSystem=strict
ProtectHome=yes
CapabilityBoundingSet=
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
$SANDBOX
EOF

	run systemctl daemon-reload
	run systemctl enable anubis@canopy.service
	run systemctl restart anubis@canopy.service
	# Caddy normally sets X-Real-IP; Anubis refuses requests without it.
	wait_http "http://$ANUBIS_BIND/" -A git/install-check -H "X-Real-IP: 127.0.0.1" ||
		die "Anubis did not start; see: journalctl -u anubis@canopy"
fi

# ---------------------------------------------------------------------------
# Caddy

if ((CADDY)); then
	say "Setting up Caddy for $DOMAIN"
	need caddy

	backend=$UPSTREAM
	((ANUBIS)) && backend=$ANUBIS_BIND
	{
		echo "# canopy site, written by install.sh."
		echo "$DOMAIN {"
		[[ -n $EMAIL ]] && echo "	tls $EMAIL"
		# HSTS: browsers that have seen the site keep to HTTPS for a year.
		# Not for localhost names, where it would stick to every local
		# dev server (HSTS is per host name, not per port).
		if [[ $DOMAIN != localhost && $DOMAIN != *.localhost ]]; then
			echo "	header Strict-Transport-Security \"max-age=31536000\""
		fi
		# Security headers for responses that bring none: Anubis's challenge
		# page above all, also redirects and errors. "?" sets each only where
		# it's missing, so canopy's own (stricter) ones win on its pages.
		# One directive each: in a single header block, Caddy adds the "?"
		# defaults only when all of them are missing. Anubis needs its
		# scripts and worker from 'self', inline styles and data: fonts; its
		# one inline script (a retry helper) is blocked without harm.
		echo "	header ?Content-Security-Policy \"default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'\""
		echo "	header ?X-Content-Type-Options nosniff"
		echo "	header ?Referrer-Policy same-origin"
		echo "	header ?X-Frame-Options DENY"
		echo "	encode zstd gzip"
		echo "	reverse_proxy $backend {"
		if ((ANUBIS)); then
			echo "		# Anubis identifies clients by these headers."
			echo "		header_up X-Real-IP {remote_host}"
			echo "		header_up X-Http-Version {http.request.proto}"
			echo "		header_up X-TLS-SNI-Name {http.request.tls.server_name}"
		fi
		echo "	}"
		echo "}"
	} | write "$CADDY_SNIPPET" 0644

	# Make the main Caddyfile load Caddyfile.d/. The stock file (which only
	# serves a welcome page on :80) and one written by an earlier run are
	# replaced; a customised one is kept and the import line is appended.
	#
	# With --harden, Caddy's admin API moves from localhost:2019, where any
	# local process can rewrite Caddy's configuration, to a unix socket
	# that only root and the caddy user can open.
	import_line='import Caddyfile.d/*.caddyfile'
	admin_line="admin unix/$CADDY_ADMIN_SOCKET"
	ours_marker='# Written by install.sh; sites live in Caddyfile.d/.'
	before=$(cat "$CADDYFILE" 2>/dev/null || true)
	stock=0
	caddy_managed=0
	if [[ ! -f $CADDYFILE ]] || grep -qxF "$ours_marker" "$CADDYFILE"; then
		stock=1
	else
		content=$(grep -v '^[[:space:]]*#' "$CADDYFILE" | grep -v '^[[:space:]]*$' || true)
		if grep -q '/usr/share/caddy' <<<"$content" && (($(wc -l <<<"$content") <= 6)); then
			stock=1
			run cp "$CADDYFILE" "$CADDYFILE.orig"
		fi
	fi
	if ((stock)); then
		caddy_managed=1
		{
			echo "$ours_marker"
			if ((HARDEN)); then
				echo "{"
				echo "	# Only root and the caddy user can reach the admin API."
				echo "	$admin_line"
				echo "}"
			fi
			echo "$import_line"
		} | write "$CADDYFILE" 0644
	else
		if ! grep -qF "$import_line" "$CADDYFILE"; then
			note "appending '$import_line' to $CADDYFILE"
			((DRY_RUN)) || printf '\n%s\n' "$import_line" >>"$CADDYFILE"
		fi
		if ((HARDEN)) && ! grep -Eq '^[[:space:]]*admin[[:space:]]' "$CADDYFILE"; then
			note "moving Caddy's admin API to $CADDY_ADMIN_SOCKET in $CADDYFILE"
			first=$(grep -v '^[[:space:]]*#' "$CADDYFILE" | grep -v '^[[:space:]]*$' | head -1)
			if ((!DRY_RUN)); then
				if [[ $first =~ ^[[:space:]]*\{[[:space:]]*$ ]]; then
					# Add to the existing global options block.
					sed -i "0,/^[[:space:]]*{[[:space:]]*\$/s||&\n\t$admin_line|" "$CADDYFILE"
				else
					sed -i "1i {\n\t$admin_line\n}" "$CADDYFILE"
				fi
			fi
		fi
	fi
	# A running Caddy only knows its old admin address, so a change of
	# address needs a restart rather than a reload.
	caddy_action=reload-or-restart
	if [[ $before != "$(cat "$CADDYFILE" 2>/dev/null || true)" ]] && grep -qF "$admin_line" "$CADDYFILE" 2>/dev/null &&
		! grep -qF "$admin_line" <<<"$before"; then
		caddy_action=restart
	fi

	# Sandbox Caddy too, but only when it serves nothing but this site: a
	# customised Caddyfile may serve files from places the sandbox hides.
	if ((HARDEN && caddy_managed)); then
		old=$(cat "$CADDY_DROPIN" 2>/dev/null || true)
		write "$CADDY_DROPIN" 0644 <<EOF
# Written by install.sh --harden: sandboxing on top of the distribution's unit.
[Service]
ProtectSystem=strict
ReadWritePaths=/var/lib/caddy -/var/log/caddy
ProtectHome=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
$SANDBOX
EOF
		if [[ $old != "$(cat "$CADDY_DROPIN" 2>/dev/null || true)" ]]; then
			run systemctl daemon-reload
			caddy_action=restart
		fi
	elif ((HARDEN)); then
		note "not sandboxing Caddy: the Caddyfile has other sites in it"
	fi

	if ((!DRY_RUN)) && ! out=$(caddy validate --config "$CADDYFILE" --adapter caddyfile 2>&1); then
		printf '%s\n' "$out" >&2
		die "the Caddy configuration is invalid; nothing was reloaded"
	fi
	run systemctl enable caddy.service
	run systemctl "$caddy_action" caddy.service

	# Open the firewall for HTTP and HTTPS when one is active.
	if firewalld_on; then
		note "opening http and https in firewalld"
		run firewall-cmd --quiet --permanent --add-service=http --add-service=https
		run firewall-cmd --quiet --reload
	elif ufw_on; then
		note "opening 80 and 443 in ufw"
		run ufw allow 80,443/tcp
	fi
fi

# ---------------------------------------------------------------------------
# Tailscale: SSH moves onto the tailnet (the hardening below closes it to the
# internet), so only the web site stays public.

# install_tailscale: from Tailscale's signed package repository.
install_tailscale() {
	local id codename version tmp
	# shellcheck disable=SC1091 # the server's own file
	id=$(. /etc/os-release && echo "$ID")
	# shellcheck disable=SC1091
	codename=$(. /etc/os-release && echo "${VERSION_CODENAME:-}")
	# shellcheck disable=SC1091
	version=$(. /etc/os-release && echo "${VERSION_ID%%.*}")
	tmp=$(mktemp -d)
	case $PM in
	apt-get)
		[[ $id == debian || $id == ubuntu ]] && [[ -n $codename ]] ||
			die "no Tailscale repository known for $id; install tailscale yourself and re-run"
		local repo=https://pkgs.tailscale.com/stable/$id/$codename
		run curl -fsSL -o "$tmp/key.gpg" "$repo.noarmor.gpg" ||
			die "Tailscale has no packages for $id $codename; install tailscale yourself and re-run"
		run curl -fsSL -o "$tmp/tailscale.list" "$repo.tailscale-keyring.list"
		if ((!DRY_RUN)); then
			write /usr/share/keyrings/tailscale-archive-keyring.gpg 0644 <"$tmp/key.gpg"
			write /etc/apt/sources.list.d/tailscale.list 0644 <"$tmp/tailscale.list"
		fi
		;;
	dnf)
		local repo=https://pkgs.tailscale.com/stable/fedora/tailscale.repo
		[[ $id == fedora ]] || repo=https://pkgs.tailscale.com/stable/rhel/$version/tailscale.repo
		run curl -fsSL -o "$tmp/tailscale.repo" "$repo" ||
			die "Tailscale has no packages for $id $version; install tailscale yourself and re-run"
		((DRY_RUN)) || write /etc/yum.repos.d/tailscale.repo 0644 <"$tmp/tailscale.repo"
		;;
	esac
	rm -rf "$tmp"
	pkg_install tailscale
}

TS_IP=
TS_HOST=
if ((TAILSCALE)); then
	say "Joining the tailnet"
	command -v tailscale >/dev/null || install_tailscale
	run systemctl enable --now tailscaled.service
	if ((!DRY_RUN)) && TS_IP=$(tailscale ip -4 2>/dev/null | head -1) && [[ -n $TS_IP ]]; then
		note "already in a tailnet"
	elif ((DRY_RUN)); then
		# A dry run never reads the key.
		run tailscale up --auth-key="file:${TS_KEY_FILE:-<auth key file>}"
	else
		[[ -n $TS_KEY_FILE ]] ||
			die "this server isn't in a tailnet yet; pass --tailscale-key FILE with an auth key from https://login.tailscale.com/admin/settings/keys"
		[[ -r $TS_KEY_FILE ]] || die "--tailscale-key: can't read $TS_KEY_FILE"
		# file: keeps the key out of the process list.
		tailscale up --auth-key="file:$(realpath "$TS_KEY_FILE")" ||
			die "tailscale up failed; nothing about SSH was changed"
		TS_IP=$(tailscale ip -4 | head -1)
	fi
	if ((!DRY_RUN)); then
		[[ $TS_IP =~ ^100\. ]] || die "no Tailscale address after joining; nothing about SSH was changed"
		TS_HOST=$(tailscale status --json --peers=false |
			awk -F'"' '/"Self":/ { s = 1 } s && $2 == "DNSName" { sub(/\.$/, "", $4); print $4; exit }')
		TS_HOST=${TS_HOST:-$TS_IP}
		note "this server is $TS_HOST ($TS_IP) in the tailnet"
	fi
fi

# ---------------------------------------------------------------------------
# Pushing over SSH

GIT_SHELL=$(command -v git-shell || true)
if ((${#PUSH_KEYS[@]})); then
	say "Setting up pushes over SSH"
	[[ -n $GIT_SHELL ]] || die "git-shell not found; it normally comes with git"
	PUSH_HOST=${TS_HOST:-${DOMAIN:-$(hostname -f 2>/dev/null || hostname)}}

	if id "$GIT_USER" >/dev/null 2>&1; then
		IFS=: read -r _ _ _ _ _ home shell < <(getent passwd "$GIT_USER")
		[[ $shell == "$GIT_SHELL" && $home == "$ROOT" ]] ||
			die "a '$GIT_USER' user already exists (home $home, shell $shell); not taking it over"
	else
		run useradd --system --user-group --home-dir "$ROOT" --no-create-home \
			--shell "$GIT_SHELL" "$GIT_USER"
		# No password at all, rather than a locked account: some sshd
		# setups refuse key logins to locked accounts.
		run usermod -p '*' "$GIT_USER"
	fi
	if ! grep -qxF "$GIT_SHELL" /etc/shells 2>/dev/null; then
		if ((DRY_RUN)); then
			run tee -a /etc/shells
		else
			echo "$GIT_SHELL" >>/etc/shells
		fi
	fi

	# The allowed keys, each restricted to running git: no terminal, no
	# port, agent or X11 forwarding. The file is root's, so the git user
	# can't add keys to it. A key given twice (say in a file and on GitHub)
	# is listed once, whatever its comment.
	keys=
	declare -A seen_keys=()
	for f in "${PUSH_KEYS[@]}"; do
		while IFS= read -r line || [[ -n $line ]]; do
			line=${line%$'\r'}
			[[ -z $line || $line == \#* ]] && continue
			is_key_line "$line" || die "$f: expected a public key (ssh-ed25519 AAAA...), got: ${line:0:30}..."
			ssh-keygen -l -f /dev/stdin <<<"$line" >/dev/null 2>&1 || die "$f: not a valid public key: ${line:0:30}..."
			read -r type blob _ <<<"$line"
			[[ -n ${seen_keys[$type $blob]:-} ]] && continue
			seen_keys[$type $blob]=1
			keys+="restrict $line"$'\n'
		done <"$f"
	done
	[[ -n $keys ]] || die "no public keys found in ${PUSH_KEYS[*]}"
	note "$(grep -c . <<<"$keys") key(s) allowed to push"
	printf '%s' "$keys" | write "$GIT_KEYS" 0644
	[[ -n $GH_KEYS ]] && rm -f "$GH_KEYS"

	# Repositories belong to the git user so pushes can write to them. The
	# folder itself stays root's, group-writable for git (so "create" works)
	# and sticky, so git can't remove or rename the root-owned config and
	# helper commands kept in it.
	run chown "root:$GIT_USER" "$ROOT"
	run chmod 1775 "$ROOT"
	if ((!DRY_RUN)); then
		changed=$(find "$ROOT" -mindepth 1 \( -path "$ROOT/git-shell-commands" -o -path "$ROOT/.gitconfig" \) -prune \
			-o ! -user "$GIT_USER" -print | wc -l)
		if ((changed)); then
			note "giving $changed existing file(s) under $ROOT to $GIT_USER"
			find "$ROOT" -mindepth 1 \( -path "$ROOT/git-shell-commands" -o -path "$ROOT/.gitconfig" \) -prune \
				-o ! -user "$GIT_USER" -print0 | xargs -0r chown -h "$GIT_USER:$GIT_USER"
		fi
	fi

	{
		echo "# Written by install.sh: settings for pushes as $GIT_USER."
		echo "[receive]"
		echo "	# Reject corrupt or malformed objects."
		echo "	fsckObjects = true"
		if ((DENY_FORCE_PUSH)); then
			echo "	# Pushes can't rewrite or delete history (install.sh --deny-force-push)."
			echo "	denyNonFastForwards = true"
			echo "	denyDeletes = true"
		fi
	} | write "$ROOT/.gitconfig" 0644

	# Commands for "ssh git@host <command>". git-shell runs these from the
	# git user's home, which is the repository folder.
	cmds=$ROOT/git-shell-commands
	# Start over if the folder was replaced by a link or isn't root's.
	if [[ -L $cmds ]] || { [[ -e $cmds ]] && [[ $(stat -c %u "$cmds") != 0 ]]; }; then
		note "replacing $cmds (it was a link or not owned by root)"
		run rm -rf -- "$cmds"
	fi
	run install -d -o root -g root -m 0755 "$cmds"
	manage_help=
	if ((MANAGE_REPOS)); then
		manage_help="
  ssh $GIT_USER@$PUSH_HOST rename OLD NEW                          rename a repository
  ssh $GIT_USER@$PUSH_HOST delete NAME --yes                       delete one (no undo)"
	fi
	write "$cmds/help" 0755 <<EOF
#!/bin/sh
cat <<'HELP'
This account only runs git. Push and fetch as usual; pushing to a name
that doesn't exist yet creates the repository:

  git remote add origin $GIT_USER@$PUSH_HOST:NAME.git
  git push -u origin main

Commands:

  ssh $GIT_USER@$PUSH_HOST create NAME [--private] [description]   make an empty repository
  ssh $GIT_USER@$PUSH_HOST list                                    list repositories
  ssh $GIT_USER@$PUSH_HOST edit-desc NAME [text]                   show or set a description
  ssh $GIT_USER@$PUSH_HOST private NAME [on|off]                   keep one off the web site$manage_help

A private repository isn't listed, shown or clonable over HTTPS; over SSH
it works as usual.
HELP
EOF
	# Shared by the commands below that take repository names. The
	# character check comes first: grep works line by line, so the pattern
	# alone would let a newline through.
	name_sh=$(
		cat <<'EOF'
# check_name NAME: exit unless NAME is a valid repository name: letters,
# digits, . _ - and up to two folders, no "..", and only the last part may
# start with a dot (canopy doesn't look inside hidden folders).
check_name() {
	case $1 in
	'' | *[!A-Za-z0-9._/-]*)
		echo "${0##*/}: '$1' isn't a valid name (letters, digits, . _ - and /)" >&2
		exit 1
		;;
	esac
	if ! printf '%s\n' "$1" | grep -Eqx '([A-Za-z0-9][A-Za-z0-9._-]*/){0,2}\.?[A-Za-z0-9][A-Za-z0-9._-]*' ||
		printf '%s' "$1" | grep -qF '..'; then
		echo "${0##*/}: '$1' isn't a valid name (letters, digits, . _ - and up to two folders)" >&2
		exit 1
	fi
}
# repo_dir NAME: set dir to the bare repository NAME.git, or exit if there
# is none.
repo_dir() {
	check_name "$1"
	dir=$HOME/$1.git
	if [ -L "$dir" ] || [ ! -f "$dir/HEAD" ] || [ ! -d "$dir/objects" ]; then
		echo "${0##*/}: no repository $1" >&2
		exit 1
	fi
}
# not_mirror NAME: exit if NAME (in dir) is a mirror. Those are managed by
# [mirrors] in the config, which would bring them straight back.
not_mirror() {
	if git config --file "$dir/config" --get remote.origin.url >/dev/null; then
		echo "${0##*/}: $1 is a mirror; change [mirrors] in the config instead" >&2
		exit 1
	fi
}
# prune_parents NAME: remove the folders above NAME.git that are now empty.
prune_parents() {
	parent=${1%/*}
	while [ "$parent" != "$1" ] && rmdir "$HOME/$parent" 2>/dev/null; do
		case $parent in */*) parent=${parent%/*} ;; *) break ;; esac
	done
}
EOF
	)
	# sshd runs this for every login to the git account (ForceCommand
	# below), through git-shell, which only runs commands from this folder.
	write "$cmds/serve" 0755 <<EOF
#!/bin/sh
# serve: a push to a repository that doesn't exist yet creates it (through
# "create", which checks the name); everything else goes to git-shell as if
# sshd had run it directly.
cmd=\${SSH_ORIGINAL_COMMAND:-}
case \$cmd in
"") exec "\$HOME/git-shell-commands/no-interactive-login" ;;
serve | "serve "*)
	# git-shell would run this script again with the same command.
	echo "unrecognized command 'serve'" >&2
	exit 128
	;;
"git-receive-pack '"*"'" | "git receive-pack '"*"'")
	name=\${cmd#*\\'}
	name=\${name%\\'}
	name=\${name#\\~/}
	name=\${name%/}
	name=\${name%.git}
	if [ ! -e "\$HOME/\$name.git" ] && [ ! -e "\$HOME/\$name" ]; then
		# stdout carries the git protocol, so messages go to stderr.
		"\$HOME/git-shell-commands/create" "\$name" >/dev/null || exit 128
		echo "created repository \$name" >&2
	fi
	;;
esac
exec $GIT_SHELL -c "\$cmd"
EOF
	write "$cmds/no-interactive-login" 0755 <<'EOF'
#!/bin/sh
"$HOME/git-shell-commands/help"
exit 128
EOF
	write "$cmds/list" 0755 <<'EOF'
#!/bin/sh
# list: the repositories under this account's home, private ones marked.
cd "$HOME" || exit 1
find . -mindepth 1 -maxdepth 3 -type d -name '*.git' -prune | sed 's|^\./||; s|\.git$||' | sort |
	while IFS= read -r name; do
		if git config --file "$name.git/config" --type=bool --get-regexp '^canopy\.private$' 2>/dev/null | grep -q ' true$'; then
			echo "$name (private)"
		else
			echo "$name"
		fi
	done
EOF
	write "$cmds/create" 0755 <<EOF
#!/bin/sh
# create NAME [--private] [description...]: make an empty bare repository to
# push to. --private keeps it off the web site from the start.
set -eu
$name_sh
private=
if [ "\${1:-}" = --private ]; then
	private=1
	shift
fi
if [ \$# -lt 1 ]; then
	echo "usage: create NAME [--private] [description...]" >&2
	exit 1
fi
name=\${1%.git}
shift
if [ "\${1:-}" = --private ]; then
	private=1
	shift
fi
check_name "\$name"
dir=\$HOME/\$name.git
if [ -e "\$dir" ]; then
	echo "create: \$name already exists" >&2
	exit 1
fi
mkdir -p "\$(dirname "\$dir")"
git init --quiet --bare --initial-branch=main "\$dir"
if [ -n "\$private" ]; then
	git config --file "\$dir/config" canopy.private true
fi
if [ \$# -gt 0 ]; then
	printf '%s\n' "\$*" >"\$dir/description"
fi
# The first push decides the default branch, so a project whose branch is
# "master" doesn't show up empty.
cat >"\$dir/hooks/post-receive" <<'HOOK'
#!/bin/sh
# Written by create: while HEAD names a branch that doesn't exist, point it
# at the first branch pushed.
git rev-parse -q --verify HEAD >/dev/null && exit 0
while read -r _ _ ref; do
	case \$ref in refs/heads/*) exec git symbolic-ref HEAD "\$ref" ;; esac
done
HOOK
chmod 0755 "\$dir/hooks/post-receive"
if [ -n "\$private" ]; then
	echo "created \$name, private: not on the web site (\"private \$name off\" changes that). Push to it with:"
else
	echo "created \$name. Push to it with:"
fi
echo "  git remote add origin $GIT_USER@$PUSH_HOST:\$name.git"
echo "  git push -u origin main"
EOF
	# The start of the commands below: the shared functions, and the address
	# they print in their hints.
	cmd_head=$(printf '#!/bin/sh\nset -eu\n%s\npush_url=%s\n' "$name_sh" "'$GIT_USER@$PUSH_HOST'")
	{
		echo "$cmd_head"
		cat <<'EOF'
# edit-desc NAME [text...]: show a repository's description, or set it. One
# set in the config ([repos."NAME"] description) wins on the web site.
if [ $# -lt 1 ]; then
	echo "usage: edit-desc NAME [text...]" >&2
	exit 1
fi
name=${1%.git}
shift
repo_dir "$name"
if [ $# -eq 0 ]; then
	cat "$dir/description" 2>/dev/null || true
	exit 0
fi
# One line: the index and the header show it as a tagline.
printf '%s\n' "$(printf '%s' "$*" | tr '\r\n' '  ')" >"$dir/description"
echo "description of $name set"
EOF
	} | write "$cmds/edit-desc" 0755
	{
		echo "$cmd_head"
		cat <<'EOF'
# private NAME [on|off]: show whether a repository is private, or set it. A
# private one is left off the web site: not listed, not shown, not clonable
# over HTTPS (canopy.private in its config; canopy notices within 15 s). Over SSH it works as before.
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
	echo "usage: private NAME [on|off]" >&2
	exit 1
fi
name=${1%.git}
repo_dir "$name"
case ${2:-} in
"")
	# As canopy reads it: anything but unset or false counts as private.
	st=0
	values=$(git config --file "$dir/config" --type=bool --get-regexp '^canopy\.private$' 2>/dev/null) || st=$?
	if [ "$st" -eq 1 ] || { [ "$st" -eq 0 ] && ! printf '%s\n' "$values" | grep -qv ' false$'; }; then
		echo "$name is public"
	else
		echo "$name is private"
	fi
	;;
on)
	git config --file "$dir/config" canopy.private true
	echo "$name is private: gone from the web site within 15 seconds"
	;;
off)
	git config --file "$dir/config" --unset-all canopy.private 2>/dev/null || true
	echo "$name is public: on the web site within 15 seconds (unless hidden in its config)"
	;;
*)
	echo "usage: private NAME [on|off]" >&2
	exit 1
	;;
esac
EOF
	} | write "$cmds/private" 0755
	if ((MANAGE_REPOS)); then
		{
			echo "$cmd_head"
			cat <<'EOF'
# rename OLD NEW: give a repository a new name. Its pages and clone URL
# move with it; settings under [repos."OLD"] in the config don't.
if [ $# -ne 2 ]; then
	echo "usage: rename OLD NEW" >&2
	exit 1
fi
old=${1%.git}
new=${2%.git}
repo_dir "$old"
not_mirror "$old"
check_name "$new"
to=$HOME/$new.git
if [ -e "$to" ] || [ -L "$to" ]; then
	echo "rename: $new already exists" >&2
	exit 1
fi
mkdir -p "$(dirname "$to")"
mv -T "$dir" "$to"
prune_parents "$old"
echo "renamed $old to $new. Point existing clones at it with:"
echo "  git remote set-url origin $push_url:$new.git"
EOF
		} | write "$cmds/rename" 0755
		{
			echo "$cmd_head"
			cat <<'EOF'
# delete NAME --yes: remove a repository and its history for good.
if [ $# -lt 1 ] || [ $# -gt 2 ]; then
	echo "usage: delete NAME --yes" >&2
	exit 1
fi
name=${1%.git}
repo_dir "$name"
not_mirror "$name"
if [ "${2:-}" != --yes ]; then
	echo "delete: this removes $name and all its history, with no undo ($(du -sh "$dir" | cut -f1))." >&2
	echo "To go ahead: ssh $push_url delete $name --yes" >&2
	exit 1
fi
rm -rf -- "$dir"
prune_parents "$name"
echo "deleted $name"
EOF
		} | write "$cmds/delete" 0755
	else
		# Still there when switched off, so the refusal says why.
		for c in rename delete; do
			printf '#!/bin/sh\necho "%s: not allowed on this server (install.sh --manage-repos turns it on); ask its admin" >&2\nexit 1\n' "$c" |
				write "$cmds/$c" 0755
		done
	fi

	write "$SSHD_GIT_CONF" 0644 <<EOF
# Written by install.sh: the $GIT_USER account can only run git (its login
# shell is git-shell) and only with the keys in $GIT_KEYS.
Match User $GIT_USER
	AuthorizedKeysFile $GIT_KEYS
	PasswordAuthentication no
	KbdInteractiveAuthentication no
	PermitTTY no
	AllowTcpForwarding no
	AllowStreamLocalForwarding no
	AllowAgentForwarding no
	X11Forwarding no
	PermitTunnel no
	# Every login goes through "serve", which creates repositories on push.
	ForceCommand serve
EOF
	sshd_apply "$SSHD_GIT_CONF"
fi

# ---------------------------------------------------------------------------
# Followed repositories: [mirrors] in the config. A timer runs canopy
# -mirror every 30 minutes, which clones new ones and fetches the rest.

mirrors=$(setting mirrors)
if ((${mirrors:-0} > 0)); then
	say "Following $mirrors repositories ([mirrors] in the config)"
	# They belong to the git account, like pushed repositories.
	id "$GIT_USER" >/dev/null 2>&1 || ((DRY_RUN)) ||
		die "[mirrors] needs the $GIT_USER account that owns the repositories; deploy with --push-key"
	write "$MIRROR_UNIT" 0644 <<EOF
[Unit]
Description=Update the repositories canopy follows ([mirrors] in $CONFIG)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=$GIT_USER
Group=$GIT_USER
ExecStart=$BIN -config $CONFIG -mirror
# The first clone of a big repository can take a long time.
TimeoutStartSec=infinity
Nice=10
IOSchedulingClass=idle

# It only writes to the repository folder.
CapabilityBoundingSet=
ProtectSystem=strict
ReadWritePaths=$ROOT
ProtectHome=read-only
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
$SANDBOX
# Readable by the canopy account, like pushed repositories.
UMask=0022
EOF
	write "$MIRROR_TIMER" 0644 <<EOF
[Unit]
Description=Update the repositories canopy follows every 30 minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=30min
RandomizedDelaySec=2min

[Install]
WantedBy=timers.target
EOF
	run systemctl daemon-reload
	run systemctl enable --now canopy-mirror.timer
	# Clone new ones and update the rest now, without holding up the deploy.
	run systemctl start --no-block canopy-mirror.service
	note "updating in the background: journalctl -u canopy-mirror -f"
elif [[ -f $MIRROR_TIMER ]]; then
	say "No [mirrors] in the config any more: stopping the updates"
	run systemctl disable --now canopy-mirror.timer
	run rm -f "$MIRROR_UNIT" "$MIRROR_TIMER"
	run systemctl daemon-reload
	note "the followed repositories stay in $ROOT"
fi

# ---------------------------------------------------------------------------
# canopy-update (--self-update): a root-only command that updates canopy on
# the server itself. It builds the newest release from $UPDATE_FROM with a
# pinned Go, as a throwaway unprivileged user (systemd DynamicUser), checks
# the installed config with the new build, and only then switches to it. It
# never changes the config, and the git account can't reach it: replacing
# the running server is the admin's call, not a push key's.

if ((SELF_UPDATE)); then
	say "Installing canopy-update (releases from $UPDATE_FROM)"
	case $(uname -m) in
	x86_64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "no pinned Go build for $(uname -m)" ;;
	esac
	need systemd-run systemd
	need flock util-linux

	goroot=$UPDATE_LIB/go
	if [[ $("$goroot/bin/go" version 2>/dev/null) == "go version go$GO_VERSION "* ]]; then
		note "Go $GO_VERSION is already installed"
	else
		need sha256sum coreutils
		need tar
		gotmp=$(mktemp -d)
		name=go$GO_VERSION.linux-$arch.tar.gz
		note "downloading https://go.dev/dl/$name"
		run curl -fsSL -o "$gotmp/$name" "https://go.dev/dl/$name"
		if ((!DRY_RUN)) && ! echo "${GO_SHA256[$arch]}  $gotmp/$name" | sha256sum -c --quiet; then
			rm -rf "$gotmp"
			die "checksum mismatch for $name; refusing to install"
		fi
		run rm -rf "$goroot.new"
		run mkdir -p "$goroot.new"
		run tar -xzf "$gotmp/$name" -C "$goroot.new" --strip-components=1 --no-same-owner
		run rm -rf "$goroot"
		run mv "$goroot.new" "$goroot"
		rm -rf "$gotmp"
	fi

	# The build step, run by canopy-update as the throwaway user.
	write "$UPDATE_LIB/build" 0755 <<'EOF'
#!/usr/bin/env bash
# Written by install.sh: canopy-update's build step. It runs as a throwaway
# unprivileged user (systemd DynamicUser) in its state folder, never as root.
# In: SOURCE, WANT (a release, or empty for the newest), INSTALLED (the
# commit the running canopy was built from, if known), CHECK (1: only look).
# Exits 0 with out/canopy and out/tag built, 10 when there is nothing to
# install, anything else on errors.
set -euo pipefail
cd "$STATE_DIRECTORY"
die() {
	printf 'canopy-update: %s\n' "$*" >&2
	exit 1
}
# No system or user git config, https only.
export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0
g() { git -c protocol.allow=never -c protocol.https.allow=always -c protocol.file.allow=always "$@"; }

echo "==> Fetching releases from $SOURCE"
[[ -d src.git ]] || g init --quiet --bare src.git
g -C src.git fetch --quiet --prune --force "$SOURCE" '+refs/heads/*:refs/heads/*' '+refs/tags/*:refs/tags/*' ||
	die "couldn't fetch $SOURCE"

if [[ -n $WANT ]]; then
	tag=$WANT
	g -C src.git rev-parse -q --verify "refs/tags/$tag^{commit}" >/dev/null || die "$SOURCE has no release $tag"
else
	tag=$(g -C src.git tag -l 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1 || true)
	[[ -n $tag ]] || die "$SOURCE has no releases (tags like v0.1.0)"
fi
commit=$(g -C src.git rev-parse "refs/tags/$tag^{commit}")

running=unknown
if [[ -n $INSTALLED ]] && g -C src.git cat-file -e "$INSTALLED^{commit}" 2>/dev/null; then
	running=$(g -C src.git describe --tags "$INSTALLED" 2>/dev/null || echo "${INSTALLED:0:12}")
fi
if [[ $INSTALLED == "$commit" ]]; then
	echo "==> canopy is at $tag already"
	exit 10
fi
# Without a release named, only move forward along the release history.
if [[ -z $WANT ]]; then
	if [[ $running == unknown ]]; then
		die "can't tell which version is running (built from ${INSTALLED:-an unknown commit}${INSTALLED:+, not in $SOURCE}; deployed with update.sh?). To switch to the newest release anyway: canopy-update $tag"
	fi
	if g -C src.git merge-base --is-ancestor "$commit" "$INSTALLED"; then
		echo "==> The running canopy ($running) is already newer than $tag"
		exit 10
	fi
	g -C src.git merge-base --is-ancestor "$INSTALLED" "$commit" ||
		die "the running canopy ($running) isn't part of $tag's history. To switch anyway: canopy-update $tag"
fi

echo "==> canopy $tag (running: $running). Its release notes:"
# Repository text going to root's terminal: no control characters.
g -C src.git tag -l --format='%(contents)' "$tag" | tr -d '\000-\010\013-\037\177' | sed 's/^/    /'
if ((CHECK)); then
	echo "==> To install it: canopy-update$([[ -n $WANT ]] && echo " $tag")"
	exit 10
fi

echo "==> Building canopy $tag"
rm -rf work out
mkdir out
g clone --quiet --no-checkout src.git work
g -C work checkout --quiet --detach "$tag"
(cd work && CGO_ENABLED=0 go build -trimpath -o ../out/canopy .)
echo "$tag" >out/tag
rm -rf work
EOF

	{
		echo '#!/usr/bin/env bash'
		echo "# Written by install.sh --self-update. Updates canopy to the newest"
		echo "# release from $UPDATE_FROM; see canopy-update --help."
		printf '%s=%q\n' SOURCE "$UPDATE_FROM" BIN "$BIN" CONFIG "$CONFIG" LIB "$UPDATE_LIB" \
			STATE "$UPDATE_STATE" MIRROR_TIMER "$MIRROR_TIMER" SERVICE_USER "$SERVICE_USER"
		cat <<'EOF'
set -euo pipefail

usage() {
	cat <<USAGE
Usage: canopy-update [--check] [vX.Y.Z]    (as root)

Builds the newest canopy release from $SOURCE on this
server, checks the installed config with it, and switches to it; if the
new build doesn't answer, the previous one is put back. The config is
never changed: $CONFIG

Without a release named, it only moves forward: it stops if the running
canopy is newer, or wasn't built from a release (deployed with update.sh).

  --check     show what would be installed, change nothing
  vX.Y.Z      install that release instead, older ones too
USAGE
}
die() {
	printf 'canopy-update: %s\n' "$*" >&2
	exit 1
}

check=0
want=
for arg; do
	case $arg in
	-h | --help) usage; exit 0 ;;
	--check) check=1 ;;
	v[0-9]*.[0-9]*.[0-9]*) [[ $arg =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$arg isn't a release like v0.1.0"; want=$arg ;;
	*) die "unknown argument: $arg (see canopy-update --help)" ;;
	esac
done
((EUID == 0)) || die "run it as root: sudo canopy-update"
[[ -x $BIN ]] || die "canopy isn't installed at $BIN"
exec 9>/run/lock/canopy-update.lock
flock -n 9 || die "another canopy-update is running"

# The commit the running binary was built from: go build records it, unless
# the checkout had uncommitted changes, which makes it unknown.
installed=
info=$("$LIB/go/bin/go" version -m "$BIN" 2>/dev/null || true)
if ! grep -q 'vcs.modified=true' <<<"$info"; then
	installed=$(sed -n 's/^[[:space:]]*build[[:space:]]*vcs.revision=\([0-9a-f]*\)$/\1/p' <<<"$info")
fi

status=0
systemd-run --quiet --wait --pipe --collect --service-type=exec \
	-p DynamicUser=yes -p StateDirectory="${STATE##*/}" \
	-p ProtectSystem=strict -p ProtectHome=yes -p PrivateTmp=yes -p PrivateDevices=yes \
	-p NoNewPrivileges=yes -p ProtectKernelTunables=yes -p ProtectKernelModules=yes \
	-p ProtectControlGroups=yes -p RestrictAddressFamilies="AF_INET AF_INET6 AF_UNIX" \
	-p Nice=10 -p UMask=0022 \
	-E SOURCE="$SOURCE" -E WANT="$want" -E INSTALLED="$installed" -E CHECK="$check" \
	-E PATH="$LIB/go/bin:/usr/local/bin:/usr/bin:/bin" -E HOME="$STATE" \
	-E GOPATH="$STATE/go" -E GOCACHE="$STATE/cache" -E GOTOOLCHAIN=auto -E GOFLAGS=-mod=readonly \
	"$LIB/build" || status=$?
case $status in
0) ;;
10) exit 0 ;;
*) die "stopped (see above); canopy wasn't changed" ;;
esac
tag=$(<"$STATE/out/tag")
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "the build left no release name; canopy wasn't changed"

# Check the config with the new build, as the account canopy runs as.
install -m 0755 "$STATE/out/canopy" "$BIN.new"
trap 'rm -f "$BIN.new"' EXIT
if ! checked=$(runuser -u "$SERVICE_USER" -- "$BIN.new" -config "$CONFIG" -check 2>&1); then
	die "canopy $tag rejects $CONFIG, so canopy wasn't changed: $(tail -1 <<<"$checked")"
fi
setting() { sed -n "s/^$1: //p" <<<"$checked"; }
mirrors=$(setting mirrors)
if ((${mirrors:-0} > 0)) && [[ ! -f $MIRROR_TIMER ]]; then
	die "[mirrors] in the config needs its timer: run install.sh once first; canopy wasn't changed"
fi

echo "==> Switching to canopy $tag"
cp -p "$BIN" "$BIN.old"
mv -f "$BIN.new" "$BIN"
systemctl restart canopy.service
listen=$(setting listen)
for _ in $(seq 20); do
	if curl -fsS -o /dev/null "http://127.0.0.1:${listen##*:}/" 2>/dev/null; then
		rm -f "$BIN.old"
		echo "==> Done: canopy $tag is answering"
		exit 0
	fi
	sleep 0.5
done
mv -f "$BIN.old" "$BIN"
systemctl restart canopy.service
die "canopy $tag didn't answer after the restart, so the previous build is back (journalctl -u canopy -n 20)"
EOF
	} | write "$UPDATE_CMD" 0755
	note "update canopy: sudo canopy-update (--check to only look)"
elif [[ -e $UPDATE_CMD || -e $UPDATE_LIB ]]; then
	say "No --self-update any more: removing canopy-update and its Go"
	remove_self_update
fi

# ---------------------------------------------------------------------------
# Admin account: logging in as an ordinary user and using sudo, so that with
# --harden root can't log in over SSH at all.

ADMIN_READY=0 # it has a key, a password and sudo: root logins can go
if [[ -n $ADMIN_USER ]]; then
	say "Setting up the admin account $ADMIN_USER"
	need sudo
	sudo_group=sudo
	getent group sudo >/dev/null || sudo_group=wheel
	if id "$ADMIN_USER" >/dev/null 2>&1; then
		id -nG "$ADMIN_USER" | grep -qw "$sudo_group" ||
			note "$ADMIN_USER already exists; adding it to $sudo_group (it can then run anything as root)"
	else
		run useradd --create-home --user-group --shell /bin/bash "$ADMIN_USER"
	fi
	run usermod -aG "$sudo_group" "$ADMIN_USER"
	admin_home=$(getent passwd "$ADMIN_USER" | cut -d: -f6 || true)
	admin_home=${admin_home:-/home/$ADMIN_USER}
	admin_keys=$admin_home/.ssh/authorized_keys

	# Keys: --admin-key, or else those of whoever runs this. Only plain key
	# lines: a key restricted to a forced command (some cloud images do that
	# for root) would let the login check pass without giving a shell.
	if [[ -n $ADMIN_KEY || ! -s $admin_keys ]]; then
		keys=
		if [[ -n $ADMIN_KEY ]]; then
			src=$ADMIN_KEY
			keys=$(grep -E '^(ssh-|ecdsa-|sk-)' "$src" || true)
			[[ -n $keys ]] || die "--admin-key: no public keys (ssh-ed25519 AAAA...) in $src"
		else
			runner_home=$(getent passwd "${SUDO_USER:-root}" | cut -d: -f6 || true)
			src=${runner_home:+$runner_home/.ssh/authorized_keys}
			[[ -n $src ]] && keys=$(grep -E '^(ssh-|ecdsa-|sk-)' "$src" 2>/dev/null || true)
			src=${src:-"${SUDO_USER:-root}'s keys (no home folder found)"}
		fi
		# Each key once, by type and key data (comments may differ).
		[[ -n $keys ]] && keys=$(tr -d '\r' <<<"$keys" | awk '!seen[$1 " " $2]++')
		if [[ -n $keys ]]; then
			note "$(wc -l <<<"$keys") key(s) from $src"
			admin_group=$(id -gn "$ADMIN_USER" 2>/dev/null || echo "$ADMIN_USER")
			run install -d -m 0700 -o "$ADMIN_USER" -g "$admin_group" "$admin_home/.ssh"
			printf '%s\n' "$keys" | write "$admin_keys" 0600
			run chown "$ADMIN_USER:$admin_group" "$admin_keys"
		elif [[ -z $ADMIN_GITHUB ]]; then
			note "no SSH keys found in $src; $ADMIN_USER can't log in over SSH yet (--admin-key FILE)"
		fi
	fi

	# --admin-github: that GitHub user's keys as well, each labelled
	# github:USER, which tells canopy-github-keys which lines are its own to
	# replace. Without it, keys an earlier run took from GitHub go, as long as
	# others are left to log in with.
	gh_lines='[[:space:]]github:[A-Za-z0-9-]+$'
	others=$(grep -Ev "$gh_lines" "$admin_keys" 2>/dev/null || true)
	if [[ -n $ADMIN_GITHUB ]]; then
		gh_admin=$(mktemp)
		github_keys "$ADMIN_GITHUB" "$gh_admin"
		admin_group=$(id -gn "$ADMIN_USER" 2>/dev/null || echo "$ADMIN_USER")
		run install -d -m 0700 -o "$ADMIN_USER" -g "$admin_group" "$admin_home/.ssh"
		printf '%s\n' "$others" | cat - "$gh_admin" | grep -E '^(ssh-|ecdsa-|sk-)' |
			awk '!seen[$1 " " $2]++' | write "$admin_keys" 0600
		run chown "$ADMIN_USER:$admin_group" "$admin_keys"
		rm -f "$gh_admin"
	elif grep -Eq "$gh_lines" "$admin_keys" 2>/dev/null; then
		if grep -Eq '^(ssh-|ecdsa-|sk-)' <<<"$others"; then
			note "removing the keys taken from GitHub (no --admin-github any more)"
			printf '%s\n' "$others" | write "$admin_keys" 0600
			run chown "$ADMIN_USER:$(id -gn "$ADMIN_USER" 2>/dev/null || echo "$ADMIN_USER")" "$admin_keys"
		else
			note "keeping the keys taken from GitHub: $ADMIN_USER has no others (--admin-key FILE)"
		fi
	fi

	# The password is for sudo; SSH stays key-only.
	if [[ -n $ADMIN_PW_HASH ]]; then
		if ((DRY_RUN)); then
			run chpasswd -e "<password hash, not shown>"
		else
			[[ -r $ADMIN_PW_HASH ]] || die "--admin-password-hash: can't read $ADMIN_PW_HASH"
			hash=$(head -1 "$ADMIN_PW_HASH")
			[[ $hash =~ ^\$[0-9a-z]+\$[^:[:space:]]+$ ]] ||
				die "--admin-password-hash: expected a crypt hash (openssl passwd -6)"
			printf '%s:%s\n' "$ADMIN_USER" "$hash" | chpasswd -e
			note "password set (sudo asks for it)"
		fi
	fi

	if ((DRY_RUN)); then
		ADMIN_READY=1
		note "(a real run turns off root logins only once $ADMIN_USER has a key and a password)"
	else
		missing=()
		grep -Eq '^(ssh-|ecdsa-|sk-)' "$admin_keys" 2>/dev/null || missing+=("an SSH key")
		case $(passwd -S "$ADMIN_USER" 2>/dev/null | awk '{ print $2 }') in
		P | PS) ;;
		*) missing+=("a password (sudo passwd $ADMIN_USER)") ;;
		esac
		id -nG "$ADMIN_USER" | grep -qw "$sudo_group" || missing+=("membership of $sudo_group")
		if ((${#missing[@]})); then
			need_list=$(printf '%s, ' "${missing[@]}")
			note "root logins stay on: $ADMIN_USER still needs ${need_list%, }"
		else
			ADMIN_READY=1
		fi
	fi
	# The hardening's AllowUsers list must include it.
	SSH_ALLOW+=("$ADMIN_USER")
fi

# ---------------------------------------------------------------------------
# --github-sync: canopy-github-keys fetches the keys of the GitHub users
# named with --github-keys and --admin-github every 15 minutes, and puts them
# where they let in: $GIT_KEYS for pushes, the admin's authorized_keys for
# logins. It only replaces the lines labelled github:USER, so keys from
# --push-key and --admin-key stay. When GitHub can't be reached, nothing
# changes; a key removed on GitHub stops working at the next check.

if ((GITHUB_SYNC)); then
	say "Keeping the keys from GitHub in sync"
	{
		echo "# Written by install.sh: the GitHub users whose keys $GH_SYNC_CMD follows."
		echo "# push USER: may push; admin ACCOUNT USER: may log in as ACCOUNT."
		if ((${#PUSH_KEYS[@]})); then
			for u in "${GITHUB_USERS[@]}"; do echo "push $u"; done
		fi
		# An if, not &&: a false test ending the group would fail the
		# pipeline (pipefail) and stop the install here, with no message.
		if [[ -n $ADMIN_GITHUB ]]; then
			echo "admin $ADMIN_USER $ADMIN_GITHUB"
		fi
	} | write "$GH_SYNC_CONF" 0644
	{
		cat <<EOF
#!/usr/bin/env bash
# canopy-github-keys, written by install.sh: fetches the SSH keys of the
# GitHub users in $GH_SYNC_CONF (https://github.com/USER.keys) and
# replaces the lines labelled github:USER with them: in $GIT_KEYS
# (pushes) and in an admin account's authorized_keys (logins). Other lines
# stay. A file is left alone when any of its users can't be fetched, and an
# admin's keys are never emptied, which would lock the account out.
set -euo pipefail
conf=$GH_SYNC_CONF
git_keys=$GIT_KEYS
EOF
		cat <<'EOF'
status=0
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# fetch USER: print USER's valid keys, labelled github:USER, or fail. A user
# GitHub doesn't have (any more) has none.
fetch() {
	local user=$1 code line
	[[ $user =~ ^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$ ]] || { echo "$conf: '$user' isn't a GitHub user name" >&2; return 1; }
	code=$(curl -sS --max-time 20 --proto =https -o "$tmp/body" -w '%{http_code}' "https://github.com/$user.keys") ||
		{ echo "github.com/$user: couldn't connect" >&2; return 1; }
	case $code in
	200) ;;
	404) echo "github.com/$user: no such user; none of its keys count" >&2; return 0 ;;
	*) echo "github.com/$user: HTTP $code" >&2; return 1 ;;
	esac
	while IFS= read -r line; do
		line=${line%$'\r'}
		[[ $line =~ ^(ssh-|ecdsa-|sk-) ]] || continue
		ssh-keygen -l -f /dev/stdin <<<"$line" >/dev/null 2>&1 || continue
		read -r type blob _ <<<"$line"
		printf '%s %s github:%s\n' "$type" "$blob" "$user"
	done <"$tmp/body"
}

# update FILE PREFIX MIN USER...: replace FILE's github: lines with the keys
# of USER..., each line starting with PREFIX, keeping each key once. With MIN
# set, refuse to leave FILE without any key.
update() {
	local file=$1 prefix=$2 min=$3 user lines new next
	shift 3
	[[ -L $file || -L $(dirname "$file") ]] && { echo "$file: a symbolic link; not touching it" >&2; return 1; }
	[[ -f $file ]] || { echo "$file: missing; run install.sh again" >&2; return 1; }
	lines=
	for user in "$@"; do
		lines+=$(fetch "$user") || { echo "$file: unchanged" >&2; return 1; }
		lines+=$'\n'
	done
	new=$({ grep -Ev '[[:space:]]github:[A-Za-z0-9-]+$' "$file" 2>/dev/null || true
		printf '%s' "$lines" | sed "/./s/^/$prefix/"; } |
		awk '{ k = $0; sub(/^restrict /, "", k); split(k, f, " ") }
			/^[^#]/ && seen[f[1] " " f[2]]++ { next } NF { print }')
	if ((min)) && ! grep -Eq '(^|[[:space:]])(ssh-|ecdsa-|sk-)' <<<"$new"; then
		echo "$file: GitHub lists no keys; keeping the ones it has, so the account isn't locked out" >&2
		return 0
	fi
	[[ $(cat "$file") == "$new" ]] && return 0
	# A new file (mktemp makes it, so nothing planted there is followed),
	# renamed over the old one with its owner and mode.
	next=$(mktemp "$file.XXXXXX")
	printf '%s\n' "$new" >"$next"
	chown --reference="$file" "$next"
	chmod --reference="$file" "$next"
	mv -fT "$next" "$file"
	echo "$file: $(grep -c 'github:' <<<"$new" || true) key(s) from GitHub"
}

push=()
declare -A admins=()
while read -r kind a b; do
	case $kind in
	push) push+=("$a") ;;
	admin) admins[$a]+="$b " ;;
	esac
done < <(grep -v '^#' "$conf")

if ((${#push[@]})); then
	update "$git_keys" "restrict " 0 "${push[@]}" || status=1
fi
for account in "${!admins[@]}"; do
	home=$(getent passwd "$account" | cut -d: -f6) || { echo "no account $account" >&2; status=1; continue; }
	read -ra users <<<"${admins[$account]}"
	update "$home/.ssh/authorized_keys" "" 1 "${users[@]}" || status=1
done
exit $status
EOF
	} | write "$GH_SYNC_CMD" 0755
	admin_rw=
	[[ -n $ADMIN_GITHUB ]] && admin_rw=" -$admin_home/.ssh"
	write "$GH_SYNC_UNIT" 0644 <<EOF
[Unit]
Description=Fetch the SSH keys canopy takes from GitHub ($GH_SYNC_CONF)
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$GH_SYNC_CMD
# It only writes the key files.
CapabilityBoundingSet=CAP_CHOWN CAP_FOWNER CAP_DAC_OVERRIDE
ProtectSystem=strict
ReadWritePaths=-$(dirname "$GIT_KEYS")$admin_rw
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
$SANDBOX
EOF
	write "$GH_SYNC_TIMER" 0644 <<EOF
[Unit]
Description=Fetch the SSH keys canopy takes from GitHub every 15 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec=15min
RandomizedDelaySec=1min

[Install]
WantedBy=timers.target
EOF
	run systemctl daemon-reload
	run systemctl enable --now canopy-github-keys.timer
	note "checks GitHub every 15 minutes: journalctl -u canopy-github-keys"
elif [[ -f $GH_SYNC_TIMER ]]; then
	say "No --github-sync any more: the keys from GitHub stay as they are now"
	run systemctl disable --now canopy-github-keys.timer
	run rm -f "$GH_SYNC_UNIT" "$GH_SYNC_TIMER" "$GH_SYNC_CMD" "$GH_SYNC_CONF"
	run systemctl daemon-reload
fi

# ---------------------------------------------------------------------------
# Hardening

if ((HARDEN)); then
	say "Hardening SSH"
	# Root logins turned off by an earlier --admin-user run stay off, like the
	# AllowUsers list: a re-run must never quietly let root back in.
	root_was_off=0
	grep -qx 'PermitRootLogin no' "$SSHD_HARDEN_CONF" 2>/dev/null && root_was_off=1
	baseline=$(sshd_settings)
	if [[ -z $baseline ]] && ((!DRY_RUN)); then
		die "couldn't read the sshd configuration (sshd -T); is openssh-server installed?"
	fi
	sshd_value() { awk -v k="$1" '$1 == k { $1 = ""; sub(/^ /, ""); print }' <<<"$baseline"; }
	SSH_PORTS=$(sshd_value port | sort -un)
	[[ -n $SSH_PORTS ]] || SSH_PORTS=22
	note "sshd listens on port(s): $(tr '\n' ' ' <<<"$SSH_PORTS")"

	# Accounts that can already log in with a key. Key-only logins and the
	# AllowUsers list are only switched on when there is at least one, so
	# hardening can never lock out the admin.
	admins=()
	if ((!DRY_RUN)); then
		while IFS=: read -r user _ uid _ _ home shell; do
			[[ $user == "$GIT_USER" ]] && continue
			case $shell in */nologin | */false | */git-shell | "") continue ;; esac
			((uid == 0 || uid >= 1000)) || continue
			for f in $(sshd_settings "user=$user,host=localhost,addr=127.0.0.1" |
				awk '$1 == "authorizedkeysfile" { $1 = ""; print }'); do
				f=${f//%h/$home}
				f=${f//%u/$user}
				f=${f//%%/%}
				[[ $f == /* ]] || f=$home/$f
				if [[ -r $f ]] && grep -Eq '^[^#]*(ssh-|ecdsa-|sk-)' "$f"; then
					admins+=("$user")
					break
				fi
			done
		done < <(getent passwd)
	fi
	# Who may log in. The first run allows the accounts that have keys now;
	# later runs keep that list (so an account that gains a key later isn't
	# let in silently) and add only the accounts named with --ssh-allow.
	allow=()
	previous=$(sed -n 's/^AllowUsers //p' "$SSHD_HARDEN_CONF" 2>/dev/null || true)
	if [[ -n $previous ]]; then
		read -ra allow <<<"$previous"
	else
		allow=("${admins[@]}")
	fi
	allow+=("${SSH_ALLOW[@]}")
	id "$GIT_USER" >/dev/null 2>&1 && [[ -f $GIT_KEYS ]] && allow+=("$GIT_USER")
	mapfile -t allow < <(printf '%s\n' "${allow[@]}" | awk 'NF && !seen[$0]++')
	# Never shut out every admin: someone allowed must be able to log in.
	usable=0
	for u in "${admins[@]}"; do
		[[ " ${allow[*]} " == *" $u "* ]] && usable=1
	done
	((usable)) || admins=()

	{
		echo "# Written by install.sh --harden. Files here are read in name order and"
		echo "# the first value wins, so these override the distribution's defaults."
		if ((${#admins[@]})); then
			echo "PasswordAuthentication no"
			if [[ $(sshd_value authenticationmethods) == any ]]; then
				echo "KbdInteractiveAuthentication no"
			fi
		fi
		echo "PermitEmptyPasswords no"
		if ((ADMIN_READY && ${#admins[@]})); then
			echo "# Admins log in as $ADMIN_USER and use sudo (install.sh --admin-user)."
			echo "PermitRootLogin no"
		elif ((root_was_off)); then
			echo "# Kept from an earlier install.sh --admin-user run."
			echo "PermitRootLogin no"
		elif [[ $(sshd_value permitrootlogin) == yes ]]; then
			echo "PermitRootLogin prohibit-password"
		fi
		echo "MaxAuthTries 3"
		echo "LoginGraceTime 30"
		echo "X11Forwarding no"
		if ((${#admins[@]})) && [[ -z $(sshd_value allowusers) && -z $(sshd_value allowgroups) &&
			$(sshd_value authorizedkeyscommand) == none ]]; then
			echo "# Only these accounts may log in at all."
			echo "AllowUsers ${allow[*]}"
		fi
		if ((TAILSCALE)); then
			# A second lock behind the firewall: even with the firewall
			# down, logins from outside the tailnet are refused. (Not
			# ListenAddress: sshd would fail to start whenever it came up
			# before Tailscale.) A Match block ends the file, so it's last.
			echo "# Logins only from the tailnet (install.sh --tailscale)."
			echo "Match Address *,!${TS_RANGES//,/,!},!127.0.0.0/8,!::1"
			echo "	DenyUsers *"
		fi
	} | write "$SSHD_HARDEN_CONF" 0644
	if ((${#admins[@]})); then
		note "key-only logins; allowed to log in: ${allow[*]}"
		[[ -n $(sshd_value allowusers)$(sshd_value allowgroups) ]] && note "keeping the existing AllowUsers/AllowGroups"
		[[ $(sshd_value authorizedkeyscommand) != none ]] && note "an AuthorizedKeysCommand is set, so no AllowUsers list"
		[[ $(sshd_value authenticationmethods) != any ]] && note "AuthenticationMethods is set; leaving keyboard-interactive alone"
	elif ((!DRY_RUN)); then
		note "WARNING: no account has an SSH key, so password logins stay on and"
		note "         there's no AllowUsers list. Add your key to ~/.ssh/authorized_keys"
		note "         and run this again to finish."
	fi
	((TAILSCALE)) && note "SSH logins only from the tailnet ($TS_RANGES)"
	if ((ADMIN_READY && ${#admins[@]})); then
		note "root can't log in over SSH; $ADMIN_USER logs in and uses sudo"
	elif ((root_was_off)); then
		note "root logins stay off, as set by an earlier run (--undo-ssh-hardening resets this)"
	fi
	sshd_apply "$SSHD_HARDEN_CONF"

	say "Setting up the firewall"
	# SSH is open to the internet, or with Tailscale only on its interface.
	ssh_ports=()
	for p in $SSH_PORTS; do ssh_ports+=("$p"); done
	tcp_ports=("${ssh_ports[@]}")
	((TAILSCALE)) && tcp_ports=()
	web_ports=()
	if ((CADDY)); then
		web_ports=(80 443)
	elif [[ $LISTEN != 127.0.0.1:* && $LISTEN != localhost:* && $LISTEN != "[::1]:"* ]]; then
		web_ports=("${LISTEN##*:}")
	fi
	if command -v firewall-cmd >/dev/null || [[ $PM == dnf ]]; then
		need firewall-cmd firewalld
		run systemctl enable --now firewalld.service
		zone=public
		((DRY_RUN)) || zone=$(firewall-cmd --get-default-zone)
		want_services=()
		want_ports=()
		for p in "${tcp_ports[@]}" "${web_ports[@]}"; do
			case $p in
			22) want_services+=(ssh) ;;
			80) want_services+=(http) ;;
			443) want_services+=(https) ;;
			*) want_ports+=("$p/tcp") ;;
			esac
		done
		((CADDY)) && want_ports+=(443/udp) # HTTP/3
		if ((!DRY_RUN)); then
			have_services=$(firewall-cmd --permanent --zone="$zone" --list-services)
			have_ports=$(firewall-cmd --permanent --zone="$zone" --list-ports)
			# dhcpv6-client is how the machine gets an IPv6 address on many networks.
			for svc in $have_services; do
				[[ $svc == dhcpv6-client || " ${want_services[*]} " == *" $svc "* ]] && continue
				note "closing service $svc"
				firewall-cmd --quiet --permanent --zone="$zone" --remove-service="$svc"
			done
			for port in $have_ports; do
				[[ " ${want_ports[*]} " == *" $port "* ]] && continue
				note "closing port $port"
				firewall-cmd --quiet --permanent --zone="$zone" --remove-port="$port"
			done
		fi
		for svc in "${want_services[@]}"; do run firewall-cmd --quiet --permanent --zone="$zone" --add-service="$svc"; done
		for port in "${want_ports[@]}"; do run firewall-cmd --quiet --permanent --zone="$zone" --add-port="$port"; done
		if ((TAILSCALE)); then
			# Traffic arriving from the tailnet gets its own zone that
			# allows SSH, and nothing else.
			if ((DRY_RUN)) || ! firewall-cmd --permanent --get-zones | grep -qw tailscale; then
				run firewall-cmd --quiet --permanent --new-zone=tailscale
			fi
			run firewall-cmd --quiet --permanent --zone=tailscale --change-interface=tailscale0
			for p in "${ssh_ports[@]}"; do
				if [[ $p == 22 ]]; then
					run firewall-cmd --quiet --permanent --zone=tailscale --add-service=ssh
				else
					run firewall-cmd --quiet --permanent --zone=tailscale --add-port="$p/tcp"
				fi
			done
		fi
		run firewall-cmd --quiet --reload
		note "zone $zone allows: ${want_services[*]} ${want_ports[*]}"
		((TAILSCALE)) && note "zone tailscale (interface tailscale0) allows: SSH on ${ssh_ports[*]}"
		if ((!DRY_RUN)); then
			# Newer firewalld prints "public (default)"; the name is the first word.
			while read -r other _; do
				[[ -z $other || $other == "$zone" || $other == tailscale ]] && continue
				note "note: zone '$other' is also active and has its own rules (not changed):"
				note "      $(firewall-cmd --zone="$other" --list-services) $(firewall-cmd --zone="$other" --list-ports)"
			done < <(firewall-cmd --get-active-zones | grep -v '^[[:space:]]')
		fi
	else
		need ufw
		# Start from a clean slate so only the ports below stay open (ufw
		# keeps a backup of the old rules in /etc/ufw).
		run ufw --force reset
		run ufw default deny incoming
		run ufw default allow outgoing
		for p in "${tcp_ports[@]}" "${web_ports[@]}"; do run ufw allow "$p/tcp"; done
		((CADDY)) && run ufw allow 443/udp # HTTP/3
		if ((TAILSCALE)); then
			for p in "${ssh_ports[@]}"; do run ufw allow in on tailscale0 to any port "$p" proto tcp; done
		fi
		run ufw --force enable
		note "open: ${tcp_ports[*]} ${web_ports[*]} (tcp)$( ((CADDY)) && echo ', 443 (udp)')"
		((TAILSCALE)) && note "open on tailscale0 only: SSH on ${ssh_ports[*]}"
	fi

	say "Turning on automatic security updates"
	case $PM in
	apt-get)
		need unattended-upgrade unattended-upgrades
		write /etc/apt/apt.conf.d/20auto-upgrades 0644 <<'EOF'
// Written by install.sh --harden: install security updates daily.
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
EOF
		run systemctl enable --now apt-daily.timer apt-daily-upgrade.timer
		;;
	dnf)
		if command -v dnf5 >/dev/null; then
			pkg_install dnf5-plugin-automatic
			write /etc/dnf/dnf5-plugins/automatic.conf 0644 <<'EOF'
# Written by install.sh --harden: install security updates daily.
[commands]
apply_updates = yes
upgrade_type = security
EOF
			run systemctl enable --now dnf5-automatic.timer
		else
			pkg_install dnf-automatic
			run sed -i 's/^apply_updates.*/apply_updates = yes/; s/^upgrade_type.*/upgrade_type = security/' \
				/etc/dnf/automatic.conf
			run systemctl enable --now dnf-automatic.timer
		fi
		;;
	*) note "skipped: no automatic update tool known for this system" ;;
	esac

	say "Hardening kernel settings"
	# What Lynis (KRNL-6000) suggests that can't get in the way here. Left
	# alone on purpose: rp_filter (Tailscale wants loose mode, 2),
	# net.ipv4.ip_forward (Tailscale exit nodes and subnet routers set it),
	# kernel.modules_disabled (it would stop WireGuard and other modules
	# loading after boot), and two that a service sets again at every boot:
	# log_martians (ufw's sysctl.conf) and fs.suid_dumpable (Ubuntu's apport).
	sysctls=(
		"fs.protected_fifos = 2"   # no writing into others' FIFOs in sticky dirs
		"kernel.kptr_restrict = 2" # hide kernel addresses, even from root
		"kernel.sysrq = 0"         # no magic SysRq keys
		"net.ipv4.conf.all.send_redirects = 0" # this is not a router
		"net.ipv4.conf.default.send_redirects = 0"
	)
	{
		echo "# Written by install.sh --harden. rp_filter, ip_forward and"
		echo "# kernel.modules_disabled are left alone on purpose (Tailscale, WireGuard)."
		printf '%s\n' "${sysctls[@]}"
	} | write "$SYSCTL_HARDEN_CONF" 0644
	run sysctl -q --system
	# Network protocols nothing here uses, which have had kernel bugs (Lynis
	# NETW-3200). "install ... /bin/false" stops them loading even when a
	# program asks; blacklist alone only stops loading by alias.
	write "$MODPROBE_HARDEN_CONF" 0644 <<'EOF'
# Written by install.sh --harden: network protocols this server doesn't use.
blacklist dccp
blacklist sctp
blacklist rds
blacklist tipc
install dccp /bin/false
install sctp /bin/false
install rds /bin/false
install tipc /bin/false
EOF
	if ((!DRY_RUN)); then
		# Something else could still set a value back; say so instead of
		# assuming.
		for s in "${sysctls[@]}"; do
			key=${s%% =*}
			want=${s##*= }
			have=$(sysctl -n "$key" 2>/dev/null || echo "?")
			[[ $have == "$want" ]] || note "note: $key is $have, not $want (something else sets it)"
		done
		loaded=$(lsmod | awk '$1 ~ /^(dccp|sctp|rds|tipc)$/ { print $1 }' | tr '\n' ' ')
		[[ -n $loaded ]] && note "already loaded until the next reboot: $loaded"
	fi
	note "kernel settings in $SYSCTL_HARDEN_CONF; dccp, sctp, rds and tipc blocked"

	say "Checking the rest of the system"
	if command -v getenforce >/dev/null; then
		mode=$(getenforce 2>/dev/null || echo unknown)
		if [[ $mode == Enforcing ]]; then
			note "SELinux: enforcing"
		else
			note "WARNING: SELinux is $mode; enforcing mode is an important safety net"
		fi
	elif [[ -r /sys/module/apparmor/parameters/enabled ]]; then
		if [[ $(cat /sys/module/apparmor/parameters/enabled) == Y ]]; then
			note "AppArmor: enabled"
		else
			note "WARNING: AppArmor is disabled"
		fi
	else
		note "no SELinux or AppArmor found"
	fi
	# Anything else listening on a non-local address is blocked by the
	# firewall now, but is better turned off.
	others=$(ss -Htlnup 2>/dev/null | awk '
		{ addr = $5; proc = $7; sub(/^users:\(\("/, "", proc); sub(/".*/, "", proc) }
		addr ~ /^(127\.|\[::1\]|::1)/ { next }
		proc ~ /^(sshd|sshd-session|caddy|systemd|tailscaled)$/ { next }
		{ print "    " $1 " " addr " (" (proc == "" ? "?" : proc) ")" }' | sort -u)
	if [[ -n $others ]]; then
		note "also listening (now firewalled; consider turning these off):"
		printf '%s\n' "$others"
	else
		note "nothing else is listening on the network"
	fi
fi

# ---------------------------------------------------------------------------

say "Done"
if ((CADDY)); then
	chain="Caddy -> canopy"
	((ANUBIS)) && chain="Caddy -> Anubis ($CHALLENGE) -> canopy"
	note "site:         https://$DOMAIN  ($chain)"
else
	note "site:         http://$LISTEN"
fi
note "repositories: $ROOT"
note "config:       $CONFIG"
logs="journalctl -u canopy"
((${mirrors:-0} > 0)) && logs+=" -u canopy-mirror"
((ANUBIS)) && logs+=" -u anubis@canopy"
((CADDY)) && logs+=" -u caddy"
note "logs:         $logs"
if [[ -f $GIT_KEYS ]]; then
	note "push:         git push $GIT_USER@${PUSH_HOST:-<host>}:NAME.git  (new names are created; ssh $GIT_USER@${PUSH_HOST:-<host>} help)"
fi
((TAILSCALE)) && note "ssh:          only from the tailnet, at ${TS_HOST:-<tailnet name>} (${TS_IP:-100.x.y.z})"
[[ -n $ADMIN_USER ]] && note "admin:        log in as $ADMIN_USER and use sudo"
if ((HARDEN)); then
	note ""
	note "SSH settings changed. Keep this session open and check that a new"
	note "login still works before closing it."
	((TAILSCALE)) && note "That login has to come from a machine in the tailnet."
fi
if ((TAILSCALE)); then
	note ""
	note "Turn off key expiry for this machine in the Tailscale admin console"
	note "(Machines, then the machine's menu: Disable key expiry), or SSH stops"
	note "working when its key expires. Tagged machines don't expire."
fi
if ((CADDY)); then
	note ""
	note "Caddy requests a certificate for $DOMAIN right away and keeps retrying"
	note "until the domain's DNS points at this machine (journalctl -u caddy)."
fi
