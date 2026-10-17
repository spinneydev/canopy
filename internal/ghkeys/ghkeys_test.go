package ghkeys

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	keyA = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGb9ECWmEzf6FQbrBZ9w7lshQhqowtrbLDFw4rXAxZuE"
	keyB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	keyC = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJ1+/0hREQvXmbJC2Y1BpG7tIYDTxuPl6Ft5hpWpp9cH"
)

// fakeGitHub is GitHub's API for the tests: accounts by ID, each with a name
// and its signing keys (the reply, or "500" for an error).
type fakeGitHub struct {
	mu       sync.Mutex
	accounts map[int64]*fakeAccount
	down     bool
}

type fakeAccount struct{ login, keys string }

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{accounts: map[int64]*fakeAccount{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	old := API
	API = srv.URL
	t.Cleanup(func() { API = old })
	return f
}

// set changes GitHub: an account with that name and keys, or none (login "").
func (f *fakeGitHub) set(id int64, login, keys string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if login == "" {
		delete(f.accounts, id)
	} else {
		f.accounts[id] = &fakeAccount{login, keys}
	}
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	named := func(name string) (int64, *fakeAccount) {
		for id, a := range f.accounts {
			if strings.EqualFold(a.login, name) {
				return id, a
			}
		}
		return 0, nil
	}
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, "/user/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "/user/"), 10, 64)
		if a := f.accounts[id]; a != nil {
			fmt.Fprintf(w, `{"login": %q, "id": %d, "node_id": "x"}`, a.login, id)
			return
		}
	case strings.HasSuffix(path, "/ssh_signing_keys"):
		if _, a := named(strings.TrimSuffix(strings.TrimPrefix(path, "/users/"), "/ssh_signing_keys")); a != nil {
			if a.keys == "500" {
				http.Error(w, "down", http.StatusInternalServerError)
				return
			}
			w.Write([]byte(a.keys))
			return
		}
	case strings.HasPrefix(path, "/users/"):
		if id, a := named(strings.TrimPrefix(path, "/users/")); a != nil {
			fmt.Fprintf(w, `{"login": %q, "id": %d, "node_id": "x"}`, a.login, id)
			return
		}
	}
	http.NotFound(w, r)
}

func TestSigning(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.set(1, "alice", `[{"id":1,"key":"`+keyA+`","title":"laptop"},{"id":2,"key":"`+keyB+`"}]`)
	gh.set(2, "bob", `[]`)
	gh.set(3, "carol", "500")
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

func TestAccounts(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.set(7, "Alice", `[]`)
	ctx := context.Background()
	if a, err := Lookup(ctx, "alice"); err != nil || a != (Account{7, "Alice"}) {
		t.Errorf("Lookup(alice) = %+v, %v", a, err)
	}
	if a, err := ByID(ctx, 7); err != nil || a != (Account{7, "Alice"}) {
		t.Errorf("ByID(7) = %+v, %v", a, err)
	}
	if _, err := Lookup(ctx, "nobody"); !errors.Is(err, ErrNoUser) {
		t.Errorf("Lookup(nobody): got %v, want ErrNoUser", err)
	}
	if _, err := ByID(ctx, 8); !errors.Is(err, ErrNoUser) {
		t.Errorf("ByID(8): got %v, want ErrNoUser", err)
	}
	if _, err := Lookup(ctx, "../x"); err == nil {
		t.Error("want an error for a name that isn't a GitHub user")
	}
}

// TestRunFollowsTheAccount: the keys come from the account the name first
// belonged to, through a rename and someone else taking the old name, and
// across a restart; an account that is gone loses them.
func TestRunFollowsTheAccount(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.set(7, "alice", `[{"key":"`+keyA+`"}]`)
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
	// Renamed, with a key added, and someone else registers "alice".
	gh.mu.Lock()
	gh.accounts[7] = &fakeAccount{"alice-new", `[{"key":"` + keyA + `"},{"key":"` + keyB + `"}]`}
	gh.accounts[8] = &fakeAccount{"alice", `[{"key":"` + keyC + `"}]`}
	gh.mu.Unlock()
	if k := next(); !slices.Equal(k["Alice"], []string{keyA, keyB}) {
		t.Fatalf("after the rename: got %q, want the renamed account's keys", k)
	}
	cancel()

	// With GitHub down, a new start keeps the cached keys and reports no
	// change.
	gh.mu.Lock()
	gh.down = true
	gh.mu.Unlock()
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

	// The account goes. The new start still knew which one it was, so the
	// newcomer's keys don't count either.
	gh.mu.Lock()
	gh.down = false
	delete(gh.accounts, 7)
	gh.mu.Unlock()
	if k := next(); k["Alice"] == nil || len(k["Alice"]) != 0 {
		t.Fatalf("after the account went: got %q, want no keys", k)
	}
}
