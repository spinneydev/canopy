# Using your canopy server

Day-to-day git with a server set up by `install.sh`. The examples use:

| | Example | Yours |
|---|---|---|
| Web site | `https://git.example.com` | the domain you deployed |
| Server's SSH address | `server.your-tailnet.ts.net` | its Tailscale name (`tailscale status`), or the domain if SSH isn't on Tailscale |
| Admin account | `alice` | the admin account you chose when deploying |

Anyone can read and clone over HTTPS. Only you can push, over SSH, with a
key the server knows. With Tailscale, SSH works only from devices in your
tailnet.

## 1. A short name for the server

Instead of typing `git@server.your-tailnet.ts.net:project.git` every time,
give the server a short name in `~/.ssh/config` on each machine you push
from:

```sshconfig
# Pushing and fetching: the git account.
Host gitserver
    HostName server.your-tailnet.ts.net
    User git
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes

# Logging in to administer the server.
Host gitserver-admin
    HostName server.your-tailnet.ts.net
    User alice
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
```

Any name works in place of `gitserver`. `IdentityFile` is the private key
whose public half the server accepts. Now:

```sh
ssh gitserver list                         # your repositories
git clone gitserver:project.git            # instead of git@server.your-tailnet.ts.net:project.git
ssh gitserver-admin                        # an admin shell (then sudo)
```

Other ways to get a short name:

- **Tailscale's short name.** With MagicDNS on (the default), devices in
  the tailnet can use the machine name alone: `git@server:project.git`.
