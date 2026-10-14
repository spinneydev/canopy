package server

import (
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spinney.dev/canopy/internal/config"
)

// indexServer serves bare repositories with these names; private ones get
// canopy.private set to the given value, as the SSH "private" command does,
// or another key when the value is "key=value".
func indexServer(t *testing.T, owner string, repos []string, private map[string]string) *Server {
	t.Helper()
	root := t.TempDir()
	for _, name := range repos {
		dir := filepath.Join(root, filepath.FromSlash(name)+".git")
		if out, err := exec.Command("git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
		if v, ok := private[name]; ok {
			key, value, found := strings.Cut(v, "=")
			if !found {
				key, value = "canopy.private", v
			}
			if out, err := exec.Command("git", "config", "--file", filepath.Join(dir, "config"), key, value).CombinedOutput(); err != nil {
				t.Fatalf("%s: %v: %s", name, err, out)
			}
		}
	}
	cfg := config.Default()
	cfg.Root = root
	cfg.Owner = owner
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func get(s *Server, url string) (int, string) {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
	return w.Code, w.Body.String()
}

func TestPrivateReposAreLeftOut(t *testing.T) {
	s := indexServer(t, "", []string{"open", "secret", "odd", "shown"},
		map[string]string{"secret": "true", "odd": "maybe", "shown": "false"})
	_, body := get(s, "/")
	for _, name := range []string{"open", "shown"} {
		if !strings.Contains(body, `href="/`+name+`"`) {
			t.Errorf("index lacks %s", name)
		}
	}
	// A value git can't read as a boolean counts as private.
	for _, name := range []string{"secret", "odd"} {
		if strings.Contains(body, `href="/`+name+`"`) {
			t.Errorf("index lists private %s", name)
		}
		for _, url := range []string{"/" + name, "/" + name + "/info/refs?service=git-upload-pack", "/" + name + "/log.atom"} {
			if code, _ := get(s, url); code != 404 {
				t.Errorf("%s: %d, want 404", url, code)
			}
		}
	}
}

// The site owner's folder comes right after the top-level repositories,
// whatever its name; the other folders follow alphabetically, ignoring case.
func TestIndexPutsTheOwnersFolderFirst(t *testing.T) {
	s := indexServer(t, "alice", []string{"top", "zed/x", "Alpha/y", "alice/z", "beta/w"}, nil)
	_, body := get(s, "/?sort=name")
	last := -1
	for _, name := range []string{"/top", "/alice/z", "/Alpha/y", "/beta/w", "/zed/x"} {
		i := strings.Index(body, `href="`+name+`"`)
		if i < 0 {
			t.Fatalf("index lacks %s", name)
		}
		if i < last {
			t.Errorf("%s is listed too early", name)
		}
		last = i
	}
}
