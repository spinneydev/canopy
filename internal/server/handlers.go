package server

import (
	"bytes"
	"context"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"spinney.dev/canopy/internal/git"
	"spinney.dev/canopy/internal/render"
)

const (
	logPageSize  = 20
	tagsPageSize = 20
	maxBlobBytes = 2 << 20
)

// section groups repositories by the folder they live in, like cgit's
// section-from-path. Top-level repositories have an empty Name.
type section struct {
	Name  string
	Repos []*Repo
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	_, list := s.repos.get()
	q := strings.TrimSpace(r.URL.Query().Get("search"))
	// Most recently updated first by default; ?sort=name for alphabetical.
	sortBy := r.URL.Query().Get("sort")
	if sortBy != "name" {
		sortBy = "idle"
	}

	var repos []*Repo
	for _, repo := range list {
		if q == "" || strings.Contains(strings.ToLower(repo.Name+" "+repo.Description), strings.ToLower(q)) {
			repos = append(repos, repo)
		}
	}
	sort.SliceStable(repos, func(i, j int) bool {
		a, b := repos[i], repos[j]
		if sortBy == "idle" && !a.Updated.Equal(b.Updated) {
			return a.Updated.After(b.Updated)
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	var sections []*section
	byName := map[string]*section{}
	for _, repo := range repos {
		name := strings.TrimSuffix(repo.Owner(), "/")
		sec, ok := byName[name]
		if !ok {
			sec = &section{Name: name}
			byName[name] = sec
			sections = append(sections, sec)
		}
		sec.Repos = append(sec.Repos, repo)
	}
	// Top-level repositories first (they belong to the site's owner), then
	// the owner's own folder, then the other sections alphabetically.
	rank := func(sec *section) int {
		switch {
		case sec.Name == "":
			return 0
		case s.cfg.Owner != "" && sec.Name == s.cfg.Owner:
			return 1
		}
		return 2
	}
	sort.SliceStable(sections, func(i, j int) bool {
		if ri, rj := rank(sections[i]), rank(sections[j]); ri != rj {
			return ri < rj
		}
		return strings.ToLower(sections[i].Name) < strings.ToLower(sections[j].Name)
	})

	s.render(w, r, http.StatusOK, "index", map[string]any{
		"Title": s.cfg.Title, "Tab": "index",
		"Sections": sections, "Count": len(repos), "Total": len(list),
		"Search": q, "Sort": sortBy,
	})
}

// label is a branch or tag shown next to a commit.
type label struct {
	Name, Kind, URL string
}

func (s *Server) labels(r *http.Request, repo *Repo) map[string][]label {
	m := map[string][]label{}
	for _, kind := range []string{"heads", "tags"} {
		refs, _ := repo.Refs(r.Context(), "refs/"+kind)
		for _, ref := range refs {
			l := label{Name: ref.Name, Kind: "branch", URL: repo.URL("log", ref.Name)}
			if kind == "tags" {
				l.Kind, l.URL = "tag", repo.URL("tag", ref.Name)
			}
			m[ref.Commit] = append(m[ref.Commit], l)
		}
	}
	return m
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request, repo *Repo) {
	ctx := r.Context()
	data := map[string]any{
		"Title": repo.Name, "Repo": repo, "Tab": "summary",
		"CloneURL": s.cloneURL(r, repo),
	}
	if repo.Upstream != "" {
		synced, failure := repo.SyncStatus(ctx)
		data["Synced"], data["SyncError"] = synced, failure
		// Updates run every 30 minutes; two hours without one means they stopped.
		data["SyncStale"] = !synced.IsZero() && time.Since(synced) > 2*time.Hour
	}
	ref, hash, err := s.resolve(ctx, repo, "")
	data["Ref"] = ref
	if err != nil {
		data["Empty"] = true
		s.render(w, r, http.StatusOK, "summary", data)
		return
	}
	commits, err := repo.Log(ctx, hash, "", 3)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data["Commits"] = commits
	data["Labels"] = s.labels(r, repo)
	if tags, _ := repo.Refs(ctx, "refs/tags"); len(tags) > 0 {
		data["LatestTag"] = tags[0]
	}
	if entries, err := repo.Tree(ctx, hash, ""); err == nil {
		data["Readme"] = s.readme(r, repo, ref, hash, "", entries)
		data["Licenses"] = licenses(ctx, repo, hash, entries)
	}
	s.render(w, r, http.StatusOK, "summary", data)
}

// readme renders the first README found among entries of directory dir.
func (s *Server) readme(r *http.Request, repo *Repo, ref, hash, dir string, entries []git.TreeEntry) template.HTML {
	var best *git.TreeEntry
	for i, e := range entries {
		if e.Type != "blob" || e.Size > maxBlobBytes {
			continue
		}
		lower := strings.ToLower(e.Name)
		if lower == "readme.md" || lower == "readme.markdown" {
			best = &entries[i]
			break
		}
		if best == nil && (lower == "readme" || strings.HasPrefix(lower, "readme.")) {
			best = &entries[i]
		}
	}
	if best == nil {
		return ""
	}
	content, err := repo.Blob(r.Context(), hash, path.Join(dir, best.Name))
	if err != nil {
		return ""
	}
	if isMarkdown(best.Name) {
		return markdown(repo, ref, dir, content)
	}
	return template.HTML(`<pre class="readme-plain">` + template.HTMLEscapeString(string(content)) + `</pre>`)
}

func isMarkdown(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	}
	return false
}

// markdown renders a document from directory dir of the repository, so its
// relative links and images resolve against the same ref.
func markdown(repo *Repo, ref, dir string, content []byte) template.HTML {
	root := "/" + repo.Name
	return render.Markdown(content, render.Links{
		TreeRoot: root + "/tree/" + ref + "/item",
		RawRoot:  root + "/blob/" + ref + "/item",
		Dir:      dir,
	})
}

type crumb struct{ Name, Path string }

func crumbs(p string) []crumb {
	if p == "" {
		return nil
	}
	var out []crumb
	segs := strings.Split(p, "/")
	for i, s := range segs {
		out = append(out, crumb{s, strings.Join(segs[:i+1], "/")})
	}
	return out
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request, repo *Repo, ref, p string) {
	ctx := r.Context()
	ref, hash, err := s.resolve(ctx, repo, ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	typ, err := repo.ObjectType(ctx, hash, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := map[string]any{
		"Title": repo.Name + ": " + orRoot(p), "Repo": repo, "Tab": "tree",
		"Ref": ref, "Hash": hash, "Path": p, "Crumbs": crumbs(p),
	}
	if last, err := repo.Log(ctx, hash, p, 1); err == nil && len(last) > 0 {
		data["Last"] = last[0]
	}
	switch typ {
	case "tree":
		entries, err := repo.Tree(ctx, hash, p)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data["Entries"] = entries
		if s.cfg.TreeCommits {
			data["Commits"] = s.lastCommits(ctx, repo, hash, p, entries)
		}
		data["Readme"] = s.readme(r, repo, ref, hash, p, entries)
		s.render(w, r, http.StatusOK, "tree", data)
	case "blob":
		s.blob(w, r, repo, hash, p, data)
	default:
		s.notFound(w, r)
	}
}

// Bounds on finding each tree entry's last commit: entries untouched for
// longer than this (on a big history) are shown without one.
const (
	treeCommitsWalk    = 5000
	treeCommitsTimeout = 3 * time.Second
)

func (s *Server) lastCommits(ctx context.Context, repo *Repo, hash, dir string, entries []git.TreeEntry) map[string]*git.Commit {
	ctx, cancel := context.WithTimeout(ctx, treeCommitsTimeout)
	defer cancel()
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return repo.LastCommits(ctx, hash, dir, names, treeCommitsWalk)
}

func orRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".bmp": true, ".ico": true, ".svg": true, ".avif": true,
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request, repo *Repo, hash, p string, data map[string]any) {
	ctx := r.Context()
	data["Canonical"] = s.canonical(r)
	size, err := repo.BlobSize(ctx, hash, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data["Size"] = size
	switch {
	case imageExts[strings.ToLower(path.Ext(p))]:
		data["Image"] = true
	case size > maxBlobBytes:
		data["TooLarge"] = true
	default:
		content, err := repo.Blob(ctx, hash, p)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		switch {
		case isBinary(content):
			data["Binary"] = true
		case isMarkdown(p) && r.URL.Query().Get("plain") == "":
			data["Markdown"] = markdown(repo, data["Ref"].(string), parent(p), content)
		default:
			data["Lines"] = codeLines(r.URL, render.Lines(p, content))
		}
		data["IsMarkdown"] = isMarkdown(p)
	}
	s.render(w, r, http.StatusOK, "blob", data)
}

// codeLine is a line of a file view: its number, the link its number
// makes, whether it is selected, and its highlighted code.
type codeLine struct {
	No   int
	Href string
	Sel  bool
	Code template.HTML
}

// codeLines numbers code for a file view whose URL is u. ?lines=N or
// ?lines=N-M selects lines without any JavaScript, through the links on
// the line numbers: with nothing selected, a number selects its line; with
// one line selected, another number extends the selection to it (and its
// own clears it); with a range selected, a number starts again.
func codeLines(u *url.URL, code []template.HTML) []codeLine {
	q := u.Query()
	start, end := selection(q.Get("lines"), len(code))
	// Links stay on this page, keeping its other parameters (?plain=1).
	base := "./" + url.PathEscape(path.Base(u.Path))
	link := func(from, to, jump int) string {
		switch {
		case from == 0:
			q.Del("lines")
		case from == to:
			q.Set("lines", strconv.Itoa(from))
		default:
			q.Set("lines", strconv.Itoa(from)+"-"+strconv.Itoa(to))
		}
		href := base
		if enc := q.Encode(); enc != "" {
			href += "?" + enc
		}
		return href + "#L" + strconv.Itoa(jump)
	}
	lines := make([]codeLine, len(code))
	for i, c := range code {
		n := i + 1
		l := codeLine{No: n, Sel: start > 0 && start <= n && n <= end, Code: c}
		switch {
		case start == 0 || start != end:
			l.Href = link(n, n, n)
		case n == start:
			l.Href = link(0, 0, n)
		default:
			l.Href = link(min(n, start), max(n, start), min(n, start))
		}
		lines[i] = l
	}
	return lines
}

// canonical is a file page's address without ?lines, so that search
// engines count every selection of a file as the file itself.
func (s *Server) canonical(r *http.Request) string {
	q := r.URL.Query()
	q.Del("lines")
	u := s.baseURL(r) + r.URL.EscapedPath()
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	return u
}

// selection reads ?lines (N or N-M, either way round) for a file of n
// lines; zeros mean no selection.
func selection(v string, n int) (start, end int) {
	a, b, isRange := strings.Cut(v, "-")
	start, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0
	}
	end = start
	if isRange {
		if end, err = strconv.Atoi(b); err != nil {
			return 0, 0
		}
	}
	if start > end {
		start, end = end, start
	}
	if start < 1 || start > n {
		return 0, 0
	}
	return start, min(end, n)
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

func (s *Server) raw(w http.ResponseWriter, r *http.Request, repo *Repo, ref, p string) {
	ctx := r.Context()
	_, hash, err := s.resolve(ctx, repo, ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if typ, err := repo.ObjectType(ctx, hash, p); err != nil || typ != "blob" {
		s.notFound(w, r)
		return
	}
	id, err := repo.ObjectID(ctx, hash, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// By the blob, not the commit: a file stays cached until it changes.
	h := w.Header()
	h.Set("Cache-Control", "public, max-age=300")
	if notModified(w, r, `"`+id+`"`) {
		return
	}
	// Never let repository content render as a page on our origin: images
	// keep their type, everything else is plain text, and the sandbox CSP
	// neutralizes scripts in SVG.
	ctype := "text/plain; charset=utf-8"
	if ext := strings.ToLower(path.Ext(p)); imageExts[ext] {
		ctype = mime.TypeByExtension(ext)
	}
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	repo.WriteBlob(ctx, w, hash, p)
}

type blameRow struct {
	git.BlameLine
	codeLine
}

func (s *Server) blame(w http.ResponseWriter, r *http.Request, repo *Repo, ref, p string) {
	ctx := r.Context()
	ref, hash, err := s.resolve(ctx, repo, ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	size, err := repo.BlobSize(ctx, hash, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data := map[string]any{
		"Title": repo.Name + ": blame " + p, "Repo": repo, "Tab": "tree",
		"Ref": ref, "Hash": hash, "Path": p, "Crumbs": crumbs(p), "Size": size,
		"Canonical": s.canonical(r),
	}
	if size > maxBlobBytes {
		data["TooLarge"] = true
		s.render(w, r, http.StatusOK, "blame", data)
		return
	}
	content, err := repo.Blob(ctx, hash, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if isBinary(content) {
		data["Binary"] = true
		s.render(w, r, http.StatusOK, "blame", data)
		return
	}
	blame, err := repo.Blame(ctx, hash, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	code := render.Lines(p, content)
	code = append(code, make([]template.HTML, max(0, len(blame)-len(code)))...)
	lines := codeLines(r.URL, code[:len(blame)])
	rows := make([]blameRow, len(blame))
	for i, b := range blame {
		rows[i] = blameRow{BlameLine: b, codeLine: lines[i]}
	}
	data["Rows"] = rows
	s.render(w, r, http.StatusOK, "blame", data)
}

func (s *Server) log(w http.ResponseWriter, r *http.Request, repo *Repo, ref, p string) {
	ctx := r.Context()
	ref, hash, err := s.resolve(ctx, repo, ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	start := hash
	if from := r.URL.Query().Get("from"); from != "" {
		if start, err = repo.ResolveCommit(ctx, from); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	commits, err := repo.Log(ctx, start, p, logPageSize+1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	next := ""
	if len(commits) > logPageSize {
		next = commits[logPageSize].Hash
		commits = commits[:logPageSize]
	}
	title := repo.Name + ": log"
	if p != "" {
		title += " " + p
	}
	hashes := make([]string, len(commits))
	for i, c := range commits {
		hashes[i] = c.Hash
	}
	s.render(w, r, http.StatusOK, "log", map[string]any{
		"Title": title, "Repo": repo, "Tab": "log", "Ref": ref, "Path": p,
		"Crumbs": crumbs(p), "Commits": commits, "Next": next,
		"Labels": s.labels(r, repo), "Paged": start != hash,
		"Sigs": s.signatures(ctx, repo, hashes),
	})
}

// cutRaw splits off name's ".diff" or ".patch", which ask for a commit as
// plain text.
func cutRaw(name string) (rest, ext string) {
	for _, ext := range []string{".diff", ".patch"} {
		if rest, ok := strings.CutSuffix(name, ext); ok {
			return rest, ext
		}
	}
	return name, ""
}

// plainText marks a response as text that browsers must not take for
// anything else.
func plainText(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request, repo *Repo, rev string) {
	ctx := r.Context()
	rev, raw := cutRaw(rev)
	hash, err := repo.ResolveCommit(ctx, rev)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	c, err := repo.Commit(ctx, hash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if raw != "" {
		if notModified(w, r, `"`+hash+`"`) {
			return
		}
		plainText(w)
		if raw == ".patch" {
			repo.WritePatch(ctx, w, hash)
		} else {
			repo.WriteDiff(ctx, w, c.FirstParent(), hash)
		}
		return
	}
	diff, err := repo.Diff(ctx, c)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "commit", map[string]any{
		"Title": repo.Name + ": " + c.Subject(), "Repo": repo, "Tab": "log",
		"C": c, "Diff": diff, "Labels": s.labels(r, repo),
		"Sigs": s.signatures(ctx, repo, []string{c.Hash}),
	})
}

func (s *Server) refs(w http.ResponseWriter, r *http.Request, repo *Repo) {
	ctx := r.Context()
	branches, err := repo.Refs(ctx, "refs/heads")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tags, err := repo.Refs(ctx, "refs/tags")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Tags come newest first, a page at a time: ?page=2 and on.
	pages := max(1, (len(tags)+tagsPageSize-1)/tagsPageSize)
	page := 1
	if q := r.URL.Query().Get("page"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > pages {
			s.notFound(w, r)
			return
		}
		page = n
	}
	start := (page - 1) * tagsPageSize
	shown := tags[start:min(start+tagsPageSize, len(tags))]
	// Only annotated tags carry a signature of their own.
	var signed []string
	for _, t := range shown {
		if t.Annotated {
			signed = append(signed, t.Hash)
		}
	}
	s.render(w, r, http.StatusOK, "refs", map[string]any{
		"Title": repo.Name + ": refs", "Repo": repo, "Tab": "refs",
		"Branches": branches, "Default": repo.DefaultBranch(ctx),
		"Tags": shown, "TagCount": len(tags), "Page": page, "Pages": pages,
		"Sigs": s.signatures(ctx, repo, signed),
	})
}

func (s *Server) tag(w http.ResponseWriter, r *http.Request, repo *Repo, name string) {
	ctx := r.Context()
	tag, err := repo.Tag(ctx, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Only annotated tags carry a signature of their own.
	var signed []string
	if tag.Annotated {
		signed = []string{tag.Hash}
	}
	data := map[string]any{
		"Title": repo.Name + ": " + tag.Name, "Repo": repo, "Tab": "refs",
		"Tag": tag, "Labels": s.labels(r, repo), "Sigs": s.signatures(ctx, repo, signed),
	}
	// A tag can name a tree or a blob, which have no commit to show.
	if hash, err := repo.ResolveCommit(ctx, tag.Hash); err == nil {
		if c, err := repo.Commit(ctx, hash); err == nil {
			data["C"] = c
		}
	}
	s.render(w, r, http.StatusOK, "tag", data)
}

func (s *Server) archive(w http.ResponseWriter, r *http.Request, repo *Repo, name string) {
	ref, ok := strings.CutSuffix(name, ".tar.gz")
	if !ok {
		s.notFound(w, r)
		return
	}
	hash, err := repo.ResolveCommit(r.Context(), ref)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Weak: the same files, though another git or gzip may pack them
	// differently.
	if notModified(w, r, `W/"`+hash+`"`) {
		return
	}
	prefix := repo.Base() + "-" + strings.ReplaceAll(strings.TrimPrefix(ref, "v"), "/", "-")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": prefix + ".tar.gz"}))
	repo.WriteArchive(r.Context(), w, hash, prefix)
}
