package server

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"spinney.dev/canopy/internal/config"
)

// projServer serves a repository "proj" with one commit of a.txt, tagged
// v1, and returns a function running git in its work tree.
func projServer(t *testing.T) (*Server, func(args ...string), string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "proj")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(work, 0o755)
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	run("add", "a.txt")
	run("commit", "-qm", "one")
	run("tag", "-a", "-m", "first", "v1")

	cfg := config.Default()
	cfg.Root = root
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, run, work
}

func TestPagesAnswerNotModified(t *testing.T) {
	s, run, work := projServer(t)
	request := func(url, etag string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", url, nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		s.ServeHTTP(w, r)
		return w
	}

	urls := []string{"/proj", "/proj/log", "/proj/refs", "/proj/tag/v1", "/proj/tree/main/item/a.txt",
		"/proj/blob/main/item/a.txt", "/proj/commit/v1.patch", "/proj/commit/v1.diff", "/proj/archive/v1.tar.gz", "/proj/archive/v1.zip", "/proj/log.atom"}
	etags := map[string]string{}
	for _, u := range urls {
		w := request(u, "")
		etags[u] = w.Header().Get("ETag")
		if w.Code != 200 || etags[u] == "" {
			t.Fatalf("%s: %d, ETag %q", u, w.Code, etags[u])
		}
		if w := request(u, `"other", `+etags[u]); w.Code != 304 || w.Body.Len() != 0 {
			t.Errorf("%s again: %d, want 304", u, w.Code)
		}
	}

	// A new commit changes the pages and the branch's raw file, but not a
	// tag's patch, diff or archives.
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("two\n"), 0o644)
	run("commit", "-qam", "two")
	for _, u := range urls {
		want := 200
		if u == "/proj/commit/v1.patch" || u == "/proj/commit/v1.diff" || u == "/proj/archive/v1.tar.gz" || u == "/proj/archive/v1.zip" {
			want = 304
		}
		if w := request(u, etags[u]); w.Code != want {
			t.Errorf("%s after a commit: %d, want %d", u, w.Code, want)
		}
	}
}
