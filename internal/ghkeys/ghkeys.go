// Package ghkeys follows the SSH signing keys people keep on GitHub
// (Settings → SSH and GPG keys, as a "Signing Key"), so a key added or
// removed there counts here too, without editing the config. The keys are
// cached, so a restart while GitHub can't be reached keeps the last ones.
//
// A GitHub name can change hands: renaming or deleting an account frees its
// name for anyone to register. An account's ID never changes, so the first
// time a name is looked up, the ID of the account it names is kept (in the
// cache too), and from then on that account is followed, whatever it is
// called by then. Someone who takes over a freed name gets nothing.
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
	"strconv"
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

// ErrNoUser means GitHub has no such user, or no such account (any more).
var ErrNoUser = errors.New("no such GitHub user")

// Account is a GitHub account: its ID, which never changes, and its name,
// which can.
type Account struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// Lookup returns the account called name now.
func Lookup(ctx context.Context, name string) (Account, error) {
	if !ValidUser(name) {
		return Account{}, fmt.Errorf("%q isn't a GitHub user name", name)
	}
	return account(ctx, "/users/"+name)
}

// ByID returns the account with this ID, under its name now; ErrNoUser when
// it is gone.
func ByID(ctx context.Context, id int64) (Account, error) {
	return account(ctx, "/user/"+strconv.FormatInt(id, 10))
}

func account(ctx context.Context, path string) (Account, error) {
	body, err := get(ctx, path)
	if err != nil {
		return Account{}, err
	}
	var a Account
	if err := json.Unmarshal(body, &a); err != nil {
		return Account{}, fmt.Errorf("GitHub: %v", err)
	}
	if a.ID <= 0 || !ValidUser(a.Login) {
		return Account{}, errors.New("GitHub: no account in the reply")
	}
	return a, nil
}

// Signing returns the SSH signing keys user has on GitHub, as public key
// lines ("ssh-ed25519 AAAA…"). No key is an empty list, not an error.
func Signing(ctx context.Context, user string) ([]string, error) {
	if !ValidUser(user) {
		return nil, fmt.Errorf("%q isn't a GitHub user name", user)
	}
	body, err := get(ctx, "/users/"+user+"/ssh_signing_keys?per_page=100")
	if err != nil {
		return nil, err
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

// get fetches path from GitHub's API; a 404 is ErrNoUser.
func get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, API+path, nil)
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
	return body, nil
}

// Signers follows the signing keys of some GitHub accounts, each under the
// name a badge shows.
type Signers struct {
	users    map[string]string // name → GitHub user
	cacheDir string            // "" keeps nothing on disk
	ids      map[string]int64  // GitHub user, in lower case → the account it named first
	logins   map[string]string // GitHub user → the account's name when last looked up
}

func NewSigners(cacheDir string, users map[string]string) *Signers {
	return &Signers{users: users, cacheDir: cacheDir, ids: map[string]int64{}, logins: map[string]string{}}
}

// Run hands update the cached keys, by name, then fetches them from GitHub
// now and every interval, handing update the keys again whenever they
// change. A user whose keys can't be fetched keeps the last ones known; an
// account that is gone loses them. It returns when ctx ends.
func (s *Signers) Run(ctx context.Context, every time.Duration, update func(map[string][]string)) {
	keys := map[string][]string{}
	for name, user := range s.users {
		if cached, err := s.readCache(user); err == nil {
			keys[name] = cached
		}
		if id, err := s.readID(user); err == nil {
			s.ids[strings.ToLower(user)] = id
		}
	}
	if len(keys) > 0 {
		update(maps.Clone(keys))
	}
	for {
		changed := false
		for _, name := range slices.Sorted(maps.Keys(s.users)) {
			user := s.users[name]
			fetched, err := s.fetch(ctx, user)
			if err != nil {
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

// fetch returns the signing keys of the account user named when it was
// first looked up, under the name that account has now. An account that is
// gone, or a name no account has, has none.
func (s *Signers) fetch(ctx context.Context, user string) ([]string, error) {
	key := strings.ToLower(user)
	id, known := s.ids[key]
	var a Account
	var err error
	if known {
		a, err = ByID(ctx, id)
	} else {
		a, err = Lookup(ctx, user)
	}
	switch {
	case errors.Is(err, ErrNoUser) && known:
		log.Printf("signatures: github.com/%s: its account (ID %d) is gone; trusting none of its keys", user, id)
		return []string{}, nil
	case errors.Is(err, ErrNoUser):
		log.Printf("signatures: github.com/%s: no such user; trusting none of its keys", user)
		return []string{}, nil
	case err != nil:
		return nil, err
	}
	if !known {
		s.ids[key] = a.ID
		if err := s.writeID(user, a.ID); err != nil {
			log.Printf("signatures: github.com/%s: account ID not cached: %v", user, err)
		}
	}
	if !strings.EqualFold(a.Login, user) && s.logins[key] != a.Login {
		log.Printf("signatures: github.com/%s is now github.com/%s (the same account, ID %d)", user, a.Login, a.ID)
	}
	s.logins[key] = a.Login
	keys, err := Signing(ctx, a.Login)
	if errors.Is(err, ErrNoUser) {
		// Renamed again since a moment ago: next time.
		return nil, fmt.Errorf("github.com/%s: %w", a.Login, err)
	}
	return keys, err
}

func (s *Signers) cachePath(user, ext string) string {
	return filepath.Join(s.cacheDir, strings.ToLower(user)+ext)
}

func (s *Signers) readCache(user string) ([]string, error) {
	if s.cacheDir == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(s.cachePath(user, ".keys"))
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
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return s.write(user, ".keys", b.String())
}

// readID returns the account user named when it was first looked up.
func (s *Signers) readID(user string) (int64, error) {
	if s.cacheDir == "" {
		return 0, os.ErrNotExist
	}
	b, err := os.ReadFile(s.cachePath(user, ".id"))
	if err != nil {
		return 0, err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err == nil && id <= 0 {
		err = fmt.Errorf("%d isn't an account ID", id)
	}
	return id, err
}

func (s *Signers) writeID(user string, id int64) error {
	return s.write(user, ".id", strconv.FormatInt(id, 10)+"\n")
}

// write replaces a cache file in one step.
func (s *Signers) write(user, ext, content string) error {
	if s.cacheDir == "" {
		return nil
	}
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return err
	}
	path := s.cachePath(user, ext)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
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
