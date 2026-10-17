package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "canopy.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFilesResolveNextToTheConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "about.md"), []byte("# hi"), 0o644)
	os.WriteFile(filepath.Join(dir, "icon.svg"), []byte("<svg/>"), 0o644)
	cfg, err := Load(writeConfig(t, dir, "about = \"about.md\"\nfavicon = \"icon.svg\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.About != filepath.Join(dir, "about.md") || cfg.Favicon != filepath.Join(dir, "icon.svg") {
		t.Fatalf("not resolved against the config's folder: %q %q", cfg.About, cfg.Favicon)
	}
}

func TestFileSettingsAreChecked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "page.html"), []byte("<script>"), 0o644)
	os.WriteFile(filepath.Join(dir, "big.png"), make([]byte, MaxFileSize+1), 0o644)
	for body, want := range map[string]string{
		`favicon = "page.html"`: "must end in one of",
		`about = "page.html"`:   "must end in one of",
		`about = "missing.md"`:  "no such file",
		`favicon = "big.png"`:   "at most 1 MiB",
		`titel = "typo"`:        "unknown keys",
	} {
		_, err := Load(writeConfig(t, dir, body+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", body, err, want)
		}
	}
}

func TestActivityIsChecked(t *testing.T) {
	dir := t.TempDir()
	ok := "[activity]\nenabled = true\nemails = [\"you@example.com\"]\n"
	if _, err := Load(writeConfig(t, dir, ok)); err != nil {
		t.Fatalf("%s: %v", ok, err)
	}
	for body, want := range map[string]string{
		"enabled = true\nemails = [\"you\"]":             "isn't an email address",
		"enabled = true\nemails = [\"a b@example.com\"]": "isn't an email address",
		"emails = [\"you@example.com\"]":                 "enabled isn't",
		"enable = true":                                  "unknown keys",
	} {
		_, err := Load(writeConfig(t, dir, "[activity]\n"+body+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", body, err, want)
		}
	}
}

func TestOwnerURLIsChecked(t *testing.T) {
	dir := t.TempDir()
	ok := "owner = \"you\"\nowner_url = \"https://example.com\"\n"
	if _, err := Load(writeConfig(t, dir, ok)); err != nil {
		t.Fatalf("%s: %v", ok, err)
	}
	for body, want := range map[string]string{
		"owner = \"you\"\nowner_url = \"example.com\"":         "must start with",
		"owner = \"you\"\nowner_url = \"javascript:alert(1)\"": "must start with",
		"owner_url = \"https://example.com\"":                  "set owner too",
	} {
		_, err := Load(writeConfig(t, dir, body+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", body, err, want)
		}
	}
}

func TestThemeIsChecked(t *testing.T) {
	dir := t.TempDir()
	ok := "[theme]\nsans = \"Inter\"\nmono = \"JetBrains Mono\"\nlight = \"ayu-light\"\ndark = \"my-dark\"\n" +
		"[themes.my-dark]\nbase = \"night-owl\"\nlink = \"rgb(121 176 242)\"\nact-4 = \"oklch(70% 0.2 145 / 0.9)\"\n"
	if _, err := Load(writeConfig(t, dir, ok)); err != nil {
		t.Fatalf("%s: %v", ok, err)
	}
	for body, want := range map[string]string{
		"[theme]\nsans = \"Inter;x\"":           "isn't a Google Fonts family",
		"[theme]\nmono = \"Mono&family=Other\"": "isn't a Google Fonts family",
		"[theme]\nfont = \"Inter\"":             "unknown keys",
		"[theme]\ndark = \"dracula\"":           "theme: dark: no theme \"dracula\"",
		"[themes.mine]\nlinks = \"#fff\"":       "themes: mine: unknown colour",
		"[themes.mine]\nbg = \"url(x)\"":        "isn't a colour",
		"[themes.github-dark]\nbg = \"#000\"":   "is a built-in theme",
	} {
		_, err := Load(writeConfig(t, dir, body+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", body, err, want)
		}
	}
}

func TestSignaturesAreChecked(t *testing.T) {
	dir := t.TempDir()
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE"
	ok := "[signatures.keys]\nalice = [\"" + key + " alice@laptop\"]\n"
	if _, err := Load(writeConfig(t, dir, ok)); err != nil {
		t.Fatalf("%s: %v", ok, err)
	}
	if _, err := Load(writeConfig(t, dir, "[signatures.github]\nAlice = \"alice-gh\"\n")); err != nil {
		t.Fatalf("github: %v", err)
	}
	twice := "[signatures.keys]\nalice = [\"" + key + "\"]\nbob = [\"" + key + "\"]"
	for body, want := range map[string]string{
		"[signatures.keys]\nalice = [\"ssh-ed25519 AAAA\"]": "isn't an SSH public key",
		twice:                                      "is listed under",
		"[signatures]\nkey = []":                   "unknown keys",
		"[signatures.github]\nalice = \"../x\"":    "isn't a GitHub user name",
		"[signatures.github]\nalice = \"-alice\"":  "isn't a GitHub user name",
		"[signatures.github]\nalice = [\"alice\"]": "",
	} {
		_, err := Load(writeConfig(t, dir, body+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", body, err, want)
		}
	}
}

func TestMirrorsAreChecked(t *testing.T) {
	dir := t.TempDir()
	ok := `"torvalds/linux" = "https://git.kernel.org/pub/scm/linux/kernel/git/torvalds/linux.git"`
	if _, err := Load(writeConfig(t, dir, "[mirrors]\n"+ok+"\n")); err != nil {
		t.Fatalf("%s: %v", ok, err)
	}
	for entry, want := range map[string]string{
		`"../escape" = "https://x/y.git"`:    "isn't a repository name",
		`"a/./b" = "https://x/y.git"`:        "isn't a repository name",
		`"-x" = "https://x/y.git"`:           "isn't a repository name",
		`"a/b.git" = "https://x/y.git"`:      "isn't a repository name",
		`"a/b/c/d" = "https://x/y.git"`:      "more than depth",
		`"a/b" = "ext::sh -c touch% /tmp/x"`: "must start with",
		`"a/b" = "file:///etc"`:              "must start with",
		`"a/b" = "https://me:tok@x/y.git"`:   "holds a user name or password",
		`"a/b" = "https://tok@x/y.git"`:      "holds a user name or password",
		`"a/b" = "git://me@x/y.git"`:         "holds a user name or password",
		`"a/b" = "https://x/%zz@y.git"`:      "isn't a URL",
	} {
		_, err := Load(writeConfig(t, dir, "[mirrors]\n"+entry+"\n"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want an error containing %q", entry, err, want)
		}
	}
}
