# Deploying canopy

This takes a fresh VPS to a finished git server:

```
browse and clone:  https://git.example.com          Caddy (HTTPS) → Anubis → canopy
push:              git@server.your-tailnet.ts.net    Tailscale → SSH → git account
admin:             alice@server.your-tailnet.ts.net  Tailscale → SSH → sudo
```

The web site is the only thing open to the internet. SSH, both your admin
logins and your pushes, is reachable only from your
[Tailscale](https://tailscale.com) network (your "tailnet"). Without
Tailscale, SSH stays open to the internet but is still key-only and limited
to known accounts.

One command does the work, from your own machine: `./install.sh
root@your-server`. Steps 1–3 below prepare for it; step 4 runs it.
Throughout, replace `git.example.com` with your domain and `alice` with
your user name.

## What you need

- [ ] **A VPS running Debian 13, Ubuntu 24.04 or Ubuntu 26.04** (tested).
      Fedora 44 is tested too; other Fedora, Rocky or Alma versions
      probably work.
- [ ] **SSH key login to it as root** (or as a user with sudo) that already
      works: `ssh root@VPS true` succeeds without a password.
- [ ] **Your provider's web console** (often called "VNC", "recovery" or
      "rescue" console). If SSH ever locks you out, it is the way back in.
      Find it now.
- [ ] **A domain** whose DNS you control.
- [ ] **Go 1.26 or newer** on your machine, to build canopy.
- [ ] **Recommended: a Tailscale account**, with your machine in the tailnet
      (`tailscale status` works), and an **auth key** for the server from
      <https://login.tailscale.com/admin/settings/keys>: one-off and
      pre-approved. A tagged key also stops the server's key from expiring.

On a VPS that already runs other services: the hardening closes every port
except 80 and 443 (and SSH, unless it moves to Tailscale).

## 1. Prepare on your machine

In the canopy folder, edit the site's settings and about page:

```sh
$EDITOR config/canopy.toml     # title, description; see config/README.md
$EDITOR config/about.md         # who you are and what's here (or remove `about` from the config)
```

You can leave the title and clone address at their placeholders: the
deploy fills them in from your domain. Check the file with
`go run . -check`.

To keep a server's settings out of the canopy checkout, in a repository of
their own, put them in a folder laid out like `config/` and point the
scripts at it with `CANOPY_CONFIG_DIR`. The answers (`deploy.env`) are then
saved there too:

```sh
CANOPY_CONFIG_DIR=~/git.example.com/config ./install.sh root@your-server
CANOPY_CONFIG_DIR=~/git.example.com/config ./update.sh
```

## 2. DNS and the provider's firewall

1. At your DNS provider, create an **A** record for `git.example.com` with
   the VPS's IPv4 address, and an **AAAA** record with its IPv6 address if it
   has one. On Cloudflare, set the record to **DNS only** (grey cloud):
   through Cloudflare's proxy, Anubis would see every visitor as Cloudflare.
2. If the provider has its own firewall (DigitalOcean Cloud Firewall,
   Hetzner Cloud Firewall, AWS security groups, …), allow incoming
   **TCP 22, 80, 443** and **UDP 443** for now. With Tailscale you remove
   22 again in step 5.

Check that DNS has arrived:

```sh
dig +short git.example.com      # must print the VPS address
```

## 3. Bring the server up to date

```sh
ssh root@VPS
apt update && apt full-upgrade -y
reboot                          # if a new kernel was installed
```

## 4. Deploy

From the canopy folder on your machine:

```sh
./install.sh root@git.example.com      # or an ssh alias, or ssh://root@host:2222
```

It connects once and asks (Enter takes the default in brackets):

```
    Domain name for the site [git.example.com]:
    Email for Let's Encrypt notices (optional) []:
    Put Anubis bot protection in front [Y/n]:
    Anubis challenge: nojs (no JavaScript) or pow (proof-of-work) [nojs]:
    Public key files allowed to push, comma-separated (none: no pushing) [~/.ssh/id_ed25519.pub]:
    Also allow the SSH keys of GitHub users to push, comma-separated (none: no) [none]:
    Keep them in sync (the server checks GitHub every 15 minutes, ...) [Y/n]:   (only with GitHub users)
    Refuse pushes that rewrite or delete branches [Y/n]:
    Let push keys rename and delete repositories [y/N]:
    Harden the server (firewall, key-only SSH, automatic updates) [Y/n]:
    SSH and pushes only over Tailscale (only the web site stays public) [Y/n]:
    Tailscale auth key:
    Admin account to log in as instead of root (none: keep using root) [alice]:
    Sudo password for alice:
    Again:
    Site title [git.example.com]:
```

