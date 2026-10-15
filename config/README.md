# Configuring canopy

All settings live in [`canopy.toml`](canopy.toml) in this folder. Edit it,
check it, then apply it:

```sh
$EDITOR config/canopy.toml
./canopy -check                 # validate and show the resulting settings
sudo ./install.sh --domain git.example.com   # install or update the server
```

`install.sh` copies this file to `/etc/canopy/canopy.toml` (keeping the
previous version as `canopy.toml.bak`) and restarts canopy. This folder is
the place to make changes: edits made directly in `/etc/canopy` are
overwritten the next time the installer runs.

## How canopy finds the file

1. `-config path/to/file.toml`, if given.
2. Otherwise `config/canopy.toml`, relative to the directory canopy is
   started from.
3. Otherwise built-in defaults.

The installed service always runs with
`-config /etc/canopy/canopy.toml`.

Command-line flags (`-listen`, `-root`, `-depth`, `-title`, `-clone-http`,
`-git`) override the file, which is handy for a quick test:

```sh
./canopy -root ~/src -listen :9000
```

## Checking the file

`./canopy -check` loads the file exactly as the server would and prints the
result, without starting anything:

```
config: config/canopy.toml
listen: 127.0.0.1:8080
root: /srv/git
depth: 3
title: git
header: git
clone.http:
```

Misspelled or unknown keys are an error rather than being ignored:

```
config/canopy.toml: unknown keys: titel
```

## Settings

