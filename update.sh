#!/usr/bin/env bash
# Updates canopy on a server that install.sh has set up: builds canopy for
# the server, uploads it with config/, and restarts it. Nothing else on the
# server changes (Caddy, Anubis, SSH, the firewall, accounts), so it is
# quick and quiet. Run from your own machine:
#
#     ./update.sh                  # the server install.sh last deployed to
#     ./update.sh USER@HOST
#     ./update.sh --force          # overwrite config edited on the server
#
# If the config on the server was changed since it was last uploaded (someone
# edited /etc/canopy there), nothing is changed: update.sh shows how server
# and local differ, and --force uploads yours over it.
#
# Use install.sh instead for changes that go beyond canopy and its config:
# push keys, the domain, Anubis, hardening, or the first [mirrors] entry.
set -euo pipefail

if ((BASH_VERSINFO[0] < 4)); then
	echo "update.sh needs bash 4 or newer (on macOS: brew install bash, then run it with that bash)" >&2
	exit 1
fi

HERE=$(cd "$(dirname "$0")" && pwd)

die() {
	printf 'update.sh: %s\n' "$*" >&2
	exit 1
}
say() { printf '==> %s\n' "$*"; }

FORCE=
TARGET=
for arg; do
	case $arg in
	-h | --help)
		sed -n '2,16s/^# \{0,1\}//p' "$0"
		exit 0
		;;
	--force) FORCE=--force ;;
	-*) die "unknown option: $arg" ;;
	*)
		[[ -z $TARGET ]] || die "one server at a time: $TARGET or $arg?"
		TARGET=$arg
		;;
	esac
done
TARGET=${TARGET:-$(sed -n 's/^target=//p' "$HERE/config/deploy.env" 2>/dev/null)}
[[ -n $TARGET ]] || die "which server? ./update.sh USER@HOST (or deploy once with install.sh, which remembers it)"

command -v go >/dev/null || die "go not found"
command -v ssh >/dev/null || die "ssh not found"
# Extra ssh options, as for install.sh: DEPLOY_SSH_OPTS="-F ./ssh_config".
read -ra ssh_opts <<<"${DEPLOY_SSH_OPTS:-}"
# shellcheck disable=SC2029 # the command is meant to run on the server
remote() { ssh "${ssh_opts[@]}" "$TARGET" "$@"; }

arch=$(remote 'uname -m' </dev/null) || die "can't connect to $TARGET over SSH; check that 'ssh $TARGET' works"
case $arch in
x86_64) goarch=amd64 ;;
aarch64 | arm64) goarch=arm64 ;;
*) die "no build for $arch servers" ;;
esac

stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT

say "Building canopy for linux/$goarch"
(cd "$HERE" && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -trimpath -o "$stage/canopy" .)
cp -R "$HERE/config" "$stage/"
rm -f "$stage/config/deploy.env"
# Kept next to install-command.sh, so a later install.sh run on the server
# matches this version.
cp "$HERE/install.sh" "$stage/"

# The part that runs on the server, as root.
cat >"$stage/update-remote.sh" <<'EOF'
#!/usr/bin/env bash
# Written by update.sh: installs the uploaded canopy and config, restarts it.
set -euo pipefail
cd "$(dirname "$0")"
BIN=/usr/local/bin/canopy
CONFIG=/etc/canopy/canopy.toml
MIRROR_TIMER=/etc/systemd/system/canopy-mirror.timer
# Checksums of the config files as update.sh or install.sh last wrote them.
STAMP=/etc/canopy/.uploaded
FORCE=${1:-}
die() {
	printf 'update.sh: %s\n' "$*" >&2
	exit 1
}

[[ -f /etc/systemd/system/canopy.service ]] || die "canopy isn't installed on this server; run install.sh first"
checked=$(./canopy -config config/canopy.toml -check 2>&1) ||
	die "config/canopy.toml is invalid, nothing was changed: $(tail -1 <<<"$checked")"
setting() { sed -n "s/^$1: //p" <<<"$checked"; }
mirrors=$(setting mirrors)
if ((${mirrors:-0} > 0)) && [[ ! -f $MIRROR_TIMER ]]; then
	die "[mirrors] is new in the config and needs its timer: run install.sh ... --yes once instead"
fi
if ((${mirrors:-0} == 0)) && [[ -f $MIRROR_TIMER ]]; then
	die "[mirrors] is gone from the config: run install.sh ... --yes once to stop the updates"
fi

# The config files to install: canopy.toml, and the about page and favicon,
# which go next to it as install.sh puts them.
src=$PWD/config
uploads=("$src/canopy.toml")
targets=("$CONFIG")
for key in favicon about; do
	file=$(setting "$key")
	case $file in "$src"/*)
		uploads+=("$file")
		targets+=("$(dirname "$CONFIG")/${file#"$src"/}")
		;;
	esac
done

# Don't overwrite edits made on the server: a file that differs from the
# upload must still be the one update.sh or install.sh last wrote there.
edited=()
for i in "${!targets[@]}"; do
	target=${targets[i]}
	[[ -f $target ]] || continue
	cmp -s "${uploads[i]}" "$target" && continue
	sum=$(sha256sum <"$target")
	grep -qxF "${sum%% *}  $target" "$STAMP" 2>/dev/null && continue
	edited+=("$i")
done
if ((${#edited[@]})) && [[ $FORCE != --force ]]; then
	for i in "${edited[@]}"; do
		diff -u --label "server: ${targets[i]}" --label "local: config/${uploads[i]#"$src"/}" \
			"${targets[i]}" "${uploads[i]}" >&2 || true
	done
	die "server and local config are not the same, and the server's was changed since the last upload (above); nothing was changed. To push yours over it: ./update.sh --force"
fi

install -m 0755 canopy "$BIN"
for i in "${!targets[@]}"; do
	[[ -f ${targets[i]} ]] && ! cmp -s "${uploads[i]}" "${targets[i]}" && cp "${targets[i]}" "${targets[i]}.bak"
	install -D -m 0644 "${uploads[i]}" "${targets[i]}"
done
for target in "${targets[@]}"; do sha256sum "$target"; done >"$STAMP"

systemctl restart canopy.service
listen=$(setting listen)
for _ in $(seq 20); do
	if curl -fsS -o /dev/null "http://127.0.0.1:${listen##*:}/" 2>/dev/null; then
		up=1
		break
	fi
	sleep 0.5
done
[[ -n ${up:-} ]] || die "canopy didn't come back after the restart: journalctl -u canopy -n 20"
syncing=
if [[ -f $MIRROR_TIMER ]]; then
	systemctl start --no-block canopy-mirror.service
	syncing=", $mirrors mirrors syncing in the background"
fi
echo "==> Done: canopy updated and answering$syncing"
EOF

say "Uploading to $TARGET:canopy"
tar -C "$stage" -czf - . | remote 'mkdir -p canopy && tar -C canopy -xzf -' || die "upload failed"

tty=()
[[ -t 0 ]] && tty=(-t)
# shellcheck disable=SC2016 # expanded on the server
remote "${tty[@]}" 'if [ "$(id -u)" = 0 ]; then bash canopy/update-remote.sh '"$FORCE"'; else sudo bash canopy/update-remote.sh '"$FORCE"'; fi' ||
	die "the update stopped on $TARGET (see above)"
