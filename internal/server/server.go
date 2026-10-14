// Package server is the HTTP front end: routing, handlers and templates.
package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"spinney.dev/canopy/internal/config"
	"spinney.dev/canopy/internal/fonts"
	"spinney.dev/canopy/internal/git"
	"spinney.dev/canopy/internal/render"
	"spinney.dev/canopy/internal/sigcheck"
)

//go:embed templates static
var assets embed.FS

type Server struct {
	cfg     *config.Config
	repos   *registry
	pages   map[string]*template.Template
	static  http.Handler
	chroma  []byte
	theme   []byte     // [theme] colours and font names; empty if the default
	fonts   *fonts.Set // [theme] fonts, served from here; nil if unset
	gitPath string
	// Slots bounding concurrent repository requests. Downloads (clones and
	// tarballs) have their own, smaller pool so they can never starve
	// ordinary page views.
	pageSlots     chan struct{}
	downloadSlots chan struct{}
	version       string // content hash appended to asset URLs for cache busting
	started       time.Time
	description   template.HTML // the site description, rendered from markdown

	activityMu    sync.Mutex
	activityCache *activityData // what activity pages are cut from, read once a minute at most

	keys    *sigcheck.Keys // [signatures]; nil turns the badges off
	sigMu   sync.Mutex
	sigSeen map[string]sigcheck.Result // by object hash, which pins the contents
}

