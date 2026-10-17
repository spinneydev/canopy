// Package git is a thin wrapper around the git binary. Every function takes
// already-validated inputs; callers should resolve user-supplied revisions
// through ResolveCommit before passing them anywhere else.
package git

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Binary is the git executable used for all commands.
var Binary = "git"

var ErrNotFound = errors.New("not found")

// forcedConfig is passed to every git invocation through the environment,
// which takes precedence over a repository's own config. Several config keys
// name programs for git to run; a repository's config must never be able to
// make the web server execute something, so those are pinned here.
var forcedConfig = [][2]string{
	// Read repositories owned by another user (e.g. a dedicated git
	// account). This trusts their config, hence the pins below.
	{"safe.directory", "*"},
	{"core.quotePath", "false"},
	// Signature checks run gpg.program; log.showSignature turns them on
	// for every log and show.
	{"log.showSignature", "false"},
	{"gpg.program", "false"},
	{"gpg.openpgp.program", "false"},
	{"gpg.x509.program", "false"},
	{"gpg.ssh.program", "false"},
	{"core.fsmonitor", "false"},
	{"core.hooksPath", "/dev/null"},
}

// Env returns the environment every git invocation runs with, with extra
// settings after the forced ones.
func Env(extra ...[2]string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	config := slices.Concat(forcedConfig, extra)
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT="+strconv.Itoa(len(config)))
	for i, kv := range config {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return env
}

type Repo struct {
	Dir string // the git directory (bare repo, or the .git of a work tree)
}

func (r *Repo) cmd(ctx context.Context, args ...string) *exec.Cmd {
	return r.cmdWithConfig(ctx, nil, args...)
}

// cmdWithConfig is cmd with extra "-c key=value" settings.
func (r *Repo) cmdWithConfig(ctx context.Context, config []string, args ...string) *exec.Cmd {
	var full []string
	for _, kv := range config {
		full = append(full, "-c", kv)
	}
	full = append(full, "--git-dir="+r.Dir)
	cmd := exec.CommandContext(ctx, Binary, append(full, args...)...)
	cmd.Env = Env()
	return cmd
}

func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := r.cmd(ctx, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Private reports whether canopy.private is set in the repository's own
// config, which keeps it off the site (the git account's "private" command
// sets it). It fails
// closed: if a key can't be read, or isn't a boolean, the repository counts
// as private.
func (r *Repo) Private(ctx context.Context) bool {
	out, err := r.run(ctx, "config", "--local", "--type=bool", "--get-regexp", `^canopy\.private$`)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false // not set
	}
	if err != nil {
		return true
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if !strings.HasSuffix(line, " false") {
			return true
		}
	}
	return false
}

// IsRepo reports whether dir looks like a git directory.
func IsRepo(dir string) bool {
	for _, name := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(dir + "/" + name); err != nil {
			return false
		}
	}
	return true
}

// ResolveCommit turns a user-supplied revision into a full commit hash.
// Options, reflog lookups (main@{1}) and commit-message searches (:/text,
// main^{/text}) are refused, and so is any commit no branch or tag holds:
// one pushed over or left by a deleted branch stays in the repository until
// git gc removes it, and its hash, or the first few characters of it, would
// otherwise still show it.
func (r *Repo) ResolveCommit(ctx context.Context, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") || strings.HasPrefix(rev, ":") ||
		strings.Contains(rev, "@{") || strings.Contains(rev, "{/") || strings.ContainsAny(rev, "\x00\n") {
		return "", ErrNotFound
	}
	out, err := r.run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", ErrNotFound
	}
	hash := strings.TrimSpace(string(out))
	// Commits the branches and tags don't hold: none for a reachable one.
	// The walk stops where hash joins their history, so a branch tip or a
	// recent commit costs next to nothing.
	out, err = r.run(ctx, "rev-list", "-n", "1", hash, "--not", "--branches", "--tags", "--")
	if err != nil || len(bytes.TrimSpace(out)) > 0 {
		return "", ErrNotFound
	}
	return hash, nil
}

// DefaultBranch returns the branch HEAD points to, even if it has no commits.
func (r *Repo) DefaultBranch(ctx context.Context) string {
	out, err := r.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "HEAD"
	}
	return strings.TrimSpace(string(out))
}

type Signature struct {
	Name  string
	Email string
	When  time.Time
}

