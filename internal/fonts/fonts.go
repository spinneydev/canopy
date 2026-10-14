// Package fonts fetches Google Fonts families and serves them from canopy
// itself, so visitors never contact Google. Each family is downloaded once
// into a cache folder; later starts read it from there.
package fonts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Set is the fonts of a site: one stylesheet of @font-face rules and the
// woff2 files it points to. It is empty until Fetch has loaded something.
type Set struct {
	families []string
	cacheDir string // "" keeps fonts in memory only

	mu    sync.RWMutex
	css   map[string][]byte // family → its @font-face rules
	files map[string][]byte // file name → woff2
}

// New returns an empty set for families (empty names are skipped), cached
// under cacheDir.
func New(cacheDir string, families ...string) *Set {
	s := &Set{cacheDir: cacheDir, css: map[string][]byte{}, files: map[string][]byte{}}
	for _, f := range families {
		if f != "" && !slices.Contains(s.families, f) {
			s.families = append(s.families, f)
		}
	}
	return s
}

// Empty reports whether the set has no families to load.
func (s *Set) Empty() bool { return len(s.families) == 0 }

// Fetch loads every family, from the cache or else from Google, retrying the
// ones that failed with a growing pause until all are loaded or ctx ends.
// Pages work meanwhile, in the fallback fonts.
func (s *Set) Fetch(ctx context.Context) {
	pending := s.families
	for wait := 30 * time.Second; ; wait = min(2*wait, time.Hour) {
		var failed []string
		for _, family := range pending {
			css, files, err := s.load(ctx, family)
			if err != nil {
				log.Printf("font %q: %v", family, err)
				// A wrong name stays wrong; a network error may pass.
				if !errors.Is(err, errUnknown) {
					failed = append(failed, family)
				}
				continue
			}
			s.mu.Lock()
			s.css[family] = css
			for name, b := range files {
				s.files[name] = b
			}
			s.mu.Unlock()
		}
		if pending = failed; len(pending) == 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// CSS is the @font-face rules of the families loaded so far.
func (s *Set) CSS() []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var buf bytes.Buffer
	for _, family := range s.families {
		buf.Write(s.css[family])
	}
	return buf.Bytes()
}

// File returns the woff2 file of that name, if loaded.
func (s *Set) File(name string) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.files[name]
	return b, ok
}

// Path is where the stylesheet's fonts are served; File takes what follows.
const Path = "/_static/fonts/"

// Limits on what Google may send.
const (
	maxCSS   = 256 << 10
	maxFont  = 4 << 20
	maxFaces = 100
)

// The browser Google is told it talks to: one recent enough to get woff2
// files split by script (unicode-range), so a page loads only what it needs.
const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

// styles are the variants asked for, best first. Google refuses a request
// naming a style the family lacks, so each is tried until one is accepted:
// all weights of a variable font, then the usual static ones.
var styles = []string{
	":ital,wght@0,400..700;1,400..700",
	":ital,wght@0,400;0,700;1,400;1,700",
	":wght@400;700",
	"",
}

var client = &http.Client{Timeout: 30 * time.Second}

var errUnknown = errors.New("not on Google Fonts (see fonts.google.com for the exact name); using the fallback fonts")

// load returns a family's rules and files, from the cache if it holds them
// all, else downloaded (and then cached).
func (s *Set) load(ctx context.Context, family string) ([]byte, map[string][]byte, error) {
	cssPath := filepath.Join(s.cacheDir, slug(family)+".css")
	if s.cacheDir != "" {
		if css, files, err := readCache(s.cacheDir, cssPath); err == nil {
			return css, files, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			log.Printf("font %q: cache: %v; downloading it again", family, err)
		}
	}
	css, files, err := download(ctx, family)
	if err != nil {
		return nil, nil, err
	}
	if s.cacheDir != "" {
		if err := writeCache(s.cacheDir, cssPath, css, files); err != nil {
			log.Printf("font %q: not cached: %v", family, err)
		}
	}
	log.Printf("font %q: downloaded %d files", family, len(files))
	return css, files, nil
}

func download(ctx context.Context, family string) ([]byte, map[string][]byte, error) {
	var src []byte
	for _, style := range styles {
		u := "https://fonts.googleapis.com/css2?family=" + url.QueryEscape(family) + style + "&display=swap"
		b, status, err := get(ctx, u, maxCSS)
		if err != nil {
			return nil, nil, err
		}
		if status == http.StatusOK {
			src = b
			break
		}
		if status != http.StatusBadRequest {
			return nil, nil, fmt.Errorf("fonts.googleapis.com: HTTP %d", status)
		}
	}
	if src == nil {
		return nil, nil, errUnknown
	}
	faces, err := parse(src)
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	byURL := map[string]string{}
	var css bytes.Buffer
	for _, f := range faces {
		name, ok := byURL[f.url]
		if !ok {
			b, status, err := get(ctx, f.url, maxFont)
			if err != nil {
				return nil, nil, err
			}
			if status != http.StatusOK {
				return nil, nil, fmt.Errorf("%s: HTTP %d", f.url, status)
			}
			name = fileName(b)
			files[name] = b
			byURL[f.url] = name
		}
		f.write(&css, family, name)
	}
	return css.Bytes(), files, nil
}

