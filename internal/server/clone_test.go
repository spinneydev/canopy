package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloneServesOnlyBranchesAndTags: a clone over HTTP gets the branches
// and tags and what they lead to, as the pages show, and nothing else: not a
// commit force-pushed over, though git's protocol v2 would hand out any
// object asked for by its hash, nor one only a ref outside branches and tags
// holds. That holds whichever protocol the client asks for, under any name
// of the header that asks, and whatever the repository's own config allows.
func TestCloneServesOnlyBranchesAndTags(t *testing.T) {
	s, run, work := projServer(t)
	head := func() string {
		t.Helper()
		out, err := exec.Command("git", "-C", work, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("secret\n"), 0o644)
	run("commit", "-qam", "secret")
	gone := head()
	run("reset", "-q", "--hard", "HEAD~1")
	run("commit", "-q", "--allow-empty", "-m", "pull request")
	pull := head()
	run("update-ref", "refs/pull/1/head", pull)
	run("reset", "-q", "--hard", "HEAD~1")
	run("config", "uploadpack.allowAnySHA1InWant", "true")

	for _, header := range []string{"Git-Protocol", "Git_Protocol"} {
		h := http.Handler(s)
		if header != "Git-Protocol" {
			// A client sending the header under another name.
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if v := r.Header.Values("Git-Protocol"); v != nil {
					r.Header.Del("Git-Protocol")
					r.Header[header] = v
				}
				s.ServeHTTP(w, r)
			})
		}
		srv := httptest.NewServer(h)
		url := srv.URL + "/proj"
		for _, version := range []string{"2", "0"} {
			clone := t.TempDir()
			client := func(args ...string) (string, error) {
				cmd := exec.Command("git", append([]string{"-c", "protocol.version=" + version}, args...)...)
				cmd.Dir = clone
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
				out, err := cmd.CombinedOutput()
				return string(out), err
			}
			what := header + ", protocol v" + version
			if out, err := client("init", "-q", "--bare"); err != nil {
				t.Fatalf("%s: git init: %v\n%s", what, err, out)
			}
			if out, err := client("fetch", "-q", url, "main"); err != nil {
				t.Errorf("%s: fetching main: %v\n%s", what, err, out)
			}
			for name, hash := range map[string]string{"the commit pushed over": gone, "refs/pull/1/head's commit": pull} {
				if _, err := client("fetch", "-q", url, hash); err == nil {
					t.Errorf("%s: fetched %s by its hash", what, name)
				}
			}
			out, err := client("ls-remote", url)
			if err != nil || !strings.Contains(out, "refs/heads/main") || !strings.Contains(out, "refs/tags/v1") || strings.Contains(out, "refs/pull") {
				t.Errorf("%s: ls-remote: %v\n%swant main and v1, and no refs/pull", what, err, out)
			}
		}
		srv.Close()
	}
}