type Commit struct {
	Hash      string
	Parents   []string
	Author    Signature
	Committer Signature
	Message   string
}

func (c *Commit) Subject() string {
	s, _, _ := strings.Cut(c.Message, "\n")
	return s
}

const commitFormat = "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%cn%x1f%ce%x1f%ct%x1f%B%x1e"

func parseCommits(out []byte) []*Commit {
	var commits []*Commit
	for _, rec := range bytes.Split(out, []byte{0x1e}) {
		rec = bytes.TrimLeft(rec, "\n")
		if len(rec) == 0 {
			continue
		}
		f := strings.SplitN(string(rec), "\x1f", 9)
		if len(f) != 9 {
			continue
		}
		c := &Commit{
			Hash:      f[0],
			Parents:   strings.Fields(f[1]),
			Author:    Signature{f[2], f[3], unix(f[4])},
			Committer: Signature{f[5], f[6], unix(f[7])},
			Message:   strings.TrimRight(f[8], "\n"),
		}
		commits = append(commits, c)
	}
	return commits
}

func unix(s string) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(n, 0)
}

// Log returns up to n commits reachable from rev, after the first skip of
// them, optionally limited to those touching path. rev is a commit hash, or
// two joined by ".." for the commits the second has that the first lacks.
func (r *Repo) Log(ctx context.Context, rev, path string, skip, n int) ([]*Commit, error) {
	args := []string{"log", commitFormat, "--skip=" + strconv.Itoa(skip), "-n", strconv.Itoa(n), rev, "--"}
	if path != "" {
		// A path, not a pattern: "a*" is that file alone, and ":x" isn't x.
		args = append(args, ":(literal)"+path)
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

// Parents lists the first n commits of hash's log, in the order Log gives
// them, each as its hash followed by its parents'.
func (r *Repo) Parents(ctx context.Context, hash string, n int) ([][]string, error) {
	out, err := r.run(ctx, "log", "--format=%H %P", "-n", strconv.Itoa(n), hash, "--")
	if err != nil {
		return nil, err
	}
	var commits [][]string
	for line := range strings.SplitSeq(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 0 {
			commits = append(commits, f)
		}
	}
	return commits, nil
}

// MergeBase returns the newest commit both a and b come from, or "" when
// they share no history.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := r.run(ctx, "merge-base", a, b)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Divergence is how far one commit has moved from another: the commits it
// has that the other lacks (ahead), and the other way round (behind).
type Divergence struct {
	Ahead, Behind int
}

// Diverged counts how far head has moved from base.
func (r *Repo) Diverged(ctx context.Context, base, head string) (Divergence, error) {
	out, err := r.run(ctx, "rev-list", "--left-right", "--count", base+"..."+head, "--")
	if err != nil {
		return Divergence{}, err
	}
	var d Divergence
	if _, err := fmt.Sscan(string(out), &d.Behind, &d.Ahead); err != nil {
		return Divergence{}, fmt.Errorf("git rev-list: %q: %w", out, err)
	}
	return d, nil
}

// BranchesDiverged counts how far each branch has moved from the commit
// base, by branch name, in one walk. It needs git 2.41 or later (for
// for-each-ref's ahead-behind); older ones fail.
func (r *Repo) BranchesDiverged(ctx context.Context, base string) (map[string]*Divergence, error) {
	out, err := r.run(ctx, "for-each-ref", "--format=%(refname)%1f%(ahead-behind:"+base+")", "refs/heads")
	if err != nil {
		return nil, err
	}
	counts := map[string]*Divergence{}
	for line := range strings.SplitSeq(string(out), "\n") {
		name, nums, ok := strings.Cut(line, "\x1f")
		if !ok {
			continue
		}
		var d Divergence
		if _, err := fmt.Sscan(nums, &d.Ahead, &d.Behind); err == nil {
			counts[strings.TrimPrefix(name, "refs/heads/")] = &d
		}
	}
	return counts, nil
}

// Since returns up to n commits on any branch committed after since (all of
// them for a zero time), newest first, each once even when several branches
// hold it.
func (r *Repo) Since(ctx context.Context, since time.Time, n int) ([]*Commit, error) {
	args := []string{"log", "--branches", commitFormat, "-n", strconv.Itoa(n)}
	if !since.IsZero() {
		args = append(args, "--since=@"+strconv.FormatInt(since.Unix(), 10))
	}
	out, err := r.run(ctx, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	return parseCommits(out), nil
}

func (r *Repo) Commit(ctx context.Context, hash string) (*Commit, error) {
	out, err := r.run(ctx, "show", "-s", commitFormat, hash)
	if err != nil {
		return nil, ErrNotFound
	}
	cs := parseCommits(out)
	if len(cs) == 0 {
		return nil, ErrNotFound
	}
	return cs[0], nil
}

type TreeEntry struct {
	Mode string
	Type string // blob, tree or commit (submodule)
	Hash string
	Size int64
	Name string
}

// Tree lists the entries of the directory at path in commit hash,
// directories first.
func (r *Repo) Tree(ctx context.Context, hash, path string) ([]TreeEntry, error) {
	spec := hash
	if path != "" {
		spec += ":" + path
	}
	out, err := r.run(ctx, "ls-tree", "-l", "-z", spec)
	if err != nil {
		return nil, ErrNotFound
	}
	var entries []TreeEntry
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, name, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(f[3], 10, 64)
		entries = append(entries, TreeEntry{Mode: f[0], Type: f[1], Hash: f[2], Size: size, Name: name})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return (entries[i].Type == "tree") && (entries[j].Type != "tree")
	})
	return entries, nil
}

// LastCommits finds the newest commit that changed each of names, the
// entries of directory dir in commit hash, keyed by name. It walks the
// directory's history once and stops when every name has one or after limit
// commits; names not reached by then are left out. Messages hold the subject
// only.
func (r *Repo) LastCommits(ctx context.Context, hash, dir string, names []string, limit int) map[string]*Commit {
	found := map[string]*Commit{}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	args := []string{"log", "-z", "--name-only", "--no-renames", "-n", strconv.Itoa(limit),
		"--format=%x1e%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%cn%x1f%ce%x1f%ct%x1f%s", hash, "--"}
	prefix := ""
	if dir != "" {
		args = append(args, ":(literal)"+dir)
		prefix = dir + "/"
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := r.cmd(ctx, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return found
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return found
	}
	// Stopping early kills git, so its exit status says nothing useful.
	defer func() { cancel(); cmd.Wait() }()

	br := bufio.NewReader(out)
	var cur *Commit
	for len(found) < len(want) {
		tok, err := br.ReadString(0)
		if err != nil {
			break
		}
		tok = strings.TrimLeft(strings.TrimSuffix(tok, "\x00"), "\n")
		if strings.HasPrefix(tok, "\x1e") {
			header, file, _ := strings.Cut(tok[1:], "\n")
			if cs := parseCommits([]byte(header)); len(cs) == 1 {
				cur = cs[0]
			}
			tok = file
		}
		name, ok := strings.CutPrefix(tok, prefix)
		if !ok || cur == nil || name == "" {
			continue
		}
		name, _, _ = strings.Cut(name, "/")
		if want[name] && found[name] == nil {
			found[name] = cur
		}
	}
	return found
}

// Objects reads the raw contents of objects by full hash (commits and tags,
// for their signatures), in one git call. Hashes that aren't plain object
// names, and objects that don't exist, are left out.
func (r *Repo) Objects(ctx context.Context, hashes []string) (map[string][]byte, error) {
	var in strings.Builder
	for _, h := range hashes {
		// cat-file would read anything else as a revision or path.
		if (len(h) == 40 || len(h) == 64) && strings.Trim(h, "0123456789abcdef") == "" {
			in.WriteString(h)
			in.WriteByte('\n')
		}
	}
	objects := map[string][]byte{}
	if in.Len() == 0 {
		return objects, nil
	}
	cmd := r.cmd(ctx, "cat-file", "--batch")
	cmd.Stdin = strings.NewReader(in.String())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file: %w", err)
	}
	// Each object is "<hash> <type> <size>\n<contents>\n"; a missing one is
	// "<name> missing\n".
	for len(out) > 0 {
		header, rest, ok := bytes.Cut(out, []byte{'\n'})
		if !ok {
			break
		}
		f := strings.Fields(string(header))
		if len(f) != 3 {
			out = rest
			continue
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size+1 > len(rest) {
			break
		}
		objects[f[0]] = rest[:size]
		out = rest[size+1:]
	}
	return objects, nil
}

// ObjectType returns "tree", "blob" or "commit" for path in commit hash.
func (r *Repo) ObjectType(ctx context.Context, hash, path string) (string, error) {
	if path == "" {
		return "tree", nil
	}
	out, err := r.run(ctx, "cat-file", "-t", hash+":"+path)
	if err != nil {
		return "", ErrNotFound
	}
	return strings.TrimSpace(string(out)), nil
}

// ObjectID returns the id of path in commit hash.
func (r *Repo) ObjectID(ctx context.Context, hash, path string) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--verify", "--quiet", hash+":"+path)
	if err != nil {
		return "", ErrNotFound
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *Repo) BlobSize(ctx context.Context, hash, path string) (int64, error) {
	out, err := r.run(ctx, "cat-file", "-s", hash+":"+path)
	if err != nil {
		return 0, ErrNotFound
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

func (r *Repo) Blob(ctx context.Context, hash, path string) ([]byte, error) {
	out, err := r.run(ctx, "cat-file", "blob", hash+":"+path)
	if err != nil {
		return nil, ErrNotFound
	}
	return out, nil
}

// WriteBlob streams a blob to w without buffering it in memory.
func (r *Repo) WriteBlob(ctx context.Context, w io.Writer, hash, path string) error {
	cmd := r.cmd(ctx, "cat-file", "blob", hash+":"+path)
	cmd.Stdout = w
	return cmd.Run()
}

// WriteArchive streams an archive of hash to w: a gzipped tarball for
// format "tar.gz", or "zip".
//
// git archive runs two kinds of programs named in a repository's config: the
// compressor (tar.<format>.command) and the smudge filters selected by
// .gitattributes. Compression happens here instead (git zips by itself),
// and every filter driver the repository defines is switched off for the
// run.
func (r *Repo) WriteArchive(ctx context.Context, w io.Writer, hash, prefix, format string) error {
	var disable []string
	out, err := r.run(ctx, "config", "--name-only", "--get-regexp", `^filter\..*\.(smudge|process|clean|required)$`)
	if err == nil {
		seen := map[string]bool{}
		for _, key := range strings.Fields(string(out)) {
			driver := key[:strings.LastIndex(key, ".")]
			if !seen[driver] {
				seen[driver] = true
				// Files come out as stored in the repository, as with
				// cgit; "required" would otherwise make the run fail.
				disable = append(disable, driver+".smudge=", driver+".process=", driver+".required=false")
			}
		}
	}
	if format == "zip" {
		cmd := r.cmdWithConfig(ctx, disable, "archive", "--format=zip", "--prefix="+prefix+"/", hash)
		cmd.Stdout = w
		return cmd.Run()
	}
	zw := gzip.NewWriter(w)
	cmd := r.cmdWithConfig(ctx, disable, "archive", "--format=tar", "--prefix="+prefix+"/", hash)
	cmd.Stdout = zw
	if err := cmd.Run(); err != nil {
		return err
	}
	return zw.Close()
}

// WritePatch streams the commit as an mbox patch, as git format-patch would.
func (r *Repo) WritePatch(ctx context.Context, w io.Writer, hash string) error {
	cmd := r.cmd(ctx, "format-patch", "-1", "--stdout", hash)
	cmd.Stdout = w
	return cmd.Run()
}

// WritePatches streams the commits reachable from to but not from from as
// mbox patches, oldest first and without merges, as git format-patch would.
func (r *Repo) WritePatches(ctx context.Context, w io.Writer, from, to string) error {
	cmd := r.cmd(ctx, "format-patch", "--stdout", from+".."+to)
	cmd.Stdout = w
	return cmd.Run()
}

type Ref struct {
	Name      string
	Hash      string // the ref's object (a tag object for annotated tags)
	Commit    string // the commit it ultimately points to
	Annotated bool
	When      time.Time
	Tagger    string // who made an annotated tag
	Subject   string
	Message   string
}

// Refs lists refs under prefix (refs/heads or refs/tags), newest first.
func (r *Repo) Refs(ctx context.Context, prefix string) ([]Ref, error) {
	return r.refs(ctx, prefix, prefix)
}

// Tag returns the tag called name, or ErrNotFound.
func (r *Repo) Tag(ctx context.Context, name string) (*Ref, error) {
	if name == "" || strings.ContainsAny(name, "\x00\n") {
		return nil, ErrNotFound
	}
	// The pattern also matches tags below name ("v1/rc"), and globs in it
	// match others; only the exact name counts.
	refs, err := r.refs(ctx, "refs/tags", "refs/tags/"+name)
	if err != nil {
		return nil, err
	}
	for _, ref := range refs {
		if ref.Name == name {
			return &ref, nil
		}
	}
	return nil, ErrNotFound
}

// refs lists the refs matching pattern, named relative to prefix.
func (r *Repo) refs(ctx context.Context, prefix, pattern string) ([]Ref, error) {
	out, err := r.run(ctx, "for-each-ref", "--sort=-version:refname", "--sort=-creatordate",
		"--format=%(refname)%1f%(objectname)%1f%(objecttype)%1f%(*objectname)%1f%(creatordate:unix)%1f%(taggername)%1f%(contents:subject)%1f%(contents)%1e",
		pattern)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		f := strings.SplitN(rec, "\x1f", 8)
		if len(f) != 8 {
			continue
		}
		ref := Ref{
			Name:    strings.TrimPrefix(strings.TrimPrefix(f[0], prefix), "/"),
			Hash:    f[1],
			Commit:  f[1],
			When:    unix(f[4]),
			Tagger:  f[5],
			Subject: f[6],
			Message: stripSignature(f[7]),
		}
		if f[2] == "tag" {
			ref.Annotated = true
			ref.Commit = f[3]
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func stripSignature(msg string) string {
	for _, marker := range []string{"-----BEGIN PGP SIGNATURE-----", "-----BEGIN SSH SIGNATURE-----"} {
		if i := strings.Index(msg, marker); i >= 0 {
			msg = msg[:i]
		}
	}
	return strings.TrimSpace(msg)
}

// State sums up the repository's refs and HEAD, which change with every
// push, new tag or mirror update: equal states show the same history. newest
// is the latest commit or tag date among them.
func (r *Repo) State(ctx context.Context) (state string, newest time.Time, err error) {
	out, err := r.run(ctx, "for-each-ref", "--format=%(refname) %(objectname) %(creatordate:unix)")
	if err != nil {
		return "", time.Time{}, err
	}
	head, err := os.ReadFile(r.Dir + "/HEAD")
	if err != nil {
		return "", time.Time{}, err
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		if i := strings.LastIndexByte(line, ' '); i >= 0 {
			if t := unix(line[i+1:]); t.After(newest) {
				newest = t
			}
		}
	}
	sum := sha256.Sum256(append(head, out...))
	return hex.EncodeToString(sum[:]), newest, nil
}

// LastUpdated returns the newest committer date across all branches.
func (r *Repo) LastUpdated(ctx context.Context) (when time.Time, committer string) {
	out, err := r.run(ctx, "for-each-ref", "--count=1", "--sort=-committerdate",
		"--format=%(committerdate:unix)%1f%(committername)", "refs/heads")
	if err != nil {
		return time.Time{}, ""
	}
	date, name, _ := strings.Cut(strings.TrimSpace(string(out)), "\x1f")
	return unix(date), name
}

type BlameCommit struct {
	Hash    string
	Author  string
	When    time.Time
	Summary string
}

type BlameLine struct {
	Commit *BlameCommit
	Start  bool // first line of a run of lines from the same commit
}

// Blame returns one entry per line of path at hash. Line contents are
// omitted; render them from the blob so highlighting stays consistent.
func (r *Repo) Blame(ctx context.Context, hash, path string) ([]BlameLine, error) {
	out, err := r.run(ctx, "blame", "--porcelain", "--no-textconv", hash, "--", path)
	if err != nil {
		return nil, ErrNotFound
	}
	commits := map[string]*BlameCommit{}
	var lines []BlameLine
	var cur *BlameCommit
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "\t") {
			prev := len(lines) > 0 && lines[len(lines)-1].Commit == cur
			lines = append(lines, BlameLine{Commit: cur, Start: !prev})
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 3 && (len(f[0]) == 40 || len(f[0]) == 64) && isNum(f[1]) && isNum(f[2]) {
			if c, ok := commits[f[0]]; ok {
				cur = c
			} else {
				cur = &BlameCommit{Hash: f[0]}
				commits[f[0]] = cur
			}
			continue
		}
		if cur == nil {
			continue
		}
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "author":
			cur.Author = val
		case "author-time":
			cur.When = unix(val)
		case "summary":
			cur.Summary = val
		}
	}
	return lines, sc.Err()
}

func isNum(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}
