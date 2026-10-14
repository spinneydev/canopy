package server

import (
	"cmp"
	"context"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"spinney.dev/canopy/internal/config"
	"spinney.dev/canopy/internal/git"
)

type Repo struct {
	*git.Repo
	Name        string // URL name, e.g. "~alice/project"
	Description string
	Homepage    string
	Updated     time.Time
	Maintainer  string // the repo's owner setting, else its folder, else the site's owner
	Committer   string // who made the newest commit on any branch
	Upstream    string // for a mirror ([mirrors] in the config), where it is copied from
}

// URL builds an escaped link into the repository, skipping empty parts.
func (r *Repo) URL(parts ...string) string {
	var b strings.Builder
	b.WriteString(escapePath(r.Name))
	for _, p := range parts {
		if p != "" {
			b.WriteString("/" + escapePath(p))
		}
	}
	return "/" + b.String()
}

// ItemURL links to a path at a ref, e.g. /repo/tree/main/item/src/x.go,
// leaving out "/item" when the path is the repository root.
func (r *Repo) ItemURL(action, ref, p string) string {
	if p == "" {
		return r.URL(action, ref)
	}
	return r.URL(action, ref, "item", p)
}

// Owner and Base split "~alice/project" for the header.
func (r *Repo) Owner() string {
	if i := strings.LastIndex(r.Name, "/"); i >= 0 {
		return r.Name[:i+1]
	}
	return ""
}

func (r *Repo) Base() string { return r.Name[strings.LastIndex(r.Name, "/")+1:] }

func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// registry discovers repositories under a root directory and caches the
// result for a short while.
type registry struct {
	root      string
	maxDepth  int
	overrides map[string]config.RepoConfig
	owner     string // the site's owner, for repositories not in a folder
	mirrors   map[string]string
	ttl       time.Duration

	mu      sync.Mutex
	scanned time.Time
	byName  map[string]*Repo
	list    []*Repo
}

func (g *registry) get() (map[string]*Repo, []*Repo) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Since(g.scanned) > g.ttl {
		g.byName, g.list = g.scan()
		g.scanned = time.Now()
	}
	return g.byName, g.list
}

func (g *registry) scan() (map[string]*Repo, []*Repo) {
	byName := map[string]*Repo{}
	var list []*Repo
	root := filepath.Clean(g.root)
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if strings.Count(rel, string(filepath.Separator)) >= g.maxDepth {
			return fs.SkipDir
		}
		gitDir := ""
		switch {
		case rel != "." && strings.HasPrefix(d.Name(), "."):
			// Hidden folders are served only when they are bare
			// repositories (.dotfiles.git). Anything else, including a work
			// tree such as a version-controlled ~/.ssh, is neither served
			// nor searched.
			if !git.IsRepo(p) {
				return fs.SkipDir
			}
			gitDir = p
		case git.IsRepo(filepath.Join(p, ".git")):
			gitDir = filepath.Join(p, ".git")
		case git.IsRepo(p):
			gitDir = p
		default:
			return nil
		}
		name := filepath.ToSlash(strings.TrimSuffix(rel, ".git"))
		if rel == "." {
			name = strings.TrimSuffix(filepath.Base(root), ".git")
		}
		if name == "" || strings.HasSuffix(name, "/") {
			return fs.SkipDir // a folder named just ".git"
		}
		override := g.overrides[name]
		if override.Hidden {
			return fs.SkipDir
		}
		repo := &Repo{Repo: &git.Repo{Dir: gitDir}, Name: name, Homepage: override.Homepage, Upstream: g.mirrors[name]}
		// Private (set over SSH) is hidden without a config change: left out
		// here, it is neither listed nor served, like hidden.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if repo.Private(ctx) {
			return fs.SkipDir
		}
		if override.Description != "" {
			repo.Description = override.Description
		} else if desc, err := os.ReadFile(filepath.Join(gitDir, "description")); err == nil {
			s := strings.TrimSpace(string(desc))
			if !strings.HasPrefix(s, "Unnamed repository") {
				repo.Description = s
			}
		}
		repo.Updated, repo.Committer = repo.LastUpdated(ctx)
		// "torvalds/linux" belongs to torvalds; "lab/tools/x" to lab.
		folder, _, _ := strings.Cut(repo.Owner(), "/")
		repo.Maintainer = cmp.Or(override.Owner, folder, g.owner)
		byName[name] = repo
		list = append(list, repo)
		return fs.SkipDir
	})
	sort.Slice(list, func(i, j int) bool { return list[i].Updated.After(list[j].Updated) })
	return byName, list
}
