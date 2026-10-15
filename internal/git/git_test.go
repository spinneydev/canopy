package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir with a fixed identity, failing the test on error.
func gitIn(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestRepoConfigCannotRunCommands sets up a repository whose own config
// names programs in every place git might run them during the operations
// canopy performs, and checks that none of them runs.
func TestRepoConfigCannotRunCommands(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	evil := filepath.Join(dir, "evil.sh")
	os.WriteFile(evil, []byte("#!/bin/sh\necho \"$0 $*\" >> "+marker+"\ncat\n"), 0o755)

	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	gitIn(t, work, "", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "a.c"), []byte("int x;\n"), 0o644)
	os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("* diff=evil filter=evil export-subst\n"), 0o644)
	os.WriteFile(filepath.Join(work, "VERSION"), []byte("$Format:%G?$\n"), 0o644)
	gitIn(t, work, "", "add", "-A")
	gitIn(t, work, "", "commit", "-qm", "one")

	// A commit with a (fake) signature, so signature checks have work to do.
	tree := gitIn(t, work, "", "rev-parse", "HEAD^{tree}")
	parent := gitIn(t, work, "", "rev-parse", "HEAD")
	signed := gitIn(t, work, fmt.Sprintf("tree %s\nparent %s\nauthor t <t@t> 1700000000 +0000\n"+
		"committer t <t@t> 1700000000 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n x\n"+
		" -----END PGP SIGNATURE-----\n\nsigned\n", tree, parent), "hash-object", "-t", "commit", "-w", "--stdin")
	gitIn(t, work, "", "update-ref", "refs/heads/main", signed)

	bare := filepath.Join(dir, "repo.git")
	gitIn(t, dir, "", "clone", "-q", "--bare", work, bare)
	for key, val := range map[string]string{
		"log.showSignature":    "true",
		"gpg.program":          evil,
		"gpg.ssh.program":      evil,
		"tar.tar.gz.command":   evil,
		"filter.evil.smudge":   evil,
		"filter.evil.process":  evil,
		"filter.evil.required": "true",
		"diff.evil.textconv":   evil,
		"diff.external":        evil,
		"core.fsmonitor":       evil,
	} {
		gitIn(t, bare, "", "config", key, val)
	}

	ctx := context.Background()
	r := &Repo{Dir: bare}
	hash, err := r.ResolveCommit(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Commit(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Log(ctx, hash, "", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Log(ctx, hash, "a.c", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Diff(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Blame(ctx, hash, "a.c"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tree(ctx, hash, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Blob(ctx, hash, "a.c"); err != nil {
		t.Fatal(err)
	}
	if got := r.LastCommits(ctx, hash, "", []string{"a.c"}, 10); got["a.c"] == nil {
		t.Fatal("LastCommits found no commit for a.c")
	}
	if err := r.WritePatch(ctx, io.Discard, hash); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"tar.gz", "zip"} {
		if err := r.WriteArchive(ctx, io.Discard, hash, "repo", format); err != nil {
			t.Fatal(err)
		}
	}
	first := c.FirstParent()
	if err := r.WriteDiff(ctx, io.Discard, first, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DiffBetween(ctx, "", hash); err != nil {
		t.Fatal(err)
	}
	if err := r.WritePatches(ctx, io.Discard, first, hash); err != nil {
		t.Fatal(err)
	}
	if base, err := r.MergeBase(ctx, first, hash); err != nil || base != first {
		t.Fatalf("MergeBase = %q, %v; want %s", base, err, first)
	}
	if d, err := r.Diverged(ctx, first, hash); err != nil || d != (Divergence{Ahead: 1}) {
		t.Fatalf("Diverged = %+v, %v; want 1 ahead", d, err)
	}
	if d, err := r.BranchesDiverged(ctx, first); err != nil || d["main"] == nil || *d["main"] != (Divergence{Ahead: 1}) {
		t.Fatalf("BranchesDiverged = %v, %v; want main 1 ahead", d, err)
	}
	if _, err := r.Refs(ctx, "refs/heads"); err != nil {
		t.Fatal(err)
	}
	if objs, err := r.Objects(ctx, []string{hash, "HEAD", "main:a.c"}); err != nil || len(objs) != 1 {
		t.Fatalf("Objects = %d objects, %v; want only the commit", len(objs), err)
	}

	if out, err := os.ReadFile(marker); err == nil {
		t.Fatalf("repository config ran a command:\n%s", out)
	}
}

func TestLastCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work := t.TempDir()
	gitIn(t, work, "", "init", "-q", "-b", "main")
	commit := func(msg string, files ...string) {
		for _, f := range files {
			p := filepath.Join(work, f)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(msg+"\n"), 0o644)
		}
		gitIn(t, work, "", "add", "-A")
		gitIn(t, work, "", "commit", "-qm", msg)
	}
	commit("one", "README", "src/a.c", "src/b.c", "src/[x]/c.c")
	commit("two", "src/a.c")
	commit("three", "src/[x]/c.c")
	commit("four", "README")

	ctx := context.Background()
	r := &Repo{Dir: filepath.Join(work, ".git")}
	hash, err := r.ResolveCommit(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	check := func(dir string, want map[string]string) {
		t.Helper()
		var names []string
		for n := range want {
			names = append(names, n)
		}
		got := r.LastCommits(ctx, hash, dir, names, 100)
		for n, msg := range want {
			if c := got[n]; c == nil || c.Message != msg {
				t.Errorf("LastCommits(%q)[%q] = %v, want %q", dir, n, c, msg)
			}
		}
	}
	check("", map[string]string{"README": "four", "src": "three"})
	check("src", map[string]string{"a.c": "two", "b.c": "one", "[x]": "three"})
	check("src/[x]", map[string]string{"c.c": "three"})

	// Walking fewer commits leaves older entries out.
	if got := r.LastCommits(ctx, hash, "src", []string{"a.c", "b.c"}, 2); got["b.c"] != nil || got["a.c"] == nil {
		t.Errorf("LastCommits with limit 2 = %v, want only a.c", got)
	}
}

func TestResolveCommitRefusesSpecialRevisions(t *testing.T) {
	r := &Repo{Dir: t.TempDir()}
	for _, rev := range []string{"", "--help", "-n", ":/fix", "main@{1}", "HEAD@{yesterday}", "main^{/fix}", "HEAD~2^{/x}", "a\nb"} {
		if _, err := r.ResolveCommit(context.Background(), rev); err != ErrNotFound {
			t.Errorf("ResolveCommit(%q) = %v, want ErrNotFound", rev, err)
		}
	}
}

func TestTag(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work := t.TempDir()
	gitIn(t, work, "", "init", "-q", "-b", "main")
	gitIn(t, work, "", "commit", "-q", "--allow-empty", "-m", "one")
	gitIn(t, work, "", "tag", "-a", "-m", "release one", "v1")
	gitIn(t, work, "", "tag", "v1.1")
	gitIn(t, work, "", "tag", "rel/a")

	ctx := context.Background()
	r := &Repo{Dir: filepath.Join(work, ".git")}
	tag, err := r.Tag(ctx, "v1")
	if err != nil || !tag.Annotated || tag.Tagger != "t" || tag.Message != "release one" {
		t.Errorf("Tag(v1) = %+v, %v", tag, err)
	}
	if tag, err := r.Tag(ctx, "rel/a"); err != nil || tag.Annotated || tag.Name != "rel/a" {
		t.Errorf("Tag(rel/a) = %+v, %v", tag, err)
	}
	// A pattern or a folder of tags names no tag.
	for _, name := range []string{"", "v*", "rel", "v", "nope"} {
		if _, err := r.Tag(ctx, name); err != ErrNotFound {
			t.Errorf("Tag(%q) = %v, want ErrNotFound", name, err)
		}
	}
}

func TestState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	work := t.TempDir()
	gitIn(t, work, "", "init", "-q", "-b", "main")
	gitIn(t, work, "", "commit", "-q", "--allow-empty", "-m", "one")

	ctx := context.Background()
	r := &Repo{Dir: filepath.Join(work, ".git")}
	before, newest, err := r.State(ctx)
	if err != nil || newest.IsZero() {
		t.Fatalf("State = %q, %v, %v", before, newest, err)
	}
	gitIn(t, work, "", "tag", "v2")
	if after, _, _ := r.State(ctx); after == before {
		t.Error("State unchanged by a new tag")
	}
	before, _, _ = r.State(ctx)
	gitIn(t, work, "", "branch", "other")
	gitIn(t, work, "", "symbolic-ref", "HEAD", "refs/heads/other")
	if after, _, _ := r.State(ctx); after == before {
		t.Error("State unchanged by a new branch and HEAD")
	}
}
