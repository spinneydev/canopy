# How canopy is protected

canopy is built for one owner and many anonymous readers. The owner pushes
over SSH; everyone else may only read, over HTTPS. Security comes from
keeping those two paths apart and giving each as little power as possible.

```
anyone --HTTPS--> Caddy --> Anubis --> canopy --read-----> /srv/git
                  :443      127.0.0.1  127.0.0.1               ^
owner --SSH (Tailscale only)--> sshd --> git account --write---+
owner --SSH (Tailscale only)--> sshd --> admin account --sudo--> root
```

## What is protected, and from whom

| What | Against | Where the line is |
|---|---|---|
| The site staying up | Anyone on the internet, scrapers | Caddy → Anubis → canopy's own limits |
| The repositories' contents | A stolen push key; anyone trying to write over HTTP | The `git` account; HTTP is read-only |
| Root on the server | Code running as `canopy` or `git`; anyone reaching SSH | Service accounts, systemd sandboxing, SSH settings |
| SSH itself | Password guessing, scanning | Firewall, Tailscale, key-only logins |

## The network: what can be reached at all

With `--harden`, the firewall (ufw on Debian and Ubuntu, firewalld on
Fedora) drops everything incoming except:

- **80 and 443** (TCP, plus UDP 443 for HTTP/3), for the web site;
- **SSH**, on whichever port sshd uses. With `--tailscale`, SSH is allowed
  only on the `tailscale0` interface, so from the internet the server
  answers on 80 and 443 and nothing else.

canopy (8080) and Anubis (8923, metrics on 8924) listen on 127.0.0.1
only, so they can't be reached from outside even without a firewall.

**Tailscale** is WireGuard underneath: only devices you have added to your
tailnet can open a connection, and your tailnet's access rules can narrow
that further (for example, only your laptop may reach port 22). Behind the
firewall there is a second, independent lock in sshd:

```
Match Address *,!100.64.0.0/10,!fd7a:115c:a1e0::/48,!127.0.0.0/8,!::1
    DenyUsers *
```

Any login from an address outside Tailscale's ranges (and localhost) is
refused, so SSH stays closed even if the firewall is turned off.
`ListenAddress` is deliberately not used: if sshd started before Tailscale
at boot, it would fail to start and lock everyone out.

A cloud provider's firewall can add a third layer: allow only 80 and 443
there too.

## The web side: read-only by design

canopy has no write path. It never runs `git push`, `receive-pack` or any
command that changes a repository, and it has no accounts or sessions.

- **Bot protection.** Anubis sits in front and gives browsers a short
  check (a no-JavaScript refresh by default, or proof-of-work), which keeps
  scrapers from crawling every blame and diff. The clone endpoints and
  non-browser clients (git, curl) pass straight through; canopy's own
  limits below cover them.
- **HTTPS.** Caddy gets certificates from Let's Encrypt and sends HSTS
  (`max-age=31536000`), so browsers that have visited keep to HTTPS.
- **Headers on every response.** Caddy adds a Content-Security-Policy,
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin` and
  `X-Frame-Options: DENY` to any response that lacks them, chiefly Anubis's
  challenge page, which sends none. Each is set only where missing, so
  canopy's own stricter headers below win on its pages. The challenge
  page's policy allows what Anubis needs (its scripts and proof-of-work
  worker from the site, inline styles, data: fonts) and blocks its inline
  retry helper, which it can do without. Scanners such as Mozilla's
  Observatory see the challenge page, as they look like browsers.
- **Cloning is fetch-only.** Smart HTTP goes through `git http-backend`
  with upload-pack only; `git-receive-pack` over HTTP is never routed, so
  nobody can push over HTTPS, with or without credentials.
- **No scripts.** Every page is sent with
  `Content-Security-Policy: default-src 'none'; style-src 'self'; font-src 'self'; img-src * data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'`,
  plus `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`.
  The interface uses no JavaScript, so the policy allows none: an injected
  script can't run.
- **Themes are colours and fonts only.** Colours in `[themes]` must look
  like CSS colours (no `url()`, `var()`, `;` or comments), highlighting
  styles must be known ones, and fonts must look like Google Fonts names. canopy downloads the fonts itself (the only outgoing
  connection it makes, besides mirrors) and serves them from the site, so
  visitors never contact Google. Of Google's stylesheet only woff2 files on
  `fonts.gstatic.com` and plain `font-style`, `font-weight` and
  `unicode-range` values are kept; the rules sent to browsers are written by
  canopy.
