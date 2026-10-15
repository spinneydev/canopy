package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	s, run, work := projServer(t)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("two\n"), 0o644)
	run("commit", "-qam", "two")
	run("tag", "-a", "-m", "second", "v2")
	run("checkout", "-qb", "side", "v1")
	os.WriteFile(filepath.Join(work, "b.txt"), []byte("side\n"), 0o644)
	run("add", "b.txt")
	run("commit", "-qm", "side")
	run("checkout", "-q", "main")

	for _, c := range []struct{ url, want string }{
		{"/proj/compare/v1...v2", "is 1 commit ahead of"},
		{"/proj/compare/v1...v2", "&#43;two"},
		{"/proj/compare/main...side", "is 1 commit ahead of <span class=\"mono\">main</span>, and 1 commit behind"},
		{"/proj/compare/v2...v1", "has no commits that"},
		{"/proj/compare", `name="from" value="v2"`},
		{"/proj/compare/v1...v2.diff", "-one\n+two"},
		{"/proj/compare/v1...v2.patch", "Subject: [PATCH] two"},
		{"/proj/refs", "1 ahead, 1 behind main"},
		{"/proj/refs", `href="/proj/compare/main...side" rel="nofollow">compare</a>`},
		{"/proj/refs", `href="/proj/compare/v1...v2" rel="nofollow" title="since v1">changes</a>`},
		{"/proj/tag/v2", `since v1</a>`},
	} {
		code, body := get(s, c.url)
		if code != 200 || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d, lacks %q", c.url, code, c.want)
		}
	}
	for _, u := range []string{"/proj/compare/v1..v2", "/proj/compare/v1...nothing"} {
		if code, _ := get(s, u); code != 404 {
			t.Errorf("%s: %d, want 404", u, code)
		}
	}
	if code, _ := get(s, "/proj/compare?from=v1&to=v2"); code != 302 {
		t.Errorf("compare form: %d, want a redirect", code)
	}
}
