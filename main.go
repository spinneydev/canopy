// Command canopy is a read-only web interface for git repositories.
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"spinney.dev/canopy/internal/config"
	"spinney.dev/canopy/internal/git"
	"spinney.dev/canopy/internal/server"
	"spinney.dev/canopy/internal/sigcheck"
)

// defaultConfig is read when -config isn't given and the file exists.
const defaultConfig = "config/canopy.toml"

func main() {
	configPath := flag.String("config", "", "path to the config file (default: "+defaultConfig+" if present)")
	// These override the config file when given.
	listen := flag.String("listen", "", "address to listen on (default :8080)")
	root := flag.String("root", "", "directory containing git repositories (default .)")
	title := flag.String("title", "", "site name shown in the top bar")
	cloneHTTP := flag.String("clone-http", "", "base URL for clone URLs")
	depth := flag.Int("depth", 0, "how many directories deep to look for repositories (default 3)")
	gitBin := flag.String("git", "", "path to the git binary")
	check := flag.Bool("check", false, "validate the config, print the resulting settings and exit")
	mirror := flag.Bool("mirror", false, "clone or update the repositories under [mirrors] in the config, then exit")
	flag.Parse()

	path := *configPath
	if path == "" {
		if _, err := os.Stat(defaultConfig); err == nil {
			path = defaultConfig
		}
	}
	cfg := config.Default()
	if path != "" {
		var err error
		if cfg, err = config.Load(path); err != nil {
			log.Fatal(err)
		}
	}
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "listen":
			cfg.Listen = *listen
		case "root":
			cfg.Root = *root
		case "title":
			cfg.Title = *title
		case "clone-http":
			cfg.Clone.HTTP = *cloneHTTP
		case "depth":
			cfg.Depth = *depth
		case "git":
			cfg.Git = *gitBin
		}
	})

	if *check {
		printConfig(path, cfg)
		return
	}
	if *mirror {
		os.Exit(syncMirrors(cfg))
	}

	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	if path != "" {
		log.Printf("loaded config from %s", path)
	}
	log.Printf("serving %s on %s", cfg.Root, cfg.Listen)
	log.Fatal(hs.ListenAndServe())
}

// syncMirrors clones the [mirrors] that don't exist yet and fetches the
// rest. One failure doesn't stop the others; the exit status is 1 if any
// failed.
func syncMirrors(cfg *config.Config) int {
	git.Binary = cfg.Git
	status := 0
	for _, name := range slices.Sorted(maps.Keys(cfg.Mirrors)) {
		repo := &git.Repo{Dir: filepath.Join(cfg.Root, filepath.FromSlash(name)+".git")}
		start := time.Now()
		created, changed, err := repo.Mirror(context.Background(), cfg.Mirrors[name])
		took := time.Since(start).Round(time.Second)
		switch {
		case err != nil:
			log.Printf("%s: %v", name, err)
			status = 1
		case created:
			log.Printf("%s: cloned in %s", name, took)
		case changed:
			log.Printf("%s: updated in %s", name, took)
		default:
			log.Printf("%s: up to date", name)
		}
		// A failed first clone leaves no repository to record it in.
		if !(created && err != nil) {
			if err := repo.RecordSync(context.Background(), err); err != nil {
				log.Printf("%s: recording the result: %v", name, err)
			}
		}
	}
	return status
}

// printConfig reports the settings canopy would run with, one "key: value"
// per line so scripts (like install.sh) can read them.
func printConfig(path string, cfg *config.Config) {
	if path == "" {
		path = "(no config file, defaults)"
	}
	root, _ := filepath.Abs(cfg.Root)
	fmt.Printf("config: %s\n", path)
	fmt.Printf("listen: %s\n", cfg.Listen)
	fmt.Printf("root: %s\n", root)
	fmt.Printf("depth: %d\n", cfg.Depth)
	fmt.Printf("title: %s\n", cfg.Title)
	fmt.Printf("header: %s\n", cmp.Or(cfg.Header, cfg.Title))
	fmt.Printf("clone.http: %s\n", cfg.Clone.HTTP)
	fmt.Printf("favicon: %s\n", absOrEmpty(cfg.Favicon))
	fmt.Printf("about: %s\n", absOrEmpty(cfg.About))
	fmt.Printf("owner: %s\n", cfg.Owner)
	fmt.Printf("owner_url: %s\n", cfg.OwnerURL)
	fmt.Printf("tree_commits: %v\n", cfg.TreeCommits)
	fmt.Printf("activity: %v\n", cfg.Activity.Enabled)
	fmt.Printf("activity.emails: %s\n", strings.Join(cfg.Activity.Emails, " "))
	keys, _ := sigcheck.NewKeys(cfg.Signatures.Keys) // checked by Load
	fmt.Printf("signatures: %d\n", keys.Len())
	for _, k := range keys.List() {
		fmt.Printf("signature key %s\n", k)
	}
	fmt.Printf("theme.sans: %s\n", cfg.Theme.Sans)
	fmt.Printf("theme.mono: %s\n", cfg.Theme.Mono)
	fmt.Printf("theme.light: %s\n", cmp.Or(cfg.Theme.Light, "default"))
	fmt.Printf("theme.dark: %s\n", cmp.Or(cfg.Theme.Dark, "default"))
	for _, name := range slices.Sorted(maps.Keys(cfg.Themes)) {
		def := cfg.Themes[name]
		var keys []string
		for _, k := range slices.Sorted(maps.Keys(def)) {
			keys = append(keys, k+"="+def[k])
		}
		fmt.Printf("themes %s: %s\n", name, strings.Join(keys, " "))
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Repos)) {
		r := cfg.Repos[name]
		fmt.Printf("repo %s: hidden=%v homepage=%q description=%q owner=%q\n", name, r.Hidden, r.Homepage, r.Description, r.Owner)
	}
	fmt.Printf("mirrors: %d\n", len(cfg.Mirrors))
	for _, name := range slices.Sorted(maps.Keys(cfg.Mirrors)) {
		fmt.Printf("mirror %s: %s\n", name, cfg.Mirrors[name])
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "warning: root %s is not a directory (yet)\n", root)
	}
}

func absOrEmpty(p string) string {
	if p == "" {
		return ""
	}
	abs, _ := filepath.Abs(p)
	return abs
}