- **Escaping.** Pages are built with Go's `html/template`, which escapes
  everything from a repository (file names, commit messages, branch names).
- **Markdown** (READMEs and `.md` files) is rendered with goldmark and then
  cleaned with bluemonday, which removes scripts, event handlers and
  anything else outside a safe list of tags and attributes.
- **Raw files are never HTML.** `/blob/` serves images with their image
  type and everything else as `text/plain`, with `nosniff` and a `sandbox`
  CSP, so a repository can't host a page that runs in the site's origin.
  The custom favicon is served the same way.
- **Revisions are checked.** Anything a visitor types as a branch, tag or
  commit goes through `ResolveCommit`, which refuses a leading `-` or `:`
  (option and path injection), `@{` (reflog), `{/` (searching commit
  messages, which can be slow), NUL and newlines, and turns the rest into a
  full commit hash that is used from then on.
- **Paths stay inside the repository.** File paths are passed to git as
  tree paths of a resolved commit, never as file system paths.
- **A repository's config can't run programs.** canopy reads repositories
  it doesn't own, whose `config` could name programs for git to run. Every
  git call gets `log.showSignature=false`, `gpg.program=false` (and the
  x509, ssh and openpgp variants), `core.fsmonitor=false` and
  `core.hooksPath=/dev/null` through `GIT_CONFIG_*` environment variables,
  which win over the repository's own config. Blame and diffs use
  `--no-textconv`. Tarballs are compressed in Go rather than by a
  configurable `tar.*.command`, and every `filter.*` driver is switched off
  while archiving. `TestRepoConfigCannotRunCommands` in
  `internal/git/git_test.go` checks this with a hostile repository.
- **Signatures are checked in Go.** The `[signatures]` badges come from
  reading the raw commit or tag (`git cat-file --batch`, which runs
  nothing) and verifying its SSH signature in canopy itself, so the pins
  above stay as they are and no `ssh-keygen` or gpg runs. Only keys in the
  config count as verified; a repository can't add any.
- **Hidden things stay hidden.** `hidden = true` in the config, or
  `canopy.private` in a repository's own config (the SSH `private`
  command), removes a repository from every page, feed, the activity tab
  and HTTPS cloning: it is left out when the repositories are scanned, so
  no route can reach it. A `canopy.private` value that isn't a plain
  true or false counts as private. Dot-named folders are only served when
  they are bare repositories, so a version-controlled `~/.ssh` or work tree
  under the root is never exposed.

### Resource limits

Anubis doesn't check git clients, so canopy limits work on its own
(`internal/server/server.go`):

| Limit | Value |
|---|---|
| Concurrent page requests | `max(8, 4 × CPUs)`; a request waits at most 10 s for a slot, then gets 503 |
| Concurrent downloads (clones, tarballs, zips) | `max(4, CPUs)`, a separate pool with the same 10 s wait, so downloads can never starve page views |
| Time per page | 30 s |
| Time per download | 10 min |
| A download that stops moving | dropped after 20 s without progress |
| Upload body of a clone negotiation | 64 MB |
| Request headers | 10 s to arrive |
| File shown with highlighting | 2 MB; larger files are offered raw |

## The push side: the `git` account

Pushes arrive over SSH as the `git` account, which is built to do one thing:

- **Its shell is `git-shell`**, which runs only git's receive-pack,
  upload-pack and upload-archive, plus the root-owned helper commands in
  `/srv/git/git-shell-commands/` (`help`, `list`, `create`, `edit-desc`,
  `private`, `serve`, and `rename` and `delete`, which refuse unless
  `--manage-repos` was given). No
  shell, no terminal, no port, agent or X11 forwarding, no tunnels: every key
  is marked `restrict` and sshd's `Match User git` block turns these off
  again.
- **Its keys** are in `/etc/ssh/canopy_authorized_keys`, owned by root, so
  the account can't add keys for itself. Only the keys given with
  `--push-key`, and those fetched from GitHub with `--github-keys` at
  install time, are there.
