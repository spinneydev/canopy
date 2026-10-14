# Customising the look

canopy's look can change in four ways, all in `config/canopy.toml`:

| What | Setting | Section |
|---|---|---|
| Colour theme, light and dark | `[theme] light`, `dark` | [1](#1-pick-a-theme) |
| Your own colour theme | `[themes."name"]` | [2](#2-make-your-own-theme) |
| Fonts for text and code | `[theme] sans`, `mono` | [3](#3-fonts) |
| Site name, tagline, icon, about page | `title`, `header`, `description`, `favicon`, `about` | [config/README.md](config/README.md#settings) |

Nothing else can change: there is no custom CSS file. Every value is
checked to be a theme name, a colour or a font name, so the config can't
add styles, scripts or requests to other sites (see
[security.md](security.md)). The settings are read when canopy starts:
after editing, run `./update.sh` on a server, or restart `go run .`
locally.

## 1. Pick a theme

The site follows each visitor's system setting, so it has two themes: one
for light mode and one for dark mode.

```toml
[theme]
light = "github-light"
dark = "github-dark"
```

| light | dark | Feel |
|---|---|---|
| `default` | `default` | canopy's own: flat, grey header, blue links |
| `github-light` | `github-dark` | GitHub's interface |
| `ayu-light` | `ayu-mirage` | soft, warm orange accents (Ayu Light is low-contrast by design) |
| `light-owl` | `night-owl` | Sarah Drasner's Night Owl: deep blue, purple and teal |
| `catppuccin-latte` | `catppuccin-frappe` | Catppuccin's pastels |

Each comes with matching syntax highlighting and an activity graph in its
accent colour. Leave a key out for `default`.

The pairs above are suggestions; any theme can go in either slot. To keep
the site dark for everyone, whatever their setting:

```toml
[theme]
light = "night-owl"
dark = "night-owl"
```

## 2. Make your own theme

Add a `[themes."name"]` section and pick it by name, like a built-in one.
The easiest way is to start from a built-in theme (`base`) and change a
few colours:

```toml
[theme]
dark = "dracula"

[themes.dracula]
base = "night-owl"      # the colours you don't set come from here
highlight = "dracula"   # syntax highlighting; else the base's
bg = "#282a36"
fg = "#f8f8f2"
header-bg = "#21222c"
event-bg = "#2f3241"
code-bg = "#21222c"
border = "#44475a"
link = "#ff79c6"
link-hover = "#ffb2e0"
act-4 = "#ff79c6"
```

(`demo/canopy.toml` has this one as `demo-dracula`.)

Without `base`, the theme starts from `default`: any colour you don't set
stays canopy's own for the mode the theme is used in. That suits small
touches, like a different link colour:

```toml
[theme]
light = "brand"

[themes.brand]
link = "#b5410b"
link-hover = "#8a3108"
```

**Names** are lower case letters, digits and `-` (`my-dark`), and can't be
a built-in name. Define as many as you like; only the two named in
`[theme]` are used.

**Colours** are `#rgb`, `#rrggbb`, `#rrggbbaa`, `rgb()`, `hsl()`,
`oklch()` and the other colour functions, or a CSS colour name (`white`).
`var()`, `url()` and anything else is refused.

**Highlighting** (`highlight`) is any [chroma
style](https://xyproto.github.io/splash/docs/): `dracula`, `monokai`,
`nord`, `onedark`, `gruvbox`, `rose-pine`, `tokyonight-night`,
`solarized-light`, `catppuccin-mocha` and many more, or a built-in theme's
own (`ayu-mirage`, `night-owl`, …). A wrong name lists them all.

### The colours

Each name is a colour of the interface; a theme sets any of them.

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
| `target` | the highlighted line from a `#L12` link |
| `add`, `add-bg`, `del`, `del-bg` | added and removed lines in diffs |
| `hunk` | `@@` lines in diffs, renames |
| `branch-bg`, `tag-bg` | branch and tag labels |
| `act-0` … `act-4` | the activity graph, from no commits to the busiest day |

Tips:

- Keep `fg` and `muted` readable on `bg`, `event-bg` and `code-bg` alike:
  text sits on all three.
- `add-bg` and `del-bg` sit behind code in diffs; keep them close to
  `code-bg` so highlighted code stays readable on them.
- `act-0` is a day without commits; make it a little different from `bg`
  so the graph's grid shows. `act-1` to `act-4` should get steadily
  stronger.
- Pick a `highlight` style made for the same background (dark or light),
  or code becomes hard to read.

## 3. Fonts

Any family from [Google Fonts](https://fonts.google.com), written as
there:

```toml
[theme]
sans = "Inter"            # text
mono = "JetBrains Mono"   # code, hashes, file lists
```

Visitors never contact Google. When canopy starts it downloads the
family itself (regular to bold, upright and italic, every script) and
serves it from the site. Until that has finished, and if it fails, pages
show the system fonts. On a server the files are kept in
`/var/cache/canopy/fonts`, so later starts don't download them again;
delete that folder to fetch a family afresh. A server installed before
fonts existed gets that folder from one `./install.sh … --yes` run
([deploy.md](deploy.md)); until then the fonts are downloaded on every
restart.

Monospace fonts that read well in code: `JetBrains Mono`, `Fira Code`,
`IBM Plex Mono`, `Source Code Pro`, `Roboto Mono`, `Ubuntu Mono`. For text:
`Inter`, `IBM Plex Sans`, `Source Sans 3`, `Noto Sans`.

If a font doesn't show, look at the log (`journalctl -u canopy` on the
server):

| Log line | Meaning |
|---|---|
| `font "Inter": downloaded 14 files` | Done; later starts say nothing. |
| `not on Google Fonts` | The name is misspelt; copy it from fonts.google.com. |
| a network error | The server couldn't reach Google; canopy retries by itself. |

## 4. Try it before deploying

The demo shows every page with sample repositories, and `demo/canopy.toml`
already has a `[theme]` to edit:

```sh
go run ./demo                        # sample repositories, once
go run . -config demo/canopy.toml   # http://127.0.0.1:8080
```

Restart it (Ctrl+C, run again) after each change. Switch your system or
the browser's developer tools between light and dark to see both themes.
Pages worth checking: a source file
(`/tinyhttpd/tree/main/item/src/request.c`), a commit with a diff, and the
**activity** tab. `go run . -config demo/canopy.toml -check` validates the
config and prints the themes in use without starting anything.

Then copy the `[theme]` and `[themes]` sections to `config/canopy.toml`
and deploy with `./update.sh`.

## Adding a built-in theme

For changes to canopy itself rather than one site's config
([development.md](development.md) has the rest):

1. Add it to `internal/theme/builtin.go` with every colour set; a test
   fails if one is missing or isn't a colour.
2. Name a chroma style as its `Highlight`. If chroma has none that fits,
   write one in `internal/theme/styles/` (see `night-owl.xml`); it is
   registered at start and can be used by `[themes]` too.
3. Add it to the tables here, in config/README.md and in the comment in
   config/canopy.toml.
