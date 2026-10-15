# canopy

A small, read-only web viewer for git repositories, in the spirit of cgit,
with a sourcehut-style look. One Go binary with its templates and CSS
embedded. Pages need no JavaScript.

It is meant for a personal git server: you push over SSH, and everyone else
can browse and clone over HTTPS. It is not a forge: there are no accounts,
issues or pull requests.

- **Browse:** an index of every repository (grouped by folder), summaries
  with the README and the license, trees with syntax highlighting, rendered markdown, raw
  files, blame, history, commits with diffs and `.patch` downloads, refs,
  tags with their release notes, and tarballs.
- **Clone** anything over HTTPS (`git clone https://git.example.com/NAME`).
  Pushing over HTTP is never possible.
- **Push** over SSH to a locked-down `git` account. Pushing to a name that
  doesn't exist yet creates the repository.
- **Deploy** with one command from your own machine:
  `./install.sh root@your-server` sets up canopy, HTTPS, bot protection,
  pushing and a hardened server, and can put SSH on a private
  [Tailscale](https://tailscale.com) network so that only the web site is
  public.

## Documentation

| File | What's in it |
|---|---|
| [usage.md](usage.md) | Day to day: cloning, pushing, pulling, new repositories, a short SSH name for the server |
| [deploy.md](deploy.md) | Putting it on a server, from a fresh VPS to a hardened setup with backups |
| [security.md](security.md) | How the server is protected, layer by layer |
| [config/README.md](config/README.md) | Every setting in `config/canopy.toml` |
| [customisation.md](customisation.md) | Themes, your own colours and fonts, step by step |
| [development.md](development.md) | Running and testing it on your own machine, including Windows |

## Try it locally

    go run ./demo                        # sample repositories in demo/repos
    go run . -config demo/canopy.toml   # http://127.0.0.1:8080

Both work on Linux, macOS and Windows; the demo needs only Go and git.

The demo has a C project with releases, merges and branches, a
`.dotfiles` repository with symlinks, markdown notes with images, a nested
repository, an empty one, and one hidden by the config.

To serve your own folder of repositories:

    go build
    ./canopy -root ~/src -listen 127.0.0.1:8080

## Deploying

From the canopy folder on your machine (needs Go and SSH access to the
server):

    ./install.sh root@git.example.com

It asks a few questions; the defaults are the most secure choices. It then
builds canopy for the server, uploads it and installs:

    internet --> Caddy :443 (HTTPS) --> Anubis (bot check) --> canopy 127.0.0.1:8080
    you      --> SSH (over Tailscale) --> git account (git-shell) --> /srv/git

Before finishing it checks that a fresh SSH login and pushing still work,
and undoes the SSH changes if they don't. Later, `./install.sh … --yes`
re-deploys with the same answers. [deploy.md](deploy.md) walks through
the whole thing, including running the installer on the server by hand.

Tested on Debian 13, Ubuntu 24.04 and 26.04, and Fedora 44.

### Who can reach what

| | No hardening | `--harden` | `--harden --tailscale` |
|---|---|---|---|
| Web site and `git clone` over HTTPS (80, 443) | Internet | Internet | Internet |
| Push over HTTP | Never | Never | Never |
| Push over SSH (`git` account) | Internet, push keys only | Internet, push keys only | **Tailnet only**, push keys only |
| Admin SSH logins | Internet, the system's own sshd settings | Internet, keys only, listed accounts only | **Tailnet only**, keys only, listed accounts only |
| Root over SSH | The system's own setting | Never with a password; never at all with an admin account | Same, and tailnet only |
| Other ports | The firewall is left as it is | Blocked | Blocked from the internet |
| canopy and Anubis | This machine only | This machine only | This machine only |

[security.md](security.md) explains each of these.

## Configuration

Everything is in [`config/canopy.toml`](config/canopy.toml):

- `listen`, `root` (the folder of repositories), `depth`, `title`,
  `description`, `about` (a markdown page for an **about** tab), `favicon`
- `owner`: who the top-level repositories belong to, and `owner_url`:
  their website, linked from the footer ("served by you")
- `[clone] http`: the base URL shown in clone commands
- `[activity]`: an **activity** tab with a year of commits as a
  GitHub-style graph (off unless `enabled = true`; mirrors never count)
- `[signatures]`: SSH keys whose signed commits and tags get a
  **verified** badge (off unless a key is listed)
- `[theme]`: a colour theme for light and dark mode (GitHub, Ayu,
  Night Owl / Light Owl, Catppuccin, or your own under `[themes]`), and
  Google Fonts for text and code (downloaded and served by canopy, so
  visitors never contact Google)
- `[repos."name"]`: a repository's `description`, `homepage`, or
  `hidden = true` (or, without touching the config, `ssh git@HOST private
  NAME on`)

`./canopy -check` validates the file and prints the resulting settings.
Unknown keys are errors, so typos don't go unnoticed.
[config/README.md](config/README.md) describes every setting.

Repositories are found under `root`, up to `depth` folders deep: bare
repositories (`foo.git`) and work trees (`foo/.git`). The folder path is the
URL, so `/srv/git/tools/foo.git` is served at `/tools/foo` and listed under
**tools** on the index. A repository's `description` file is its
description. Dot-named folders are only served when they are bare
repositories (`.dotfiles.git`), so a version-controlled `~/.ssh` never is.

## URLs

Modelled on sourcehut. The `item` separator lets branch names contain
slashes.

    /activity?year={year}&page={n}       commit graph and activity, last year by default ([activity] in the config)
    /about                               the about page (about in the config)
    /{repo}                              summary: recent commits, license, refs, clone URL, README
    /{repo}/tree/{ref}/item/{path}       folder listing or highlighted file
    /{repo}/blob/{ref}/item/{path}       raw file
    /{repo}/blame/{ref}/item/{path}      blame
    /{repo}/log/{ref}/item/{path}        history, paginated
    /{repo}/commit/{hash}                commit with diff (.patch for a mailbox patch)
    /{repo}/refs                         branches and tags (tags paginated)
    /{repo}/tag/{name}                   a tag: its message (release notes), signature, commit and tarball
    /{repo}/log.atom                     Atom feed of the newest commits
    /{repo}/tags.atom                    Atom feed of the newest tags
    /{repo}/archive/{ref}.tar.gz         tarball

`git clone https://host/{repo}` uses git's smart HTTP protocol, fetch only.

## Source layout

    main.go              flags, -check, HTTP server
    internal/config      TOML loading (unknown keys are errors)
    internal/fonts       downloads [theme] fonts from Google Fonts and caches them
    internal/theme       built-in colour themes and highlighting styles, [themes] checks
    internal/git         runs the git binary: log, tree, blob, diff, blame, refs, archive
    internal/render      syntax highlighting, markdown rendering and sanitizing
    internal/sigcheck    checks SSH signatures on commits and tags, for [signatures]
    internal/server      routing, handlers, templates and CSS (embedded)
    config/              your settings, about page and icon
    demo/                sample repositories and their config
    install.sh           installer and remote deploy (also writes canopy-update)
    update.sh            quick redeploy of canopy and config/ only (or CANOPY_CONFIG_DIR)

Development: `go test ./...`, `go vet ./...`, `gofmt -l .` and
`shellcheck install.sh update.sh` must all be clean. See
[development.md](development.md) for running and testing it locally.

## License

Copyright (C) 2026 The spinney authors

canopy is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free
Software Foundation, either version 3 of the License, or (at your option)
any later version. See [LICENSE](LICENSE).