- **Every login goes through `serve`** (sshd `ForceCommand`). On a push to a
  repository that doesn't exist, `serve` asks `create` to make it; anything
  else goes to `git-shell` exactly as sshd would have passed it. `serve`
  refuses to be called by name (git-shell would loop), and writes its
  messages to stderr because stdout carries the git protocol.
- **Repository names are checked** by `create` (and `rename`, `delete`,
  `edit-desc`, `private`) before anything is made or changed:
  letters, digits, `.`, `_` and `-` only, at most two folders, no `..`, no
  leading dot except on the last part, no newlines. A name like `../x`,
  `/abs`, `a/b/c/d` or one with quotes or `;` creates nothing.
- **Pushed data is checked.** `receive.fsckObjects` rejects corrupt or
  malformed objects. With `--deny-force-push` (the default when deploying),
  `receive.denyNonFastForwards` and `receive.denyDeletes` refuse pushes that
  rewrite or delete history, so a stolen key can add commits but not erase
  any. `--manage-repos` gives that up on purpose: any push key can then
  delete a repository, or rename it and push a different one under its
  name. Leave it off unless every key holder may do that. Mirrors are
  refused either way.
- **The account can't touch root's files.** `/srv/git` is owned by
  `root:git` with mode `1775`: `git` can create repositories in it, but the
  sticky bit stops it from removing or replacing the root-owned
  `.gitconfig` and `git-shell-commands/`.
- **New repositories** get a `post-receive` hook that points `HEAD` at the
  first branch pushed. It runs as `git`, which owns the repository anyway,
  so it adds no power. The web server never runs hooks
  (`core.hooksPath=/dev/null`).

## Admin access

- **Keys only.** `PasswordAuthentication` and keyboard-interactive logins
  are off, empty passwords refused, 3 attempts per connection, 30 s to log
  in.
- **A fixed list of accounts.** `AllowUsers` lists the accounts that had
  SSH keys on the first `--harden` run, plus `git` and the admin account.
  Later runs keep the list, so an account that gains a key later isn't let
  in silently; `--ssh-allow NAME` adds one deliberately.
- **No root logins.** With `--admin-user`, you log in as an ordinary account
  and use sudo, which asks for that account's password. `PermitRootLogin no`
  is written only once that account has a key, a password and sudo, so
  turning root off can never leave the server without an admin. Once off,
  root logins stay off on later runs, even ones without `--admin-user`;
  only `--undo-ssh-hardening` resets that. Without an admin account, root
  may log in only with a key. Giving `--admin-user` an existing account
  adds it to the sudo group, and the installer says so.

### Not locking yourself out

Hardening SSH on a remote server is the easiest way to lose it, so every
step checks before it commits:

- Key-only logins and `AllowUsers` are only switched on when an admin
  account already has a key. Otherwise the installer leaves passwords on
  and says why.
- Every sshd change is validated with `sshd -t`. If it fails, the new file
  is removed again and sshd is never reloaded with it. sshd is reloaded,
  never restarted, so open sessions survive.
- The installer reads the system's own SSH settings from a temporary copy of
  the configuration (`sshd -T -f`), never by moving the live files aside.
- The tailnet-only restriction is only written once `tailscale ip -4`
  returns a tailnet address.
- When deploying from your machine, the SSH connection used for the
  install stays open while a **new** login is tried (as the admin account,
  over Tailscale). If it fails, `install.sh --undo-ssh-hardening` runs over
  the open connection and puts SSH back the way the system had it.
- From the provider's console, the same `--undo-ssh-hardening` is the way
  back in.

## Secrets

| Secret | How it is handled |
|---|---|
| Tailscale auth key | Typed hidden (or from a file or `TS_AUTHKEY`). Uploaded in a mode-600 file for that one run, passed as `--auth-key=file:…` so it never appears in the process list, deleted afterwards. Never saved in `config/deploy.env` or `install-command.sh`. |
| Admin password | Typed hidden, twice. Hashed on your machine (`openssl passwd -6`); only the hash is uploaded (mode 600, deleted after the run) and set with `chpasswd -e`, never on a command line. |
| Anubis signing key | Generated on the server, kept in `/etc/anubis/canopy.env` (mode 600) across re-runs. A dry run prints a placeholder instead. |
| Your SSH private keys | Never leave your machine. Only public keys are uploaded. |

