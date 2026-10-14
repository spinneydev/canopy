# Developing and testing locally

canopy is a plain Go program with its own web server, so it runs on your
own machine as it is. Everything here works on Linux, macOS and Windows
unless it says otherwise.

## What you need

- Go 1.26 or newer
- git

## Run the demo

```sh
go run ./demo                        # builds sample repositories in demo/repos
go run . -config demo/canopy.toml   # http://127.0.0.1:8080
```

The demo has something for most pages: a C project with tags, merges, a
rename and an open branch (`tinyhttpd`), a `.dotfiles` repository with a
symlink and an executable script, markdown notes with relative links and an
SVG (`notes`), a nested name with a binary file (`lab/raytracer`), a
snapshot of canopy itself, an empty repository (`scratch`) and one hidden
by the config (`secret`, which must answer 404).

With `ssh-keygen` on the path (it comes with git on Windows), the demo signs
robin's commits and tags with a key the demo config lists, and kim's with
one it doesn't. The `tinyhttpd` log then shows **verified** and **signed**
badges; sam's commits are unsigned and have none.

`go run ./demo` deletes and rebuilds `demo/repos` every time, so anything
you put there yourself is lost on the next run.

Stop the server with Ctrl+C. Templates and CSS are built into the program,
so after changing them stop it and run `go run .` again; there is no live
reload.

## Run it on your own repositories

```sh
go run . -root ~/src -listen 127.0.0.1:8080
```

`-root` is a folder of repositories (bare ones like `foo.git`, or work
trees). Without `-config`, canopy reads `config/canopy.toml` if it
exists; flags override what's in it.

`-listen 127.0.0.1:8080` only accepts connections from this machine. Use
`-listen :8080` to try it from a phone on the same network.

## Try a theme ([theme])

Add a `[theme]` to a copy of the demo config (keep it in `demo/`, so
`root` and `about` still resolve), with another port so it can run next to
the demo:

```toml
# demo/theme-test.toml: demo/canopy.toml plus
listen = "127.0.0.1:8099"

[theme]
light = "ayu-light"
dark = "my-dark"
sans = "Inter"
mono = "JetBrains Mono"

[themes.my-dark]
base = "night-owl"
highlight = "dracula"
link = "#ff79c6"
```

Look at a highlighted file (`/tinyhttpd/tree/main/item/src/request.c`), a
commit and the activity tab in both modes, for each built-in theme you
touch. Adding a built-in theme is described at the end of
[customisation.md](customisation.md).

The first start logs `font "Inter": downloaded 14 files`; later starts read
the fonts from the cache and log nothing. The cache is the user's cache
folder (`~/.cache/canopy/fonts`, `%LocalAppData%\canopy\fonts`), or
`$CACHE_DIRECTORY/fonts` if set: set it to a scratch folder to test a first
download, or delete the folder. Check that a misspelt family logs "not on Google Fonts" and leaves the system fonts in
place.

## Try followed repositories ([mirrors])

Use a scratch folder and a config of its own, so the clones don't end up in
your real repositories:

```toml
# mirror-test.toml
root = "/tmp/mirror-test"
listen = "127.0.0.1:8080"
owner = "you"

[mirrors]
"BurntSushi/toml" = "https://github.com/BurntSushi/toml.git"
"yuin/goldmark" = "https://github.com/yuin/goldmark.git"
```

```sh
go run . -config mirror-test.toml -mirror   # first run: "cloned in 3s"
go run . -config mirror-test.toml -mirror   # second run: "up to date"
go run . -config mirror-test.toml           # browse them
```

To see an update, move a branch back and sync again; it should say
"updated":

```sh
git -C /tmp/mirror-test/BurntSushi/toml.git update-ref refs/heads/master refs/heads/master~3
go run . -config mirror-test.toml -mirror
```

A mirror shows "mirror of" on its summary page, has no `.tar.gz` links,
and refuses `git clone` and downloads with a 403. Its folder (`BurntSushi`)
is its owner on the index.

