package ghkeys

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

const (
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
)

// fakeGitHub answers /users/NAME/ssh_signing_keys from replies, by user;
// a user it doesn't know is a 404.
func fakeGitHub(t *testing.T, mu *sync.Mutex, replies map[string]string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for user, body := range replies {
			if r.URL.Path == "/users/"+user+"/ssh_signing_keys" {
				if body == "500" {
					http.Error(w, "down", http.StatusInternalServerError)
					return
				}
				w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
}

func TestSigning(t *testing.T) {
	var mu sync.Mutex
	fakeGitHub(t, &mu, map[string]string{
		"alice": `[{"id":1,"key":"` + keyA + `","title":"laptop"},{"id":2,"key":"` + keyB + `"}]`,
		"bob":   `[]`,
		"carol": "500",
	})
	ctx := context.Background()
	if keys, err := Signing(ctx, "alice"); err != nil || !slices.Equal(keys, []string{keyA, keyB}) {
		t.Errorf("alice: got %q, %v", keys, err)
	}
	if keys, err := Signing(ctx, "bob"); err != nil || keys == nil || len(keys) != 0 {
		t.Errorf("bob: got %q, %v; want an empty list", keys, err)
	}
	if _, err := Signing(ctx, "carol"); err == nil {
		t.Error("carol: want an error for HTTP 500")
	}
	if _, err := Signing(ctx, "nobody"); !errors.Is(err, ErrNoUser) {
		t.Errorf("nobody: got %v, want ErrNoUser", err)
	}
	if _, err := Signing(ctx, "../x"); err == nil {
		t.Error("want an error for a name that isn't a GitHub user")
	}
}

func TestRunFollowsAndCaches(t *testing.T) {
	var mu sync.Mutex
	replies := map[string]string{"alice": `[{"key":"` + keyA + `"}]`}
	fakeGitHub(t, &mu, replies)
	dir := t.TempDir()
	users := map[string]string{"Alice": "alice"}

	updates := make(chan map[string][]string, 10)
	run := func(ctx context.Context) {
		NewSigners(dir, users).Run(ctx, 10*time.Millisecond, func(k map[string][]string) { updates <- k })
	}
	next := func() map[string][]string {
		t.Helper()
		select {
		case k := <-updates:
			return k
		case <-time.After(5 * time.Second):
			t.Fatal("no update")
			return nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	go run(ctx)
	if k := next(); !slices.Equal(k["Alice"], []string{keyA}) {
		t.Fatalf("first fetch: got %q", k)
	}
	// A key added on GitHub shows up at the next fetch.
	mu.Lock()
	replies["alice"] = `[{"key":"` + keyA + `"},{"key":"` + keyB + `"}]`
	mu.Unlock()
	if k := next(); !slices.Equal(k["Alice"], []string{keyA, keyB}) {
		t.Fatalf("after adding a key: got %q", k)
	}
	cancel()

	// With GitHub down, a new start keeps the cached keys and reports no
	// change.
	mu.Lock()
	replies["alice"] = "500"
	mu.Unlock()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	for len(updates) > 0 {
		<-updates
	}
	go run(ctx)
	if k := next(); !slices.Equal(k["Alice"], []string{keyA, keyB}) {
		t.Fatalf("from the cache: got %q", k)
	}
	select {
	case k := <-updates:
		t.Fatalf("a failed fetch changed the keys: %q", k)
	case <-time.After(100 * time.Millisecond):
	}

	// A user GitHub no longer has loses its keys.
	mu.Lock()
	delete(replies, "alice")
	mu.Unlock()
	if k := next(); len(k["Alice"]) != 0 {
		t.Fatalf("after the account went: got %q", k)
	}
}
