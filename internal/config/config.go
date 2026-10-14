// Package config loads canopy's TOML configuration file.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"

	"spinney.dev/canopy/internal/sigcheck"
	"spinney.dev/canopy/internal/theme"
)

type Config struct {
	Listen      string `toml:"listen"`
	Root        string `toml:"root"`         // directory scanned for repositories
	Title       string `toml:"title"`        // site name in the browser tab and footer
	Header      string `toml:"header"`       // site name in the top bar; Title if empty
	Description string `toml:"description"`  // tagline on the index page
	Depth       int    `toml:"depth"`        // how deep under Root to look for repositories
	Git         string `toml:"git"`          // path to the git binary
	Favicon     string `toml:"favicon"`      // .svg, .png or .ico file; a built-in icon if empty
	About       string `toml:"about"`        // markdown file shown as the site's about page
	Owner       string `toml:"owner"`        // owner of top-level repositories on the index
	OwnerURL    string `toml:"owner_url"`    // owner's website; the footer then credits Owner with a link to it
	TreeCommits bool   `toml:"tree_commits"` // each tree entry's last commit on tree pages

	Clone      Clone                        `toml:"clone"`
	Activity   Activity                     `toml:"activity"`
	Signatures Signatures                   `toml:"signatures"`
	Theme      Theme                        `toml:"theme"`
	Themes     map[string]map[string]string `toml:"themes"` // name → colours, base and highlight
	Repos      map[string]RepoConfig        `toml:"repos"`
	Mirrors    map[string]string            `toml:"mirrors"` // repository name → upstream URL to follow
}

type Clone struct {
	HTTP string `toml:"http"` // base URL for clones; derived from the request if empty
}

// Activity is the "activity" tab: a year of commits as a graph, and the
// newest commits and tags. Mirrors are never counted.
type Activity struct {
	Enabled bool     `toml:"enabled"`
	Emails  []string `toml:"emails"` // authors whose commits count; everyone's if empty
}

// Signatures turns on signature badges on commits and tags. A good SSH
// signature by one of these keys shows as verified, with the key's name.
type Signatures struct {
	Keys map[string][]string `toml:"keys"` // name → SSH public keys
}

// Theme picks the site's colours and fonts, nothing else. Colour themes are
// built in (internal/theme) or written under [themes]; fonts are Google
// Fonts families, which canopy downloads and serves itself.
type Theme struct {
	Light string `toml:"light"` // theme for visitors in light mode; "default" if empty
	Dark  string `toml:"dark"`  // the same for dark mode
	Sans  string `toml:"sans"`  // Google Fonts family for text
	Mono  string `toml:"mono"`  // Google Fonts family for code
}

// fontFamily matches a Google Fonts family name, like "JetBrains Mono".
var fontFamily = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ]{0,63}$`)

func checkTheme(t Theme, themes map[string]map[string]string) error {
	for key, family := range map[string]string{"sans": t.Sans, "mono": t.Mono} {
		if family != "" && !fontFamily.MatchString(family) {
			return fmt.Errorf("theme: %s: %q isn't a Google Fonts family name like \"JetBrains Mono\"", key, family)
		}
	}
	for name, def := range themes {
		if err := theme.CheckCustom(name, def); err != nil {
			return fmt.Errorf("themes: %w", err)
		}
	}
	for key, name := range map[string]string{"light": t.Light, "dark": t.Dark} {
		if _, err := theme.Resolve(name, themes); err != nil {
			return fmt.Errorf("theme: %s: %w", key, err)
		}
	}
	return nil
}

// RepoConfig overrides settings for one repository, keyed by its URL name.
type RepoConfig struct {
	Description string `toml:"description"` // replaces the repo's description file
	Homepage    string `toml:"homepage"`    // project website, shown on the summary
	Hidden      bool   `toml:"hidden"`      // not listed and not served
	Owner       string `toml:"owner"`       // replaces the owner taken from the folder
}

func Default() *Config {
	return &Config{
		Listen: ":8080",
		Root:   ".",
		Title:  "canopy",
		Depth:  3,
		Git:    "git",
	}
}

// Load reads the file at path over the defaults. A relative root is resolved
// against the file's directory.
func Load(path string) (*Config, error) {
	cfg := Default()
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, err
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys: %s", path, strings.Join(keys, ", "))
	}
	dir := filepath.Dir(path)
	cfg.Root = resolve(dir, cfg.Root)
	cfg.Favicon = resolve(dir, cfg.Favicon)
	cfg.About = resolve(dir, cfg.About)
	if err := checkFile("favicon", cfg.Favicon, ".svg", ".png", ".ico"); err != nil {
		return nil, err
	}
	if err := checkFile("about", cfg.About, ".md", ".markdown"); err != nil {
		return nil, err
	}
	if cfg.OwnerURL != "" {
		if !strings.HasPrefix(cfg.OwnerURL, "https://") && !strings.HasPrefix(cfg.OwnerURL, "http://") {
			return nil, fmt.Errorf("owner_url: %q must start with https:// or http://", cfg.OwnerURL)
		}
		if cfg.Owner == "" {
			return nil, fmt.Errorf("owner_url: set owner too, the name the footer links")
		}
	}
	for name, url := range cfg.Mirrors {
		if err := checkMirror(name, url, cfg.Depth); err != nil {
			return nil, err
		}
	}
	for _, email := range cfg.Activity.Emails {
		if !strings.Contains(email, "@") || strings.ContainsFunc(email, unicode.IsSpace) {
			return nil, fmt.Errorf("activity: %q isn't an email address", email)
		}
	}
	if len(cfg.Activity.Emails) > 0 && !cfg.Activity.Enabled {
		return nil, fmt.Errorf("activity: emails are set but enabled isn't; add enabled = true")
	}
	if err := checkTheme(cfg.Theme, cfg.Themes); err != nil {
		return nil, err
	}
	if _, err := sigcheck.NewKeys(cfg.Signatures.Keys); err != nil {
		return nil, fmt.Errorf("signatures: keys: %w", err)
	}
	return cfg, nil
}

// mirrorName matches one folder or file name of a mirror: no "." or "..",
// and nothing git could read as an option.
var mirrorName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)

// checkMirror validates one [mirrors] entry: a repository name under root
// that the index can reach, and an upstream git can fetch without running
// anything (no ext:: or file transports).
func checkMirror(name, url string, depth int) error {
	segs := strings.Split(name, "/")
	for _, s := range segs {
		if !mirrorName.MatchString(s) || strings.HasSuffix(s, ".git") {
			return fmt.Errorf("mirrors: %q isn't a repository name like \"owner/project\"", name)
		}
	}
	if len(segs) > depth {
		return fmt.Errorf("mirrors: %q is %d folders deep, more than depth = %d", name, len(segs), depth)
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "git://") {
		return fmt.Errorf("mirrors: %s: %q must start with https://, http:// or git://", name, url)
	}
	return nil
}

// MaxFileSize bounds the favicon and about files.
const MaxFileSize = 1 << 20

// checkFile validates an optional file setting: it must exist, be small and
// have one of the given extensions (which also decides how it is served).
func checkFile(key, path string, exts ...string) error {
	if path == "" {
		return nil
	}
	ext := strings.ToLower(filepath.Ext(path))
	if !slices.Contains(exts, ext) {
		return fmt.Errorf("%s: %s must end in one of %s", key, path, strings.Join(exts, " "))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxFileSize {
		return fmt.Errorf("%s: %s must be a regular file of at most 1 MiB", key, path)
	}
	return nil
}

func resolve(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}