func New(cfg *config.Config) (*Server, error) {
	if cfg.Depth <= 0 {
		cfg.Depth = 3
	}
	// git http-backend runs in another working directory, so repository
	// paths must not be relative.
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, err
	}
	cfg.Root = root
	git.Binary = cfg.Git
	gitPath, err := exec.LookPath(git.Binary)
	if err != nil {
		return nil, fmt.Errorf("git binary: %w", err)
	}
	themeCSS, chroma, err := themeStyles(cfg)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:           cfg,
		repos:         &registry{root: cfg.Root, maxDepth: cfg.Depth, overrides: cfg.Repos, owner: cfg.Owner, mirrors: cfg.Mirrors, ttl: 15 * time.Second},
		pages:         map[string]*template.Template{},
		chroma:        chroma,
		theme:         themeCSS,
		gitPath:       gitPath,
		pageSlots:     make(chan struct{}, max(8, 4*runtime.NumCPU())),
		downloadSlots: make(chan struct{}, max(4, runtime.NumCPU())),
		started:       time.Now(),
		description:   render.Inline(cfg.Description),
	}
	if len(cfg.Signatures.Keys) > 0 {
		if s.keys, err = sigcheck.NewKeys(cfg.Signatures.Keys); err != nil {
			return nil, fmt.Errorf("signatures: keys: %w", err)
		}
		s.sigSeen = map[string]sigcheck.Result{}
	}
	css, _ := assets.ReadFile("static/style.css")
	sum := sha256.Sum256(slices.Concat(css, s.chroma, s.theme))
	s.version = hex.EncodeToString(sum[:4])

	// Fonts download in the background; pages use the fallbacks until then.
	if set := fonts.New(fonts.CacheDir(), cfg.Theme.Sans, cfg.Theme.Mono); !set.Empty() {
		s.fonts = set
		go set.Fetch(context.Background())
	}

	staticFS, _ := fs.Sub(assets, "static")
	s.static = http.StripPrefix("/_static/", http.FileServerFS(staticFS))

	pages, _ := fs.Glob(assets, "templates/*.html")
	for _, p := range pages {
		name := strings.TrimSuffix(path.Base(p), ".html")
		if name == "layout" || name == "partials" {
			continue
		}
		t, err := template.New(name).Funcs(funcs).ParseFS(assets,
			"templates/layout.html", "templates/partials.html", p)
		if err != nil {
			return nil, err
		}
		s.pages[name] = t
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	defer func() {
		log.Printf("%s %s %s", r.Method, r.URL.RequestURI(), time.Since(start).Round(time.Millisecond))
	}()

	switch {
	case r.URL.Path == "/":
		s.index(w, r)
		return
	case r.URL.Path == "/favicon.ico":
		s.favicon(w, r)
		return
	case r.URL.Path == "/about" && s.cfg.About != "":
		// Shadows a repository named "about", as in cgit.
		s.about(w, r)
		return
	case r.URL.Path == "/activity" && s.cfg.Activity.Enabled:
		// Shadows a repository named "activity", like the about page.
		s.activity(w, r)
		return
	case r.URL.Path == "/_static/chroma.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(s.chroma)
		return
	case r.URL.Path == "/_static/theme.css" && len(s.theme) > 0:
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Write(s.theme)
		return
	case (r.URL.Path == "/_static/fonts.css" || strings.HasPrefix(r.URL.Path, fonts.Path)) && s.fonts != nil:
		s.serveFonts(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/_static/"):
		w.Header().Set("Cache-Control", "public, max-age=3600")
		s.static.ServeHTTP(w, r)
		return
	}

	// The repository is the longest prefix of the path naming a known repo,
	// which allows nested names like "~alice/project".
	byName, _ := s.repos.get()
	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i := len(segs); i > 0; i-- {
		name := strings.Join(segs[:i], "/")
		if repo, ok := byName[strings.TrimSuffix(name, ".git")]; ok {
			s.serveRepo(w, r, repo, segs[i:])
			return
		}
	}
	s.notFound(w, r)
}

// Limits on the git work a request can cause. Pages get a short deadline;
// clones and tarballs of big repositories legitimately take longer.
const (
	pageTimeout     = 30 * time.Second
	transferTimeout = 10 * time.Minute
	queueTimeout    = 10 * time.Second
	maxUploadBody   = 64 << 20 // a fetch negotiation, not a push
	// A download whose client stops reading is cut off: each write must
	// finish within stallTimeout, which demands roughly 32 KiB per 20 s.
	stallTimeout = 20 * time.Second
)

// acquire takes a slot from pool, waiting briefly when all are busy.
func acquire(r *http.Request, pool chan struct{}) (release func(), ok bool) {
	ctx, cancel := context.WithTimeout(r.Context(), queueTimeout)
	defer cancel()
	select {
	case pool <- struct{}{}:
		return func() { <-pool }, true
	case <-ctx.Done():
		return nil, false
	}
}

// progressWriter enforces stallTimeout on a download (see above).
type progressWriter struct {
	http.ResponseWriter
	rc *http.ResponseController
}

func newProgressWriter(w http.ResponseWriter) *progressWriter {
	return &progressWriter{w, http.NewResponseController(w)}
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.rc.SetWriteDeadline(time.Now().Add(stallTimeout))
	return p.ResponseWriter.Write(b)
}

func (p *progressWriter) Unwrap() http.ResponseWriter { return p.ResponseWriter }

func (s *Server) serveRepo(w http.ResponseWriter, r *http.Request, repo *Repo, rest []string) {
	clone := len(rest) == 2 && rest[0] == "info" && rest[1] == "refs" && r.URL.Query().Get("service") == "git-upload-pack" ||
		len(rest) == 1 && rest[0] == "git-upload-pack" && r.Method == http.MethodPost
	download := clone || len(rest) > 0 && rest[0] == "archive"
	// Mirrors are browsed here but cloned and downloaded from where they
	// come from.
	if download && repo.Upstream != "" {
		http.Error(w, repo.Name+" is a mirror; clone it from "+repo.Upstream, http.StatusForbidden)
		return
	}

	pool := s.pageSlots
	if download {
		pool = s.downloadSlots
		w = newProgressWriter(w)
	}
	release, ok := acquire(r, pool)
	if !ok {
		w.Header().Set("Retry-After", "10")
		http.Error(w, "server busy, try again shortly", http.StatusServiceUnavailable)
		return
	}
	defer release()

	// Smart HTTP clone support (read-only).
	if clone {
		ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBody)
		s.gitHTTP(w, r, repo, "/"+strings.Join(rest, "/"))
		return
	}

	timeout := pageTimeout
	if download {
		timeout = transferTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := ""
	if len(rest) > 0 {
		action, rest = rest[0], rest[1:]
	}
	// URLs follow sourcehut: /<action>/<ref...>/item/<path...>. The "item"
	// separator lets ref names contain slashes.
	ref, p := splitItem(rest)
	p = strings.Trim(path.Clean("/"+p), "/")

	switch action {
	case "":
		s.summary(w, r, repo)
	case "tree":
		s.tree(w, r, repo, ref, p)
	case "blob":
		s.raw(w, r, repo, ref, p)
	case "blame":
		s.blame(w, r, repo, ref, p)
	case "log":
		s.log(w, r, repo, ref, p)
	case "commit":
		s.commit(w, r, repo, strings.Join(rest, "/"))
	case "refs":
		s.refs(w, r, repo)
	case "log.atom", "tags.atom":
		s.feed(w, r, repo, strings.TrimSuffix(action, ".atom"))
	case "archive":
		s.archive(w, r, repo, strings.Join(rest, "/"))
	default:
		s.notFound(w, r)
	}
}

func splitItem(segs []string) (ref, p string) {
	for i := 1; i < len(segs); i++ {
		if segs[i] == "item" {
			return strings.Join(segs[:i], "/"), strings.Join(segs[i+1:], "/")
		}
	}
	return strings.Join(segs, "/"), ""
}

func (s *Server) gitHTTP(w http.ResponseWriter, r *http.Request, repo *Repo, pathInfo string) {
	r2 := r.Clone(r.Context())
	r2.URL.Path = pathInfo
	h := &cgi.Handler{
		Path: s.gitPath,
		Args: []string{"http-backend"},
		Env: append(git.Env(),
			"GIT_PROJECT_ROOT="+repo.Dir,
			"GIT_HTTP_EXPORT_ALL=1",
		),
	}
	h.ServeHTTP(w, r2)
}

// resolve maps a ref from the URL to a commit, defaulting to HEAD's branch.
func (s *Server) resolve(ctx context.Context, repo *Repo, ref string) (string, string, error) {
	if ref == "" {
		ref = repo.DefaultBranch(ctx)
	}
	hash, err := repo.ResolveCommit(ctx, ref)
	return ref, hash, err
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data map[string]any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "missing template "+page, http.StatusInternalServerError)
		return
	}
	data["Site"] = s.cfg.Title
	data["Header"] = cmp.Or(s.cfg.Header, s.cfg.Title)
	data["SiteDescription"] = s.description
	data["HasAbout"] = s.cfg.About != ""
	data["HasActivity"] = s.cfg.Activity.Enabled
	data["HasTheme"] = len(s.theme) > 0
	data["HasFonts"] = s.fonts != nil
	data["V"] = s.version
	data["Started"] = s.started
	if _, ok := data["Tab"]; !ok {
		data["Tab"] = ""
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		log.Printf("render %s: %v", page, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; font-src 'self'; img-src * data:; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "error", map[string]any{
		"Title": "Not found", "Message": "The page you requested does not exist.",
	})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, git.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if errors.Is(err, context.Canceled) {
		return
	}
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		s.render(w, r, http.StatusServiceUnavailable, "error", map[string]any{
			"Title": "Too slow", "Message": "This page took too long to generate.",
		})
		return
	}
	log.Printf("error: %s: %v", r.URL.Path, err)
	s.render(w, r, http.StatusInternalServerError, "error", map[string]any{
		"Title": "Error", "Message": "Something went wrong reading the repository.",
	})
}