Each `-mirror` run records its result, and the summary page shows it
under "mirror of": "synced just now", or in red with the error. To see a
failure, sync once with a config whose URL doesn't exist; the next good
sync clears it. To see the two-hours warning, backdate the last sync:

```sh
git -C /tmp/mirror-test/yuin/goldmark.git config canopy.synced $(($(date +%s) - 3*3600))
```

Keep test mirrors small. A big project like the Linux kernel takes several
GB and a long time to clone.

## Things worth checking by hand

- **The index**: sorting by name and by idle, search, the Owner and Last
  committer columns.
- **A narrow window**: at 700 px or less the index shows only Name,
  Description and Idle. Use the browser's phone view (F12, then the phone
  icon).
- **Pagination**: the log (`/repo/log`) and tags on the refs page
  (`/repo/refs`), 20 per page. A repository with many tags, like a mirror
  of goldmark, has several pages.
- **Cloning**: `git clone http://127.0.0.1:8080/notes` works; cloning a
  mirror is refused.
- **Hidden repositories**: `secret` in the demo is neither listed nor
  reachable, nor counted on the activity page.
- **Activity** (`/activity`): the graph's month names line up with the
  squares, hovering a square shows its date and count, and the newest
  commits and tags are listed. Mirrors never appear. The years list switches
  to a calendar year (January to December, or to today for this year), and
  more than 10 events get newer/older links. At 700 px or less the graph
  scrolls sideways, starting at its last day. Check light and dark mode.
- **Signatures**: in the demo, `/tinyhttpd/log` has badges, a commit page
  has a **signature** line, and both tags on `/tinyhttpd/refs` are
  verified. Without `[signatures]` in the config there are no badges at
  all. Bad signatures are covered by `go test ./internal/sigcheck`.
- **Licenses**: `/tinyhttpd` shows **license** MIT in the sidebar, linking
  to its LICENSE file. A license file whose text isn't one of the common
  licenses canopy knows shows its name instead.
- **Feeds**: `/tinyhttpd/log.atom` and `/tinyhttpd/tags.atom` are valid
  Atom with absolute links, and a feed reader finds them from the
  repository's page.

## Tests and checks

Before committing, these must all be clean:

```sh
go test ./...
go vet ./...
gofmt -l .
shellcheck install.sh update.sh
```

`gofmt -l .` prints the files it would change; no output means clean.

`canopy-update` (`install.sh --self-update`) needs root and systemd, which
WSL has. To try it without installing canopy, run install.sh's
"canopy-update" section with its paths pointed at a folder like
`/opt/gsu-test` (not under `/tmp`: the build runs with a private `/tmp`), a
throwaway `canopy.service` running a binary there, and `nobody` as the
service account. Then check: a modified build refuses without a release
named, `--check` changes nothing, a named release switches (older ones
too), a config the new build rejects leaves the old one running, and
running it again says there's nothing to do. Remove the unit,
`/opt/gsu-test` and `/var/lib/private/canopy-update*` afterwards.

## Windows

canopy, the demo and the Go tests run natively in PowerShell. A few
things behave differently:

- **`gofmt -l .` lists every Go file.** With git's default
  `core.autocrlf=true` the checkout has CRLF line endings, and gofmt wants
  LF. Git stores LF, so commits are fine; check formatting from WSL, or on
  a copy with LF endings.
- **One config test fails on Windows.** `TestFileSettingsAreChecked`
  expects Linux's "no such file" error text, and Windows says "The system
  cannot find the file specified". It passes on Linux.
- **Run the full checks in WSL** on a clone:

  ```sh
  git clone /mnt/c/path/to/canopy ~/canopy-check
  cd ~/canopy-check && go test ./... && go vet ./... && gofmt -l .
  ```

  A clone only has committed changes; commit first, or copy the working
  tree instead.