`--dry-run` never reads any of these.

## The installer

The installer runs as root and writes into a folder the `git` account can
write to, so it is careful about how it writes:

- Every file is written to a temporary file in the target folder and then
  renamed into place. A rename replaces a symbolic link rather than
  following it, so a link planted in `/srv/git` can't redirect a write made
  as root to another file. It refuses to write into a folder that is a link.
- `git-shell-commands/` is removed and recreated if it is ever a link or
  not owned by root.
- It only takes over a `git` account whose home and shell are the ones it
  would create.
- `--dry-run` shows every command and every file's contents before
  anything changes.

## Sandboxed services

canopy and Anubis run as their own system users under systemd sandboxing
(and Caddy too with `--harden`, while its Caddyfile is the installer's
own): read-only system and home folders, no new privileges, private `/tmp`
and devices, a system-call filter (`@system-service`), no writable and
executable memory, no kernel tuning, modules or logs, invisible other
processes, `UMask=0077`, and no capabilities except binding ports 80 and
443 for Caddy. `systemd-analyze security` rates all three "OK" (about 1.5–1.7).

With `--harden`, Caddy's admin API moves from `localhost:2019`, where any
local process could reconfigure Caddy, to a unix socket only root and Caddy
can open.

### Kernel settings

`--harden` also writes `/etc/sysctl.d/99-zz-canopy-hardening.conf`
(named to come after the distribution's own files, which it overrides) and
`/etc/modprobe.d/canopy-hardening.conf`, from Lynis's suggestions:

- `kernel.kptr_restrict = 2` hides kernel addresses, which exploits use;
  `kernel.sysrq = 0` turns off the magic SysRq keys;
  `fs.protected_fifos = 2` stops writing into other users' FIFOs in shared
  folders; `send_redirects = 0`, as the server isn't a router.
- The dccp, sctp, rds and tipc network protocols, which nothing here uses
  and which have had kernel bugs, can't be loaded.

Left alone on purpose: `rp_filter` (Tailscale needs loose mode),
`net.ipv4.ip_forward` (Tailscale exit nodes and subnet routers set it),
`kernel.modules_disabled` (it would stop WireGuard loading after boot), and
two that a service sets again at every boot, `log_martians` (ufw) and
`fs.suid_dumpable` (Ubuntu's crash reporter, apport). The installer reads
each value back and says if something else set it differently.

## Updates and software sources

- **Security updates** install automatically every day (unattended-upgrades
  or dnf-automatic). They never reboot: when `/var/run/reboot-required`
  exists, reboot by hand.
- **Anubis** is a pinned release whose SHA-256 is checked before
  installing.
- **Caddy** comes from the distribution's own packages; **Tailscale** from
  its signed package repository.
- **canopy's Go dependencies** can be checked with
  `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`.

## How this is tested

- `go test ./...` includes security regression tests: a repository whose
  config tries to run commands, special revision syntax, path handling.
- The installer is tested in systemd containers on Debian 13, Ubuntu 24.04
  and 26.04, and Fedora 44: install, re-run, hardening, firewall probes from
  a second container, the lockout guard and rollback, and pushing through
  the real sshd.
- Creating repositories on push, Tailscale-only SSH (with Tailscale
  stubbed) and the admin account were tested on Ubuntu 26.04: refused names
  create nothing, a login from outside the tailnet is refused, root is
  refused while the admin logs in and sudo needs the password, and
  re-deploys keep root off.
- The tailnet-only SSH restriction is checked with `sshd -T -C addr=…` for
  addresses inside and outside Tailscale's ranges.

**Not yet tested:** SELinux in enforcing mode, firewalld's Tailscale zone
on Fedora, and the newer features (above) on the other distributions.

## Known limits

- If the server drops off the tailnet (node key expiry, a Tailscale outage,
  a removed machine), SSH is only reachable through the provider's console.
  Disable key expiry for the server, or use a tagged key.
- A commit that was removed from every branch can still be opened by its
  full hash until `git gc` runs.
- Images in READMEs may load from other sites, which then see visitors'
  IP addresses.
- The firewall step only manages the default firewalld zone (plus the
  Tailscale zone); ports published by Docker bypass ufw.
- The web site is public: everything that isn't `hidden` can be read and
  cloned by anyone.