| key | default | meaning |
|---|---|---|
| `listen` | `":8080"` | Address and port to listen on. `127.0.0.1:8080` only accepts local connections (use this behind Caddy); `:8080` accepts them from anywhere. |
| `root` | `"."` | Folder that holds the repositories. Relative paths are resolved against the config file's folder. |
| `depth` | `3` | How many folders deep under `root` to look for repositories. |
| `title` | `"canopy"` | Site name in the browser tab and footer, and in the page header unless `header` is set. |
| `header` | the `title` | Site name in the page header, when it should differ from the browser tab's. |
| `description` | `""` | Tagline under the title on the index page. Markdown, so it can hold links: `"Personal git server for [Alice](https://example.com)"`. Unsafe HTML is removed, as on the about page. |
| `git` | `"git"` | Path to the git program, if it isn't on `PATH`. |
| `about` | none | Markdown file shown under an **about** tab next to **index** (like cgit's `root-readme`). Starts as [`about.md`](about.md) in this folder. Remove the setting to drop the tab. |
| `favicon` | built-in icon | The browser tab icon: an `.svg`, `.png` or `.ico` file. |
| `owner` | none | Owner shown on the index for repositories at the top of `root`. One in a folder is owned by that folder, so `torvalds/linux.git` shows `torvalds`; `[repos."name"] owner` overrides both. A folder named like `owner` (`alice/…` with `owner = "alice"`) is listed right after the top-level repositories, before the other folders. |
| `owner_url` | none | The owner's website, starting with `https://` or `http://`. The footer then says "served by" `owner`, linking here, instead of the site's `title` linking to the index. Needs `owner`. |
| `tree_commits` | `false` | `true` to show, on tree pages, the last commit that changed each file or folder (its subject and age), like GitHub's file list. It costs one `git log` per tree page; on a long history, entries untouched in the last 5000 commits of that folder are shown without one. The subject is hidden on narrow screens. |

`about` and `favicon` are paths relative to this file and are checked when
canopy starts (they must exist, have one of the listed extensions, and be at
most 1 MiB). Keep them in this folder: the installer copies them next to the
installed config. Like the config, edit them here and re-run the installer;
the about page is read on every request, so changes show up without a
restart. Links in it like `[myproject](myproject)` lead to that repository. While the about page is on,
a repository named `about` can't be reached at `/about`.

### `[clone]`

| key | default | meaning |
|---|---|---|
| `http` | address of the request | Base URL shown for `git clone`, e.g. `"https://git.example.com"`. Set it when canopy is behind a proxy that changes the host name. |

Cloning is read-only over HTTP. Pushing goes over SSH, outside canopy.

### `[activity]`

An **activity** tab next to **index**, like a GitHub profile: the past year
of commits as a graph of days (darker on busier days), the repositories they
went to, and the newest commits and tags. Mirrors never count: they are other
people's work. Hidden repositories don't either.

| key | default | meaning |
|---|---|---|
| `enabled` | `false` | `true` to show the tab, at `/activity`. |
| `emails` | everyone | Author emails whose commits count, e.g. `["you@example.com", "123+you@users.noreply.github.com"]`. Leave it out on a server where every commit is yours. |

```toml
[activity]
enabled = true
emails = ["you@example.com"]
```

A **years** list next to the graph shows any calendar year with commits
instead (`/activity?year=2025`), and the commits and tags are listed 10 to a
page. Commits on every branch count, on the day they were written (UTC), as
on GitHub. The repositories are read again at most once a minute. While the
tab is on, a repository named `activity` can't be reached at `/activity`.

### `[signatures]`

Signature badges on commits (log and commit pages) and annotated tags (refs
and tag pages), like GitHub's. Off until a key is listed.

| key | default | meaning |
|---|---|---|
| `keys` | none | Trusted SSH public keys, by the name shown on the badge: `name = ["ssh-ed25519 AAAA… comment", …]`, one key per entry, as in `authorized_keys` but without options. |

```toml
[signatures.keys]
alice = ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA… alice@laptop"]
```

| badge | meaning |
|---|---|
| **verified** (green) | a good SSH signature by one of these keys |
| **signed** (grey) | a good SSH signature by a key not listed, or a PGP or X.509 signature (not checked) |
| **bad signature** (red) | an SSH signature that doesn't match: the commit or tag was changed after signing, or the signature is forged |

Unsigned commits get no badge. Hovering a badge, or the commit page's
**signature** line, says which key signed (its `SHA256:` fingerprint, as
`ssh-keygen -lf key.pub` prints it). canopy checks signatures itself, in
Go; it never runs `ssh-keygen` or gpg for this. Verified means a listed key
signed exactly that commit or tag. Whether that key's owner is the author
shown is up to who signs: git signs with the key of whoever commits.
`allowed_signers` options such as `valid-after` aren't supported, and PGP
signatures aren't checked. `-check` lists each key's name and fingerprint.

To sign your own commits and tags with an SSH key:

```sh
git config gpg.format ssh
git config user.signingKey ~/.ssh/id_ed25519.pub
git config commit.gpgSign true   # every commit; or git commit -S
git config tag.gpgSign true      # annotated tags; or git tag -s
```

### `[theme]`

The site's colours and fonts. Nothing else of the look can be changed: the
values are checked to be theme names, colours and font names, so a theme
can't add other CSS. [customisation.md](../customisation.md) walks through
picking and making themes, with tips and previews.

| key | default | meaning |
|---|---|---|
| `light` | `"default"` | The colour theme for visitors whose system is in light mode: a built-in one (below) or one of yours from `[themes]`. |
| `dark` | `"default"` | The same for dark mode. |
| `sans` | system font | A [Google Fonts](https://fonts.google.com) family for text, e.g. `"Inter"`. |
| `mono` | system monospace | A Google Fonts family for code, hashes and file lists, e.g. `"JetBrains Mono"`. |

```toml
[theme]
light = "catppuccin-latte"
dark = "catppuccin-frappe"
sans = "Inter"
mono = "JetBrains Mono"
```

The site follows each visitor's light or dark setting, so it always shows
one of the two. Built-in themes, each with matching syntax highlighting:

| light | dark |
|---|---|
| `default` (canopy's own) | `default` (canopy's own) |
| `github-light` | `github-dark` |
| `ayu-light` | `ayu-mirage` |
| `light-owl` | `night-owl` |
| `catppuccin-latte` | `catppuccin-frappe` |

Any theme can go in either slot: `dark = "github-light"` keeps the site
light for everyone. `./canopy -check` prints the two in use.

**Fonts** are downloaded by canopy, not by visitors: when it starts it
fetches the family from Google (regular to bold, upright and italic, every
script it has) and serves it from `/_static/fonts/`, so no page makes a
request to Google. The name must be written as on fonts.google.com. Until the
download has finished, and if it fails, pages use the system fonts; a
failed download (no network yet) is retried in the background, a name
Google doesn't know is logged once (`journalctl -u canopy`). The files are
kept in `/var/cache/canopy/fonts` (`$CACHE_DIRECTORY/fonts`, or the user's
cache folder when run by hand), so later starts don't need Google; delete
that folder to download a family again.

### `[themes."name"]`

Your own colour themes, picked by name in `[theme]` like the built-in ones.
Start from a built-in theme with `base` and change only some colours, or
leave `base` out to start from `default` (then any colour you don't set is
canopy's own for the mode the theme is used in).

```toml
[theme]
dark = "my-dark"

[themes.my-dark]
base = "night-owl"          # optional: where the other colours come from
highlight = "dracula"       # optional: syntax highlighting, else the base's
link = "#ff79c6"
header-bg = "#21222c"
act-4 = "#ff79c6"
```

Names are lower case letters, digits and `-`, and can't be a built-in
name. `highlight` is a [chroma style](https://xyproto.github.io/splash/docs/)
such as `dracula`, `monokai`, `nord`, `onedark`, `gruvbox`, `rose-pine`,
`tokyonight-night` or `solarized-light`, or one of the built-in themes'
names; a wrong name lists them all. A colour is `#rgb`, `#rrggbb` (with
optional alpha), `rgb()`, `hsl()`, `oklch()` and the like, or a CSS colour
name. The colours:

| name | used for |
|---|---|
| `bg`, `fg` | page background and text |
| `muted` | secondary text: dates, owners, line numbers |
| `link`, `link-hover` | links |
| `border` | lines and boxes |
| `header-bg` | the top bar and file headers in diffs |
| `event-bg` | commit boxes, table row hover |
| `code-bg` | source, diffs and code blocks |
| `btn-bg`, `btn-hover` | buttons and the active year on the activity tab |
| `target` | highlighted lines, from a `#L12` or `?lines=12-20` link |
| `add`, `add-bg`, `del`, `del-bg` | added and removed lines in diffs |
| `hunk` | `@@` lines in diffs, renames |
| `branch-bg`, `tag-bg` | branch and tag labels |
| `act-0` … `act-4` | the activity graph, from no commits to the busiest day |

### `[mirrors]`

Repositories you follow: each line names a repository and the URL it is
copied from. The installer sets up a timer that keeps them up to date: a
repository that doesn't exist yet is cloned, and every 30 minutes the rest
fetch whatever is new. Only branches and tags are copied.

```toml
[mirrors]
"torvalds/linux" = "https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git"
"neovim/neovim" = "https://github.com/neovim/neovim.git"
```

Put a mirror in a folder named after its owner, as above, and the index
shows that owner. Mirrors can be browsed but not cloned over HTTP or
downloaded as `.tar.gz` or `.zip`: both are refused with a message naming the
upstream URL, which the summary page shows as "mirror of" instead of a
clone address. Below it is the last successful update ("synced 12 minutes
ago"), in red with the error when the last one failed, or when there has
been none for two hours. URLs must start with `https://`, `http://` or `git://`,
and can't hold a user name, password or token: the summary page shows them.
The first clone runs in the background after a deploy; follow it with
`journalctl -u canopy-mirror -f`, or run it by hand with
`canopy -config canopy.toml -mirror`. Removing a line stops updating that
repository but keeps it; delete its folder to remove it. Don't push to a
mirror: the next update overwrites its branches.

### `[repos."name"]`

Settings for one repository. The name is the repository's path under `root`
without `.git`, as it appears in the URL:

| on disk | name |
|---|---|
| `/srv/git/tinyhttpd.git` | `tinyhttpd` |
| `/srv/git/tools/foo.git` | `"tools/foo"` |
| `/srv/git/.dotfiles.git` | `".dotfiles"` |

Quote names that contain `/` or `.`.

| key | meaning |
|---|---|
| `description` | Shown in the header and index, instead of the repository's own `description` file. |
| `homepage` | A website link shown on the repository's summary page. |
| `hidden` | `true` to leave the repository out entirely: not listed, not browsable, not clonable. The same can be done without a config change or deploy: `ssh git@HOST private NAME on`, or `create NAME --private` (see [usage.md](../usage.md#6-commands)). |
| `owner` | Shown as its owner on the index, instead of its folder or the site's `owner`. |

```toml
[repos.tinyhttpd]
homepage = "https://example.com/tinyhttpd/"

[repos."lab/raytracer"]
description = "A weekend ray tracer."

[repos.secret]
hidden = true
```

## Where repositories come from

canopy lists every git repository under `root` (bare `foo.git` folders and
ordinary checkouts with a `.git` inside), up to `depth` folders deep. New
repositories show up within about 15 seconds, without a restart.

- A repository's description comes from the `description` file inside it
  (`foo.git/description`), unless the config sets one.
- Repositories in subfolders are grouped into sections on the index page:
  `tools/foo` is listed as `foo` under **tools**. Repositories at the top
  of `root` come first, then the folder named like the site's `owner`
  (with `owner = "alice"`, **alice**), then the other folders alphabetically.
- A repository is left out, like `hidden`, when its own config has
  `canopy.private = true`, which the SSH commands `private` and
  `create --private` set.
- Folders whose names start with `.` are only served if they are bare
  repositories (`.dotfiles.git`); other hidden folders, such as `.ssh`, are
  skipped.
- The canopy service runs as its own user, which needs read access to
  `root`. Folders inside home directories usually aren't readable by other
  users, so `/srv/git` is a better place than `~/git`.

## Examples

Behind Caddy, which `install.sh --domain` sets up:

```toml
listen = "127.0.0.1:8080"
root = "/srv/git"
title = "git.example.com"
description = "Small tools and experiments."

[clone]
http = "https://git.example.com"
```

Serving directly on a home network, without Caddy:

```toml
listen = ":8080"
root = "/srv/git"
title = "homelab git"
```