// maxSigSeen bounds the signature cache; it starts over when full.
const maxSigSeen = 100000

// signatures checks the signatures of commits and tags, by hash, for the
// badges. It returns nil when [signatures] is off or the objects can't be
// read, and pages then show no badges.
func (s *Server) signatures(ctx context.Context, repo *Repo, hashes []string) map[string]sigcheck.Result {
	if s.keys == nil {
		return nil
	}
	found := map[string]sigcheck.Result{}
	var todo []string
	s.sigMu.Lock()
	for _, h := range hashes {
		if res, ok := s.sigSeen[h]; ok {
			found[h] = res
		} else {
			todo = append(todo, h)
		}
	}
	s.sigMu.Unlock()
	if len(todo) == 0 {
		return found
	}
	objects, err := repo.Objects(ctx, todo)
	if err != nil {
		log.Printf("%s: reading signatures: %v", repo.Name, err)
		return nil
	}
	s.sigMu.Lock()
	defer s.sigMu.Unlock()
	if len(s.sigSeen)+len(objects) > maxSigSeen {
		clear(s.sigSeen)
	}
	for h, raw := range objects {
		res := s.keys.Object(raw, len(h) == 64)
		found[h] = res
		s.sigSeen[h] = res
	}
	return found
}

func (s *Server) cloneURL(r *http.Request, repo *Repo) string {
	return s.baseURL(r) + repo.URL()
}

// baseURL is the site's address, for links that must be absolute (clone
// URLs, feeds): [clone] http from the config, else the address the visitor
// came in on.
func (s *Server) baseURL(r *http.Request) string {
	if base := strings.TrimSuffix(s.cfg.Clone.HTTP, "/"); base != "" {
		return base
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// favicon serves the configured icon, or the built-in one.
func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("X-Content-Type-Options", "nosniff")
	// An SVG can carry scripts; never let one run on this origin.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	if s.cfg.Favicon == "" {
		h.Set("Content-Type", "image/svg+xml")
		icon, _ := assets.ReadFile("static/favicon.svg")
		w.Write(icon)
		return
	}
	switch strings.ToLower(filepath.Ext(s.cfg.Favicon)) {
	case ".svg":
		h.Set("Content-Type", "image/svg+xml")
	case ".png":
		h.Set("Content-Type", "image/png")
	default:
		h.Set("Content-Type", "image/x-icon")
	}
	http.ServeFile(w, r, s.cfg.Favicon)
}

// about renders the markdown file set as "about" in the config. It is read
// on every request, so edits show up without a restart.
func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	src, err := os.ReadFile(s.cfg.About)
	if err != nil || len(src) > config.MaxFileSize {
		log.Printf("about page: %v", err)
		s.render(w, r, http.StatusInternalServerError, "error", map[string]any{
			"Title": "Error", "Message": "The about page can't be read.",
		})
		return
	}
	// Relative links point into the site: [project](project) → /project.
	s.render(w, r, http.StatusOK, "about", map[string]any{
		"Title": "about", "Tab": "about", "About": render.Markdown(src, render.Links{}),
	})
}