The defaults are the most secure choices:

- **Anubis** shows browsers a short "not a bot" check, which keeps
  scrapers off. `nojs` needs no JavaScript; `pow` (proof-of-work) is harder
  for bots but needs JavaScript. git and other non-browser clients are never
  checked.
- **Push keys** are the public keys allowed to push. Add one per machine.
- **Refusing rewrites** means `git push --force` and branch deletion are
  refused, so a stolen key can't destroy history.
- **Hardening** adds a firewall, key-only SSH for a fixed list of accounts,
  automatic security updates, and safer kernel settings (see
  [security.md](security.md#kernel-settings)).
- **Tailscale** is offered (default yes) when your machine is in a tailnet.
  The auth key is only asked for while the server isn't in one yet. It is
  typed hidden, sent for this run only, deleted on the server afterwards
  and never saved.
- **The admin account** is asked for when you log in as root. It gets
  root's SSH keys and a password for sudo, and then root can't log in over
  SSH at all. Only a hash of the password leaves your machine.

Then it shows a summary and asks once more. Nothing changes before that.
When you confirm, it:

1. fills in the title and clone address in `config/canopy.toml` and the
   domain in `config/about.md` where they are still placeholders, and saves
   your answers in `config/deploy.env` (never the key or password)
2. builds canopy for the server's CPU and uploads it with `config/` and
   your push keys to `~/canopy` on the server
3. runs the installer there (the exact command is kept as
   `~/canopy/install-command.sh`)
4. opens a **new** SSH connection, as the admin account and over Tailscale
   if you chose them, to check that you can still log in. If that fails, it
   undoes the SSH hardening over the connection it kept open, so you are
   never locked out
5. checks that pushing works (`ssh git@… list`)

The last lines tell you how to reach the server from now on:

```
==> Done
    site: https://git.example.com
    admin: log in as alice and use sudo
    ssh:  only over Tailscale: ssh alice@server.your-tailnet.ts.net
    push: git remote add origin git@server.your-tailnet.ts.net:NAME.git  (new names are created on push)
    deploy again (same answers): ./install.sh alice@server.your-tailnet.ts.net --yes
```

From then on you deploy as the admin account; the installer runs with sudo,
which asks for its password.

`--dry-run` shows the whole run without changing anything, on your machine
or the server. Flags answer questions in advance: `--domain`, `--email`,
`--anubis`, `--challenge pow`, `--push-key FILE`, `--github-keys USER`, `--github-sync`,
`--deny-force-push`, `--manage-repos`, `--self-update`, `--harden`, `--tailscale` / `--no-tailscale`, `--tailscale-key FILE`,
`--admin-user NAME`, `--admin-github USER`. With `--yes`, give the Tailscale key with
`--tailscale-key FILE` or `TS_AUTHKEY`; no sudo password is asked, so root
logins stay on until you set one (`sudo passwd alice`) and deploy again. On
macOS, run the script with a newer bash (`brew install bash`).

## 5. After deploying

1. **Turn off key expiry** for the server in the Tailscale admin console
   (Machines → the server's menu → Disable key expiry), unless you used a
   tagged key. Otherwise the server drops off the tailnet when its key
   expires (after 180 days by default), and SSH with it.
2. **Close SSH in the provider's firewall** too: remove TCP 22 from the
   rules of step 2. The server's own firewall already drops it; this adds a
   third lock. Keep the provider's recovery console as the way in.
3. **Check from outside the tailnet** (turn Tailscale off for a moment):
   `nc -vz -w5 git.example.com 22` should time out, and
   `curl -sI https://git.example.com` should answer `200`.
4. **Give the server a short SSH name** and push your first project: see
   [usage.md](usage.md).

## 6. Backups

Nothing backs up `/srv/git` for you. Use one or both of these.

**Your provider's backups** (DigitalOcean: Droplet → Backups → Enable)
snapshot the whole server and restore it in one click.

**A daily copy on your own machine**, with a key that can only read
`/srv/git`, so a stolen laptop gives no shell on the server:

```sh
ssh-keygen -t ed25519 -N "" -C git-backup -f ~/.ssh/git-backup
ssh alice@server.your-tailnet.ts.net sudo apt install -y rsync
echo "restrict,command=\"/usr/bin/rrsync -ro /srv/git\" $(cat ~/.ssh/git-backup.pub)" |
  ssh alice@server.your-tailnet.ts.net 'cat >> ~/.ssh/authorized_keys'
```

`~/.config/systemd/user/git-backup.service`:

```ini
[Unit]
Description=Copy the git server's repositories

[Service]
Type=oneshot
ExecStart=/usr/bin/rsync -a -e "ssh -i %h/.ssh/git-backup -o IdentitiesOnly=yes -o BatchMode=yes" alice@server.your-tailnet.ts.net:/ %h/backups/git/
```

`~/.config/systemd/user/git-backup.timer`:

```ini
[Unit]
Description=Daily copy of the git server's repositories

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=30m

[Install]
WantedBy=timers.target
```

```sh
mkdir -p ~/backups/git
systemctl --user daemon-reload
systemctl --user enable --now git-backup.timer
systemctl --user start git-backup.service && ls ~/backups/git
```

There is no `--delete`, so a repository deleted on the server stays in the
backup. A missed day runs the next time the machine is on. Test a restore
once: `git clone ~/backups/git/project.git /tmp/restore-test`.

## Day to day

| Task | How |
|---|---|
| Change a setting, the about page or the icon | Edit the files in `config/` (or your `CANOPY_CONFIG_DIR` folder), then `./update.sh`. If the config was edited on the server, it stops and shows the difference; `./update.sh --force` overwrites it. |
| Update canopy | `git pull` in the canopy folder, then `./update.sh`. Or, on a server installed with `--self-update`: `sudo canopy-update` there (see below). |
| Add or remove a push key | Run `./install.sh alice@server…` without `--yes` and change the key list (it is the complete list). |
| Let another account log in over SSH | On the server: `~/canopy/install-command.sh --ssh-allow NAME` (remembered from then on). |
| Reboot after kernel updates | Updates install by themselves but never reboot. When `/var/run/reboot-required` exists: `sudo reboot`. |
| Keep a repository off the web site | `ssh git@server… private NAME on` (or `create NAME --private` before the first push); it's gone within 15 seconds, no deploy needed. `hidden = true` under `[repos."NAME"]` in the config does the same through a deploy. |
| Change the theme or fonts | `[theme]` (and your own `[themes]`) in `config/canopy.toml` (see [config/README.md](config/README.md)), then `./update.sh`. Fonts are downloaded once into `/var/cache/canopy/fonts`; a server installed before `[theme]` existed needs `./install.sh alice@server… --yes` once to create that folder (until then fonts are downloaded again on every restart). |
| Follow a repository | Add it under `[mirrors]` in `config/canopy.toml` (see [config/README.md](config/README.md)), then `./update.sh`. The first `[mirrors]` entry, and removing the last one, need `./install.sh alice@server… --yes` instead, which sets up or removes the timer. |
| Logs | `journalctl -u canopy -u anubis@canopy -u caddy -f` (followed repositories: `-u canopy-mirror`) |

`./update.sh` (or `./update.sh alice@server…`; without it, the server
`install.sh` last deployed to) builds canopy, uploads it with `config/` and
restarts it, and touches nothing else: no packages, Caddy, Anubis, SSH or
firewall. It checks the config with the new build first and changes nothing
if it's invalid. It also leaves the config alone if someone edited it on the
server (in `/etc/canopy`) since the last upload: it shows how server and
local differ and stops. Copy the change into `config/` and run it again,
or run `./update.sh --force` to upload yours over it (the server's version
is kept as `.bak`). Use `./install.sh … --yes` for anything else, such as
the domain, Anubis, push keys or hardening.

### Updating from the server itself

With `--self-update` (or a yes to the deploy question, default no), the
server gets `canopy-update`, a root-only command that updates canopy
without your machine:

```sh
ssh -t alice@server… sudo canopy-update            # the newest release
ssh -t alice@server… sudo canopy-update --check    # only show what's new
ssh -t alice@server… sudo canopy-update v0.1.0     # that release, older ones too
```

(`-t` gives sudo a terminal to ask for the password on.)

It fetches the release tags from `https://github.com/spinneydev/canopy` (or
`--update-from URL`), builds the newest one with Go (installed by the
installer, a pinned and checksum-verified release) as a throwaway
unprivileged user, and prints the release notes. Read their "Upgrading"
part: some releases need `install.sh` as well. It then checks
`/etc/canopy/canopy.toml` with the new build and switches to it only if
the config passes. If the new build doesn't answer after the restart, the
previous one is put back. It never changes the config.

Without a release named, it only moves forward. It stops when the running
canopy is newer than the newest release, or wasn't built from one (a
deploy with `update.sh` from a checkout with commits or changes of your own).
Naming a release switches anyway. The `git` account can't run it: replacing
the running server stays with the admin, not with whoever holds a push key.

Re-running the installer is always safe. It keeps the repositories, the
list of accounts allowed to log in, and Anubis's signing key, and applies
whatever changed.

## Installing on the server by hand

Instead of `./install.sh USER@HOST`, you can copy the files and run the
installer on the server. Build for the server first; `CGO_ENABLED=0`
matters, or the binary may not start on an older distribution:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o canopy .   # GOARCH=arm64 for ARM
cp ~/.ssh/id_ed25519.pub push-keys.pub                          # one line per key allowed to push
ssh root@VPS mkdir -p canopy
scp -r canopy install.sh config push-keys.pub root@VPS:canopy/
```

On the server, keep the command in a script so every update uses the same
flags, and start without the hardening:

```sh
cd ~/canopy
cat > install-command.sh <<'EOF'
#!/bin/sh
cd "$(dirname "$0")" && exec ./install.sh --domain git.example.com --anubis \
    --push-key push-keys.pub --deny-force-push "$@"
EOF
chmod +x install-command.sh
./install-command.sh --dry-run     # optional: see what it would do
./install-command.sh
```

Check the site in a browser and push a project (see [usage.md](usage.md)).
Then add the hardening. **Keep this SSH session open** until a second one
works:

```sh
sed -i 's/--deny-force-push/--deny-force-push --harden --admin-user alice/' install-command.sh
./install-command.sh
passwd alice                       # the password sudo will ask for
./install-command.sh               # with a password set, root logins go off
```

From a second terminal: `ssh alice@VPS sudo true`. To move SSH onto
Tailscale as well, put the auth key in a file only root can read and pass it
once:

```sh
(umask 077 && cat > ts-authkey)    # paste the key, then Ctrl-D
sed -i 's/--harden/--harden --tailscale/' install-command.sh
./install-command.sh --tailscale-key ts-authkey && rm ts-authkey
```

Then check `ssh alice@server.your-tailnet.ts.net sudo true` from a machine
in the tailnet before closing the first session.

### Installer options

| Option | Effect |
|---|---|
| `--domain HOST` | Caddy with automatic HTTPS for this name |
| `--email ADDR` | email for Let's Encrypt notices |
| `--no-caddy` | no Caddy even with `--domain` |
| `--anubis`, `--challenge nojs\|pow` | Anubis in front of canopy, and its browser check |
| `--push-key FILE` | keys allowed to push (repeatable; replaces the list) |
| `--github-keys USER` | also allow USER's keys from `https://github.com/USER.keys` (repeatable; fetched on every run) |
| `--github-sync` | fetch the GitHub keys (`--github-keys`, `--admin-github`) again every 15 minutes on the server (`canopy-github-keys.timer`), so changes on GitHub count without a deploy |
| `--deny-force-push` | refuse pushes that rewrite or delete branches |
| `--manage-repos` | let push keys `rename` and `delete` repositories over SSH (off: a stolen key could erase one) |
| `--self-update`, `--update-from URL` | install `canopy-update`, which builds the newest release on the server and switches to it; releases from URL (https, default `https://github.com/spinneydev/canopy`) |
| `--harden` | firewall, key-only SSH for listed accounts, automatic updates, kernel settings, Caddy admin API on a root-only socket |
| `--ssh-allow USER` | with `--harden`: also let this account log in |
| `--admin-user NAME` | admin account with sudo; with `--harden`, root logins go off once it has a key and a password |
| `--admin-key FILE`, `--admin-password-hash FILE` | that account's keys (default: those of whoever runs the installer; when deploying, those root logs in with) and password hash |
| `--admin-github USER` | also let USER's GitHub keys log in as that account (kept current with `--github-sync`; its last key is never removed) |
| `--tailscale`, `--tailscale-key FILE` | with `--harden`: join the tailnet and accept SSH only from it |
| `--undo-ssh-hardening` | back to the system's own SSH settings, SSH open in the firewall |
| `--uninstall` | remove canopy, `canopy-update`, Anubis and the Caddy site (keeps repositories, config, the `git` account and the hardening) |
| `--dry-run` | print every step without changing anything |

## What gets installed

| Part | Where | Notes |
|---|---|---|
| canopy | `/usr/local/bin/canopy`, unit `canopy.service`, config `/etc/canopy/`, cache `/var/cache/canopy` | runs as the `canopy` user, listens on 127.0.0.1:8080, sandboxed by systemd; writes only the cache (`[theme]` fonts) |
| Caddy | distribution package, site in `/etc/caddy/Caddyfile.d/canopy.caddyfile` | HTTPS from Let's Encrypt, HSTS, HTTP/3; a customised Caddyfile is kept |
| Anubis | `/usr/local/bin/anubis` (a pinned, checksum-verified release), unit `anubis@canopy` | listens on 127.0.0.1:8923 |
| Repositories | `/srv/git` | folder `root:git` 1775, repositories owned by `git` |
| Followed repositories | units `canopy-mirror.service` and `.timer` | only with `[mirrors]` in the config; runs as `git` every 30 minutes, writing only to `/srv/git` |
| Self-update | `/usr/local/sbin/canopy-update`, Go in `/usr/local/lib/canopy-update/`, clone and caches in `/var/lib/canopy-update` | only with `--self-update`; root runs it, the build runs as a throwaway user (systemd `DynamicUser`) |
| `git` account | home `/srv/git`, shell `git-shell`, keys in `/etc/ssh/canopy_authorized_keys` | can only run git and a few commands |
| Hardening | `/etc/ssh/sshd_config.d/10-canopy-hardening.conf`, ufw or firewalld, unattended-upgrades or dnf-automatic, `/etc/sysctl.d/99-zz-canopy-hardening.conf`, `/etc/modprobe.d/canopy-hardening.conf` | |
| Tailscale | from Tailscale's signed package repository | |

[security.md](security.md) explains how these fit together.

## Undoing the hardening

If SSH ever locks you out, use the provider's recovery console (or a session
that is still open):

```sh
sudo ~/canopy/install.sh --undo-ssh-hardening
```

This returns sshd to the system's own settings (root and password logins as
they were before) and opens SSH in the firewall again. If that isn't
enough, turn the firewall off: `sudo ufw disable` (on Fedora,
`sudo systemctl stop firewalld`). Fix the problem, then deploy again.