- **Rename the machine** in the Tailscale admin console (Machines → the
  server's menu → Edit machine name), for example to `git`. It is then
  `git.your-tailnet.ts.net`, or just `git`.
- **A git URL prefix**, if you want even shorter remotes:
  `git config --global url."gitserver:".insteadOf "gs:"`, then
  `git clone gs:project`.

The rest of this guide uses `gitserver`.

## 2. Put a project on the server

Pushing to a name that doesn't exist yet creates the repository:

```sh
cd ~/src/project
git remote add origin gitserver:project.git
git push -u origin main
git push origin --tags
```

The server answers `created repository project`, and the project shows on
the web site within about 15 seconds. The first branch you push becomes
the default branch, so a project whose main branch is `master` works as it
is.

To push every branch and tag at once: `git push -u origin --all` then
`git push origin --tags`.

**Names** may contain letters, digits, `.`, `_`, `-`, and up to two folders:
`tools/cli.git` is created in the folder `tools/` and listed under
**tools** on the index. `.git` at the end is optional.

**A description** shows on the index and the summary page. Give it when
creating the repository:

```sh
ssh gitserver create project "A short description"
```

or later, in `config/canopy.toml` (then re-deploy):

```toml
[repos."project"]
description = "A short description"
homepage = "https://example.com/project"
```

## 3. Clone

Anyone, from anywhere, read-only:

```sh
git clone https://git.example.com/project
```

You, read-write (from a tailnet device, with Tailscale):

```sh
git clone gitserver:project.git
```

## 4. Pull and push

These are ordinary git commands; nothing about them is canopy-specific:

```sh
git pull                  # fetch and merge the server's changes
git push                  # send yours
git push origin feature   # push another branch
git fetch --all --prune   # update every remote, forget deleted branches
```

### Reading over HTTPS, pushing over SSH

On a machine that should pull from anywhere but only push from inside the
tailnet, clone over HTTPS and set only the push address to SSH:

```sh
git clone https://git.example.com/project
cd project
git remote set-url --push origin gitserver:project.git
git remote -v
# origin  https://git.example.com/project (fetch)
# origin  gitserver:project.git (push)
```

`git pull` then works from any network; `git push` needs the tailnet.

### Pushing to two places

To keep a copy on another host as well (GitHub, Codeberg, …), give
`origin` two push addresses:

```sh
git remote set-url --add --push origin gitserver:project.git
git remote set-url --add --push origin git@github.com:you/project.git
git push            # goes to both
```

## 5. Rules the server enforces

If the server was deployed with "refuse pushes that rewrite or delete
branches" (the default):

- `git push --force` and pushes that rewrite history are refused:
  `! [remote rejected] main -> main (non-fast-forward)`. Fix history with a
  new commit (`git revert`) instead.
- Deleting a branch or tag (`git push origin --delete old`) is refused.
  An admin can still do it on the server (section 7).

Every pushed object is checked, so a corrupt push is rejected.

## 6. Commands

```sh
ssh gitserver help                          # what the account can do
ssh gitserver list                          # all repositories, private ones marked
ssh gitserver create NAME [description]     # an empty repository
ssh gitserver create NAME --private         # one that stays off the web site
ssh gitserver edit-desc NAME                # show its description
ssh gitserver edit-desc NAME "New text"     # set it
ssh gitserver private NAME                  # is it private?
ssh gitserver private NAME on               # take it off the web site
ssh gitserver private NAME off              # put it back
```

A **private** repository isn't listed, browsable or clonable over HTTPS,
and doesn't count on the activity tab; over SSH you push and fetch as
usual. The site follows within 15 seconds, with no config change or
deploy. To push something private, create it with `--private` first:
pushing to a name that doesn't exist yet creates a public repository.

If the server was deployed with "let push keys rename and delete
repositories" (`--manage-repos`, off by default), two more:

```sh
ssh gitserver rename OLD NEW                # e.g. rename tool lab/tool
ssh gitserver delete NAME                   # says what would go
ssh gitserver delete NAME --yes             # deletes it, with no undo
```

A renamed repository keeps its history; point existing clones at the new
name with `git remote set-url origin gitserver:NEW.git`. Settings under
`[repos."OLD"]` in the config don't follow it. Mirrors can't be renamed or
deleted this way: change `[mirrors]` in the config instead.

The `git` account runs nothing else: no shell, no terminal, no port
forwarding.

## 7. Administering repositories

Repositories live in `/srv/git` on the server, owned by the `git` account.
Without `--manage-repos`, or for anything the commands in section 6 don't
cover, log in as the admin account and use sudo:

```sh
ssh gitserver-admin
sudo -u git mv /srv/git/old.git /srv/git/new.git            # rename
sudo -u git git -C /srv/git/project.git branch -D old       # delete a branch
echo "New description" | sudo -u git tee /srv/git/project.git/description
sudo rm -rf /srv/git/project.git                            # delete (no undo)
```

To hide a repository from the web site and HTTPS cloning without deleting
it, make it private (`ssh gitserver private NAME on`, section 6), or set
`hidden = true` for it in `config/canopy.toml` and re-deploy. You can
still push to and fetch from it over SSH.

## 8. Pushing from another computer of yours

Each of your machines pushes with its own key. Only you can allow a new
one: the list of push keys is a root-owned file on the server, and the only
way to change it is to deploy again with admin access. Nobody can add a key
through the web site or the `git` account.

1. On the new machine, make a key if it has none: `ssh-keygen -t ed25519`.
2. Copy its **public** half (`~/.ssh/id_ed25519.pub`) to the machine you
   deploy from (USB stick, Taildrop, `scp`). Never copy the private file,
   the one without `.pub`.
3. Deploy again, without `--yes`, and list every key file when asked:

   ```sh
   ./install.sh alice@server.your-tailnet.ts.net
   #   Public key files allowed to push, comma-separated: ~/.ssh/id_ed25519.pub,~/keys/desktop.pub
   ```

The list you give replaces the old one, so a key you leave out can no longer
push.

If your keys are on GitHub, you can skip copying them: give your GitHub user
name when asked "Also allow the SSH keys of GitHub users to push" (or pass
`--github-keys USER`). Each deploy has the server fetch
`https://github.com/USER.keys` again, so a key added to or removed from
GitHub counts from the next deploy. Answer yes to "Keep them in sync"
(`--github-sync`) and you needn't deploy at all: the server checks GitHub
every 15 minutes, so a new laptop's key works a quarter of an hour after you
add it on GitHub, and a key you remove there stops working as soon. If
GitHub can't be reached, the keys stay as they were. Anyone who can add keys
to that GitHub account can then push here too.

The server remembers which account the name belonged to, by its ID, the
first time (in `/etc/canopy/github-ids`), and keeps taking the keys of that
account: renaming yours on GitHub changes nothing here, and if someone
registers the name you gave up, their keys don't count. Once the account is
deleted, the sync drops its keys at its next check, and deploys stop, saying
why, until you leave the name out. To switch to a new account under the same
name, remove the name's line from that file on the server and deploy again.

The same works for logging in to the server: "GitHub user whose SSH keys can
also log in as alice" (`--admin-github USER`) adds that user's GitHub keys to
the admin account, and with `--github-sync` keeps them current. sudo still
asks for the account's password. The sync never takes the account's last key
away (that would lock you out); keys from `--admin-key`, and lines you added
yourself (with options like `from="…"` too), always stay.

And for signature badges: answer yes to "Show their commits and tags signed
with an SSH signing key they keep on GitHub as verified" and the deploy adds
`[signatures.github]` to `config/canopy.toml` (see
[config/README.md](config/README.md#signatures)). On GitHub, add the key a
second time under **Settings → SSH and GPG keys → New SSH key** with
**Key type: Signing Key**; a key added only for authentication doesn't
count. With Tailscale, the new machine must also be in the tailnet (and
allowed by your tailnet's access rules) before it can reach SSH.

Anyone whose key is on the list can push to every repository and create new
ones (but not rewrite history, when force pushes are refused), so add only
keys of machines you control.

## When something goes wrong

| Message | Cause and fix |
|---|---|
| `Connection timed out` | With Tailscale: this device isn't in the tailnet, or Tailscale is off. Check `tailscale status`. Without: a firewall or the wrong host name. |
| `Permission denied (publickey)` | The server doesn't know this key. Check `ssh -v gitserver` shows the right key, and add it (section 8). |
| `Too many authentication failures` | Your SSH agent offers several keys before the right one, and the server allows 3 tries. Keep `IdentitiesOnly yes` and the right `IdentityFile` in the `gitserver` entry (section 1). |
| `create: 'x' isn't a valid name` | The name has characters other than letters, digits, `. _ -`, or more than two folders. |
| `non-fast-forward` | Someone (or another machine) pushed first: `git pull`, then push again. If you rewrote history, see section 5. |
| `fatal: unrecognized command` | The `git` account only runs git and the commands in section 6. |
| The repository doesn't show on the web site | Wait 15 seconds and reload. A hidden repository never shows. |
| Clone over HTTPS says `not found` | The name is case-sensitive, and hidden repositories can't be cloned over HTTPS. |