func get(ctx context.Context, u string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(b)) > limit {
		return nil, 0, fmt.Errorf("%s: more than %d bytes", u, limit)
	}
	return b, resp.StatusCode, nil
}

// face is one @font-face rule from Google, reduced to the parts canopy
// writes back out. Nothing else of Google's stylesheet reaches a page.
type face struct {
	style, weight, unicodeRange, url string
}

var (
	faceRule = regexp.MustCompile(`@font-face\s*\{([^{}]*)\}`)
	srcURL   = regexp.MustCompile(`url\((https://fonts\.gstatic\.com/[A-Za-z0-9/._-]+\.woff2)\)`)
	// Values of font-style, font-weight and unicode-range.
	plainValue = regexp.MustCompile(`^[A-Za-z0-9 ,+?-]*$`)
)

func parse(src []byte) ([]face, error) {
	rules := faceRule.FindAllSubmatch(src, -1)
	if len(rules) == 0 {
		return nil, fmt.Errorf("no woff2 fonts in Google's reply")
	}
	if len(rules) > maxFaces {
		return nil, fmt.Errorf("%d font files, more than %d", len(rules), maxFaces)
	}
	var faces []face
	for _, rule := range rules {
		var f face
		for _, decl := range strings.Split(string(rule[1]), ";") {
			key, value, _ := strings.Cut(decl, ":")
			key, value = strings.TrimSpace(key), strings.TrimSpace(value)
			switch key {
			case "font-style":
				f.style = value
			case "font-weight":
				f.weight = value
			case "unicode-range":
				f.unicodeRange = value
			case "src":
				if m := srcURL.FindStringSubmatch(value); m != nil {
					f.url = m[1]
				}
			}
		}
		if f.url == "" {
			return nil, fmt.Errorf("a font in Google's reply isn't a woff2 file on fonts.gstatic.com")
		}
		for _, v := range []string{f.style, f.weight, f.unicodeRange} {
			if !plainValue.MatchString(v) {
				return nil, fmt.Errorf("unexpected value %q in Google's reply", v)
			}
		}
		faces = append(faces, f)
	}
	return faces, nil
}

func (f face) write(w io.Writer, family, file string) {
	fmt.Fprintf(w, "@font-face {\n  font-family: %q;\n", family)
	if f.style != "" {
		fmt.Fprintf(w, "  font-style: %s;\n", f.style)
	}
	if f.weight != "" {
		fmt.Fprintf(w, "  font-weight: %s;\n", f.weight)
	}
	fmt.Fprintf(w, "  font-display: swap;\n  src: url(%s%s) format(\"woff2\");\n", Path, file)
	if f.unicodeRange != "" {
		fmt.Fprintf(w, "  unicode-range: %s;\n", f.unicodeRange)
	}
	fmt.Fprintf(w, "}\n")
}

// fileName names a font by its content, so its URL can be cached forever.
func fileName(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16]) + ".woff2"
}

// ValidName reports whether name could be a file of this package.
var ValidName = regexp.MustCompile(`^[0-9a-f]{32}\.woff2$`).MatchString

// fileRef finds the font files a cached stylesheet uses.
var fileRef = regexp.MustCompile(regexp.QuoteMeta(Path) + `([0-9a-f]{32}\.woff2)`)

func readCache(dir, cssPath string) ([]byte, map[string][]byte, error) {
	css, err := os.ReadFile(cssPath)
	if err != nil {
		return nil, nil, err
	}
	files := map[string][]byte{}
	for _, m := range fileRef.FindAllSubmatch(css, -1) {
		name := string(m[1])
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, nil, err
		}
		if fileName(b) != name {
			return nil, nil, fmt.Errorf("%s is damaged", name)
		}
		files[name] = b
	}
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("%s names no fonts", cssPath)
	}
	return css, files, nil
}

// writeCache stores the files first and the stylesheet last, so a
// stylesheet in the cache always has its files.
func writeCache(dir, cssPath string, css []byte, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, b := range files {
		if err := writeFile(filepath.Join(dir, name), b); err != nil {
			return err
		}
	}
	return writeFile(cssPath, css)
}

func writeFile(path string, b []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// slug turns a family name into a file name: "JetBrains Mono" → "jetbrains-mono".
func slug(family string) string {
	return strings.ToLower(strings.ReplaceAll(family, " ", "-"))
}

// CacheDir is where fonts are kept: systemd's CacheDirectory= if the service
// has one, else the user's cache folder. "" if neither is known.
func CacheDir() string {
	if dirs := filepath.SplitList(os.Getenv("CACHE_DIRECTORY")); len(dirs) > 0 && dirs[0] != "" {
		return filepath.Join(dirs[0], "fonts")
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "canopy", "fonts")
	}
	return ""
}
