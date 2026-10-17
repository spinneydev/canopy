// Package ghkeys follows the SSH signing keys people keep on GitHub
// (Settings → SSH and GPG keys, as a "Signing Key"), so a key added or
// removed there counts here too, without editing the config. The keys are
// cached, so a restart while GitHub can't be reached keeps the last ones.
package ghkeys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// ValidUser matches a GitHub user name.
var ValidUser = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`).MatchString

// API is GitHub's REST API; tests point it elsewhere.
var API = "https://api.github.com"

var client = &http.Client{Timeout: 30 * time.Second}

// maxBody bounds GitHub's reply: 100 keys are a few tens of KiB.
const maxBody = 1 << 20

// ErrNoUser means GitHub has no such user (any more).
var ErrNoUser = errors.New("no such GitHub user")

// Signing returns the SSH signing keys user has on GitHub, as public key
// lines ("ssh-ed25519 AAAA…"). No key is an empty list, not an error.
func Signing(ctx context.Context, user string) ([]string, error) {
	if !ValidUser(user) {
		return nil, fmt.Errorf("%q isn't a GitHub user name", user)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+"/users/"+user+"/ssh_signing_keys?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "canopy")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrNoUser
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub: HTTP %d", resp.StatusCode)
	case len(body) > maxBody:
		return nil, fmt.Errorf("GitHub: more than %d bytes", maxBody)
	}
	var list []struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("GitHub: %v", err)
	}
	keys := []string{}
	for _, k := range list {
		if k.Key = strings.TrimSpace(k.Key); k.Key != "" && !strings.ContainsAny(k.Key, "\r\n") {
			keys = append(keys, k.Key)
		}
	}
	return keys, nil
}

// Signers follows the signing keys of some GitHub users, each under the
// name a badge shows.
type Signers struct {
	users    map[string]string // name → GitHub user
	cacheDir string            // "" keeps nothing on disk
}

func NewSigners(cacheDir string, users map[string]string) *Signers {
	return &Signers{users: users, cacheDir: cacheDir}
}

// Run hands update the cached keys, by name, then fetches them from GitHub
// now and every interval, handing update the keys again whenever they
// change. A user whose keys can't be fetched keeps the last ones known; one
// GitHub no longer has loses them. It returns when ctx ends.
func (s *Signers) Run(ctx context.Context, every time.Duration, update func(map[string][]string)) {
	keys := map[string][]string{}
	for name, user := range s.users {
		if cached, err := s.readCache(user); err == nil {
			keys[name] = cached
		}
	}
	if len(keys) > 0 {
		update(maps.Clone(keys))
	}
	for {
		changed := false
		for _, name := range slices.Sorted(maps.Keys(s.users)) {
			user := s.users[name]
			fetched, err := Signing(ctx, user)
			if errors.Is(err, ErrNoUser) {
				log.Printf("signatures: github.com/%s: no such user; trusting none of its keys", user)
				fetched = []string{}
			} else if err != nil {
				log.Printf("signatures: github.com/%s: %v; keeping the keys known before", user, err)
				continue
			}
			if old, ok := keys[name]; !ok || !slices.Equal(old, fetched) {
				log.Printf("signatures: github.com/%s: %d signing key(s) for %s", user, len(fetched), name)
				keys[name] = fetched
				changed = true
			}
			if err := s.writeCache(user, fetched); err != nil {
				log.Printf("signatures: github.com/%s: not cached: %v", user, err)
			}
		}
		if changed {
			update(maps.Clone(keys))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (s *Signers) cachePath(user string) string {
	return filepath.Join(s.cacheDir, strings.ToLower(user)+".keys")
}

func (s *Signers) readCache(user string) ([]string, error) {
	if s.cacheDir == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(s.cachePath(user))
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for line := range strings.Lines(string(b)) {
		if line = strings.TrimSpace(line); line != "" {
			keys = append(keys, line)
		}
	}
	return keys, nil
}

func (s *Signers) writeCache(user string, keys []string) error {
	if s.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return err
	}
	path := s.cachePath(user)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CacheDir is where the keys are kept: systemd's CacheDirectory= if the
// service has one, else the user's cache folder. "" if neither is known.
func CacheDir() string {
	if dirs := filepath.SplitList(os.Getenv("CACHE_DIRECTORY")); len(dirs) > 0 && dirs[0] != "" {
		return filepath.Join(dirs[0], "github")
	}
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "canopy", "github")
	}
	return ""
}
