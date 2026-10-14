package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Mirror makes the repository a copy of the branches and tags at url,
// creating it first when it doesn't exist. A fetch only transfers anything
// when the copy is behind. It reports whether the repository was created
// and whether any branch or tag changed.
func (r *Repo) Mirror(ctx context.Context, url string) (created, changed bool, err error) {
	// A first clone that fails leaves nothing behind, rather than an empty
	// repository on the index.
	defer func() {
		if created && err != nil {
			os.RemoveAll(r.Dir)
		}
	}()
	if !IsRepo(r.Dir) {
		if err := os.MkdirAll(filepath.Dir(r.Dir), 0o755); err != nil {
			return false, false, err
		}
		cmd := exec.CommandContext(ctx, Binary, "init", "-q", "--bare", r.Dir)
		cmd.Env = Env()
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, false, fmt.Errorf("git init: %w: %s", err, bytes.TrimSpace(out))
		}
		created = true
	}
	// Set on every run, so a URL changed in the config takes effect. Only
	// branches and tags are copied, not a host's extras like refs/pull/*.
	for _, args := range [][]string{
		{"config", "remote.origin.url", url},
		{"config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"},
		{"config", "--add", "remote.origin.fetch", "+refs/tags/*:refs/tags/*"},
	} {
		if _, err := r.run(ctx, args...); err != nil {
			return created, false, err
		}
	}
	before, err := r.run(ctx, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return created, false, err
	}
	if _, err := r.run(ctx, "fetch", "--prune", "--quiet", "origin"); err != nil {
		return created, false, err
	}
	after, err := r.run(ctx, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return created, false, err
	}
	// HEAD follows the upstream's default branch, so the summary page
	// shows the same branch as the original.
	if out, err := r.run(ctx, "ls-remote", "--symref", "origin", "HEAD"); err == nil {
		line, _, _ := strings.Cut(string(out), "\n")
		if ref, _, ok := strings.Cut(strings.TrimPrefix(line, "ref: "), "\t"); ok && strings.HasPrefix(ref, "refs/heads/") {
			if _, err := r.run(ctx, "symbolic-ref", "HEAD", ref); err != nil {
				return created, false, err
			}
		}
	}
	return created, !bytes.Equal(before, after), nil
}

// RecordSync notes the result of a Mirror call in the repository's config
// for the summary page: canopy.synced is the time of the last successful
// update, canopy.syncerror the last failure, cleared by the next success.
func (r *Repo) RecordSync(ctx context.Context, syncErr error) error {
	if syncErr != nil {
		msg := strings.Join(strings.Fields(syncErr.Error()), " ")
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		_, err := r.run(ctx, "config", "canopy.syncerror", msg)
		return err
	}
	if _, err := r.run(ctx, "config", "canopy.synced", strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return err
	}
	r.run(ctx, "config", "--unset", "canopy.syncerror") // fails when there is none
	return nil
}

// SyncStatus reads what RecordSync wrote. Both are zero for a repository
// that isn't a mirror or hasn't been updated yet.
func (r *Repo) SyncStatus(ctx context.Context) (synced time.Time, failure string) {
	out, err := r.run(ctx, "config", "--get-regexp", `^canopy\.sync`)
	if err != nil {
		return time.Time{}, ""
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		key, value, _ := strings.Cut(line, " ")
		switch key {
		case "canopy.synced":
			synced = unix(value)
		case "canopy.syncerror":
			failure = value
		}
	}
	return synced, failure
}
