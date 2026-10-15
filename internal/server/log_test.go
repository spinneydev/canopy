package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// mergedHistory gives proj a branch side, merged into main, whose commits
// interleave with main's past the first page of the log.
func mergedHistory(t *testing.T, run func(args ...string), work string) {
	t.Helper()
	commit := func(name string, n int) {
		os.WriteFile(filepath.Join(work, name), []byte(strconv.Itoa(n)+"\n"), 0o644)
		run("add", name)
		run("commit", "-qm", name+" "+strconv.Itoa(n), "--date", strconv.Itoa(1700000000+n*60)+" +0000")
	}
	run("checkout", "-qb", "side")
	run("checkout", "-q", "main")
	for i := 1; i <= 30; i++ {
		if i%2 == 0 {
			run("checkout", "-q", "side")
			commit("b.txt", i)
		} else {
			run("checkout", "-q", "main")
			commit("a.txt", i)
		}
	}
	run("checkout", "-q", "main")
	run("merge", "-q", "--no-ff", "-m", "merge side", "side")
}

var commitLink = regexp.MustCompile(`class="hash" href="/proj/commit/([0-9a-f]+)"`)

// Following "next" shows every commit once, side's included, and draws the
// graph on every page.
func TestLogPagesShowEveryCommit(t *testing.T) {
	s, run, work := projServer(t)
	t.Setenv("GIT_COMMITTER_DATE", "1700000000 +0000")
	mergedHistory(t, run, work)

	seen := map[string]bool{}
	next := regexp.MustCompile(`href="(/proj/log/main\?[^"]+)">next`)
	for u, pages := "/proj/log", 0; u != ""; pages++ {
		if pages > 5 {
			t.Fatal("too many pages")
		}
		code, body := get(s, strings.ReplaceAll(u, "&amp;", "&"))
		if code != 200 {
			t.Fatalf("%s: %d", u, code)
		}
		if !strings.Contains(body, `class="lane node`) || !strings.Contains(body, `<svg class="bend"`) {
			t.Errorf("%s: no graph", u)
		}
		for _, m := range commitLink.FindAllStringSubmatch(body, -1) {
			if seen[m[1]] {
				t.Errorf("%s: %s again", u, m[1])
			}
			seen[m[1]] = true
		}
		u = ""
		if m := next.FindStringSubmatch(body); m != nil {
			u = m[1]
		}
	}
	// one, then 30, then the merge
	if len(seen) != 32 {
		t.Errorf("pages showed %d commits, want 32", len(seen))
	}
}