To remove canopy: `sudo ~/canopy/install.sh --uninstall`.

## Troubleshooting

| Symptom | Likely cause and fix |
|---|---|
| No certificate, browser warning | DNS doesn't point at the server yet, or 80/443 are closed in the provider's firewall. `journalctl -u caddy` says which; Caddy retries by itself. |
| `502 Bad Gateway` | canopy or Anubis isn't running: `systemctl status canopy anubis@canopy`. |
| SSH times out | With Tailscale: your machine isn't in the tailnet, or the server dropped off it (key expiry). Check `tailscale status`. |
| `Permission denied (publickey)` | The key isn't allowed. For pushing, compare `ssh-add -L` with `/etc/ssh/canopy_authorized_keys`. |
| Root can't log in | Expected with an admin account: log in as it and use sudo. Root logins stay off on later runs; `--undo-ssh-hardening` turns them back on. |
| `Too many authentication failures` | Your SSH agent offers several keys and the server allows 3 tries. Set `IdentitiesOnly yes` and the right `IdentityFile` for the server in `~/.ssh/config` (see [usage.md](usage.md)). |
| A "Making sure you're not a bot" page | That's Anubis. It continues by itself after a moment. `git clone` and curl are never checked. |
| `install.sh: … is invalid` | A typo in `config/canopy.toml`; the message names the key. Check with `go run . -check`. |
| Another service on the server is unreachable | The firewall closed its port: `sudo ufw allow PORT/tcp`, or on Fedora `sudo firewall-cmd --permanent --add-port=PORT/tcp && sudo firewall-cmd --reload`. |
| Fedora: pushes or the site fail | Possibly SELinux, which is untested in enforcing mode: `sudo ausearch -m avc -ts recent`. |
