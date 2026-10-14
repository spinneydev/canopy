// Command demo builds a set of demo repositories in demo/repos for trying
// out canopy. Run it from the canopy folder, on any system with git:
//
//	go run ./demo
//	go run . -config demo/canopy.toml
//
// Safe to re-run; demo/repos is recreated each time.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// demo builds repositories in a scratch folder and publishes them to out.
type demo struct {
	project string            // the canopy folder
	out     string            // demo/repos
	work    string            // scratch folder holding the work trees
	dir     string            // the current work tree
	env     []string          // identity and date for the next commit or tag
	exec    []string          // files to mark executable in the next commit
	keys    map[string]string // who signs their commits and tags → private key file
}

func main() {
	project, err := os.Getwd()
	check(err)
	if _, err := os.Stat(filepath.Join(project, "demo", "canopy.toml")); err != nil {
		fail("run this from the canopy folder: go run ./demo")
	}
	if _, err := exec.LookPath("git"); err != nil {
		fail("git not found")
	}
	work, err := os.MkdirTemp("", "canopy-demo-")
	check(err)
	defer os.RemoveAll(work)

	d := &demo{project: project, out: filepath.Join(project, "demo", "repos"), work: work}
	check(os.RemoveAll(d.out))
	check(os.MkdirAll(d.out, 0o755))
	fmt.Println("creating demo repositories in", d.out)
	d.signingKeys()

	d.tinyhttpd()
	d.dotfiles()
	d.notes()
	d.raytracer()
	d.canopy()
	d.scratchAndSecret()

	fmt.Println("done. run: go run . -config demo/canopy.toml")
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "demo:", msg)
	os.Exit(1)
}

// md turns ´ into backticks, which Go's raw strings can't hold.
func md(s string) string { return strings.ReplaceAll(s, "´", "`") }

// git runs git in the current work tree, keeping the user's own git config
// (signing, hooks, line endings, default branch) out of it.
func (d *demo) git(args ...string) string {
	return d.gitIn(d.dir, nil, args...)
}

func (d *demo) gitIn(dir string, stdin []byte, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, d.env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		fail(fmt.Sprintf("git %s: %v\n%s", strings.Join(args, " "), err, out))
	}
	return strings.TrimSpace(string(out))
}

// new starts a fresh work tree and makes it the current one.
func (d *demo) new(name string) {
	d.dir = filepath.Join(d.work, filepath.FromSlash(name))
	check(os.MkdirAll(d.dir, 0o755))
	d.git("init", "-q", "-b", "main")
}

// write creates or replaces a file in the work tree.
func (d *demo) write(name, content string) {
	p := filepath.Join(d.dir, filepath.FromSlash(name))
	check(os.MkdirAll(filepath.Dir(p), 0o755))
	check(os.WriteFile(p, []byte(content), 0o644))
}

// appendTo adds text to the end of a file in the work tree.
func (d *demo) appendTo(name, content string) {
	f, err := os.OpenFile(filepath.Join(d.dir, filepath.FromSlash(name)), os.O_APPEND|os.O_WRONLY, 0)
	check(err)
	_, err = f.WriteString(content)
	check(err)
	check(f.Close())
}

// edit replaces one exact occurrence of old with new.
func (d *demo) edit(name, old, new string) {
	p := filepath.Join(d.dir, filepath.FromSlash(name))
	b, err := os.ReadFile(p)
	check(err)
	if !strings.Contains(string(b), old) {
		fail(fmt.Sprintf("edit: %q not found in %s", old, name))
	}
	check(os.WriteFile(p, []byte(strings.Replace(string(b), old, new, 1)), 0o644))
}

// executable marks a file executable from the next commit on. The mode
// is set in git's index, so it also works on Windows.
func (d *demo) executable(name string) {
	check(os.Chmod(filepath.Join(d.dir, filepath.FromSlash(name)), 0o755))
	d.exec = append(d.exec, name)
}

// symlink adds a symbolic link to the index without creating it on disk,
// since Windows needs special rights for that. skip-worktree keeps later
// commits from seeing it as deleted.
func (d *demo) symlink(name, target string) {
	hash := d.gitIn(d.dir, []byte(target), "hash-object", "-w", "--stdin")
	d.git("update-index", "--add", "--cacheinfo", "120000,"+hash+","+name)
	d.git("update-index", "--skip-worktree", name)
}

// signingKeys gives robin and kim SSH signing keys, made from fixed seeds
// so demo/canopy.toml can list robin's under [signatures]; kim's isn't
// listed, so kim's commits show as signed but not verified. Without
// ssh-keygen (which git signs with) nothing is signed.
func (d *demo) signingKeys() {
	d.keys = map[string]string{}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		fmt.Println("  ssh-keygen not found: commits won't be signed")
		return
	}
	for _, who := range []string{"robin", "kim"} {
		seed := sha256.Sum256([]byte("canopy demo: " + who))
		block, err := ssh.MarshalPrivateKey(ed25519.NewKeyFromSeed(seed[:]), who+"@example")
		check(err)
		file := filepath.Join(d.work, who+".key")
		check(os.WriteFile(file, pem.EncodeToMemory(block), 0o600))
		d.keys[who] = file
	}
}

// as sets the identity and date for the next commit or tag, and signing
// for those with a key.
func (d *demo) as(who, date string) {
	names := map[string][2]string{
		"robin": {"Robin Example", "robin@example.com"},
		"sam":   {"Sam Rivera", "sam@example.org"},
		"kim":   {"Kim Tanaka", "kim@example.net"},
	}
	n := names[who]
	d.env = []string{
		"GIT_AUTHOR_NAME=" + n[0], "GIT_AUTHOR_EMAIL=" + n[1], "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=" + n[0], "GIT_COMMITTER_EMAIL=" + n[1], "GIT_COMMITTER_DATE=" + date,
	}
	if key, ok := d.keys[who]; ok {
		d.env = append(d.env, "GIT_CONFIG_COUNT=4",
			"GIT_CONFIG_KEY_0=gpg.format", "GIT_CONFIG_VALUE_0=ssh",
			"GIT_CONFIG_KEY_1=user.signingKey", "GIT_CONFIG_VALUE_1="+key,
			"GIT_CONFIG_KEY_2=commit.gpgSign", "GIT_CONFIG_VALUE_2=true",
			"GIT_CONFIG_KEY_3=tag.gpgSign", "GIT_CONFIG_VALUE_3=true")
	}
}

// commit commits everything in the work tree.
func (d *demo) commit(who, date, msg string) {
	d.as(who, date)
	d.git("add", "-A")
	for _, name := range d.exec {
		d.git("update-index", "--chmod=+x", name)
	}
	d.exec = nil
	d.git("commit", "-q", "-m", msg)
}

// publish copies the work tree into out as a bare repository.
func (d *demo) publish(name, description string) {
	bare := filepath.Join(d.out, filepath.FromSlash(name)+".git")
	d.gitIn(d.work, nil, "clone", "-q", "--bare", d.dir, bare)
	check(os.WriteFile(filepath.Join(bare, "description"), []byte(description+"\n"), 0o644))
	fmt.Println(" ", name)
}

// tinyhttpd: a small C project with releases, a merge and an open branch.
func (d *demo) tinyhttpd() {
	d.new("tinyhttpd")
	d.write("README.md", md(`# tinyhttpd

A tiny, single-threaded HTTP/1.1 server for serving static files.
It is meant for local development and small embedded systems, not for
the open internet.

## Building

´´´sh
make
sudo make install
´´´

## Usage

´´´sh
tinyhttpd -p 8000 -r ./public
´´´

| flag | default | meaning                |
|------|---------|------------------------|
| ´-p´ | ´8080´  | port to listen on      |
| ´-r´ | ´.´     | document root          |
| ´-q´ |         | do not log requests    |

## Embedding

The request parser has no dependencies, so it can be used on its own:

´´´c
#include "http.h"

struct request req;
if (parse_request(buf, len, &req) == 0 && req.keep_alive)
	printf("%s %s (keep-alive)\n", req.method, req.path);
´´´

A systemd unit for running it as a service:

´´´ini
[Service]
ExecStart=/usr/local/bin/tinyhttpd -q -p 8000 -r /srv/www
DynamicUser=yes
´´´

See [the manual page](doc/tinyhttpd.1) for details, and the
[changelog](CHANGELOG.md) for what changed between releases.

## License

MIT, see [LICENSE](LICENSE).
`))
	d.write("LICENSE", `MIT License

Copyright (c) 2025 Robin Example

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`)
	d.write("Makefile", `CC ?= cc
CFLAGS ?= -O2 -Wall -Wextra -std=c99
PREFIX ?= /usr/local

OBJ = src/main.o src/http.o

tinyhttpd: $(OBJ)
	$(CC) $(CFLAGS) -o $@ $(OBJ)

clean:
	rm -f tinyhttpd $(OBJ)

.PHONY: clean
`)
	d.write("src/http.h", `#ifndef TINYHTTPD_HTTP_H
#define TINYHTTPD_HTTP_H

#include <stddef.h>

struct request {
	char method[8];
	char path[1024];
	int keep_alive;
};

int parse_request(const char *buf, size_t len, struct request *req);
void serve_file(int fd, const char *root, const struct request *req);

#endif
`)
	d.write("src/http.c", `#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include "http.h"

int
parse_request(const char *buf, size_t len, struct request *req)
{
	(void)len;
	if (sscanf(buf, "%7s %1023s", req->method, req->path) != 2)
		return -1;
	return 0;
}

void
serve_file(int fd, const char *root, const struct request *req)
{
	char path[2048];
	FILE *f;
	char chunk[4096];
	size_t n;

	snprintf(path, sizeof path, "%s%s", root, req->path);
	if (!(f = fopen(path, "rb"))) {
		dprintf(fd, "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\n\r\n");
		return;
	}
	dprintf(fd, "HTTP/1.1 200 OK\r\n\r\n");
	while ((n = fread(chunk, 1, sizeof chunk, f)) > 0)
		write(fd, chunk, n);
	fclose(f);
}
`)
	d.write("src/main.c", `#include <netinet/in.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/socket.h>
#include <unistd.h>
#include "http.h"

int
main(int argc, char *argv[])
{
	int port = 8080, srv, c;
	const char *root = ".";
	struct sockaddr_in addr = {0};
	char buf[8192];
	struct request req;
	ssize_t n;

	if (argc > 1)
		port = atoi(argv[1]);

	srv = socket(AF_INET, SOCK_STREAM, 0);
	addr.sin_family = AF_INET;
	addr.sin_port = htons(port);
	if (bind(srv, (struct sockaddr *)&addr, sizeof addr) < 0) {
		perror("bind");
		return 1;
	}
	listen(srv, 16);

	for (;;) {
		if ((c = accept(srv, NULL, NULL)) < 0)
			continue;
		if ((n = read(c, buf, sizeof buf - 1)) > 0) {
			buf[n] = '\0';
			if (parse_request(buf, n, &req) == 0)
				serve_file(c, root, &req);
		}
		close(c);
	}
}
`)
	d.commit("robin", "2025-11-02T10:14:00+00:00", "Initial commit")

	d.edit("src/http.c", "\tdprintf(fd, \"HTTP/1.1 200 OK",
		"\tif (strstr(req->path, \"..\")) {\n\t\tdprintf(fd, \"HTTP/1.1 403 Forbidden\\r\\nContent-Length: 0\\r\\n\\r\\n\");\n\t\tfclose(f);\n\t\treturn;\n\t}\n\tdprintf(fd, \"HTTP/1.1 200 OK")
	d.commit("sam", "2025-11-09T18:40:00+00:00", `Refuse paths containing ..

A request for /../../etc/passwd would happily walk out of the document
root. Reject any path with a ".." component before opening it.

Reported-by: Kim Tanaka <kim@example.net>`)

	d.write("doc/tinyhttpd.1", `.TH TINYHTTPD 1 2025-11-20
.SH NAME
tinyhttpd \- a tiny static file server
.SH SYNOPSIS
.B tinyhttpd
.RB [ \-q ]
.RB [ \-p
.IR port ]
.RB [ \-r
.IR root ]
.SH DESCRIPTION
.B tinyhttpd
serves files below
.I root
over HTTP/1.1.
`)
	d.commit("robin", "2025-11-20T09:05:00+00:00", "Add a manual page")

	d.write("CHANGELOG.md", `# Changelog

## 0.1.0

- First release: serves static files, rejects path traversal.
`)
	d.commit("robin", "2025-11-21T12:00:00+00:00", "Release 0.1.0")
	d.as("robin", "2025-11-21T12:00:00+00:00")
	d.git("tag", "-a", "v0.1.0", "-m", `tinyhttpd 0.1.0

First public release. It serves files, and it does not serve
/etc/passwd. That is about it.`)

	d.edit("src/main.c", "\tif (argc > 1)\n\t\tport = atoi(argv[1]);\n",
		"\twhile ((c = getopt(argc, argv, \"p:r:q\")) != -1) {\n\t\tswitch (c) {\n\t\tcase 'p':\n\t\t\tport = atoi(optarg);\n\t\t\tbreak;\n\t\tcase 'r':\n\t\t\troot = optarg;\n\t\t\tbreak;\n\t\tcase 'q':\n\t\t\tquiet = 1;\n\t\t\tbreak;\n\t\tdefault:\n\t\t\tfprintf(stderr, \"usage: tinyhttpd [-q] [-p port] [-r root]\\n\");\n\t\t\treturn 1;\n\t\t}\n\t}\n")
	d.edit("src/main.c", "int port = 8080, srv, c;", "int port = 8080, quiet = 0, srv, c;")
	d.edit("src/main.c", "\t\t\t\tserve_file(c, root, &req);\n",
		"\t\t\t\tserve_file(c, root, &req);\n\t\t\tif (!quiet)\n\t\t\t\tfprintf(stderr, \"%s %s\\n\", req.method, req.path);\n")
	d.commit("robin", "2026-01-08T20:31:00+00:00", `Parse options with getopt

Adds -p, -r and -q. The positional port argument is gone; nobody
should have been relying on it yet.`)

	d.git("checkout", "-q", "-b", "feature/keepalive")
	d.edit("src/http.c", "\t\treturn -1;\n\treturn 0;",
		"\t\treturn -1;\n\treq->keep_alive = strstr(buf, \"Connection: keep-alive\") != NULL;\n\treturn 0;")
	d.commit("sam", "2026-01-15T16:02:00+00:00", "Detect Connection: keep-alive")
	d.edit("src/http.c", `HTTP/1.1 200 OK\r\n\r\n");`, `HTTP/1.1 200 OK\r\nConnection: %s\r\n\r\n",
	    req->keep_alive ? "keep-alive" : "close");`)
	d.commit("sam", "2026-01-16T10:45:00+00:00", "Echo the connection mode in responses")

	d.git("checkout", "-q", "main")
	d.appendTo("CHANGELOG.md", `
## Unreleased

- Options are parsed with getopt: -p, -r and -q.
`)
	d.commit("robin", "2026-01-20T08:12:00+00:00", "Update changelog")
	d.as("robin", "2026-02-02T19:30:00+00:00")
	d.git("merge", "-q", "--no-ff", "feature/keepalive", "-m", `Merge branch 'feature/keepalive'

Keep-alive detection from Sam. The connection is still closed after
each response for now; reusing it comes next.`)

	d.git("mv", "src/http.c", "src/request.c")
	d.edit("Makefile", "src/http.o", "src/request.o")
	d.commit("robin", "2026-02-14T11:00:00+00:00", `Rename http.c to request.c

It only deals with requests; responses will get their own file.`)

	d.edit("CHANGELOG.md", "## Unreleased", "## 0.2.0")
	d.appendTo("CHANGELOG.md", "- Keep-alive requests are recognised.\n")
	d.commit("robin", "2026-03-01T15:20:00+00:00", "Release 0.2.0")
	d.as("robin", "2026-03-01T15:20:00+00:00")
	d.git("tag", "-a", "v0.2.0", "-m", `tinyhttpd 0.2.0

- getopt-style options: -p, -r, -q
- Connection: keep-alive is recognised
- src/http.c is now src/request.c`)

	d.write("src/mime.c", `#include <string.h>

static const struct {
	const char *ext, *type;
} types[] = {
	{ ".html", "text/html; charset=utf-8" },
	{ ".css", "text/css" },
	{ ".js", "text/javascript" },
	{ ".png", "image/png" },
	{ ".svg", "image/svg+xml" },
};

const char *
mime_type(const char *path)
{
	const char *dot = strrchr(path, '.');
	size_t i;

	if (dot)
		for (i = 0; i < sizeof types / sizeof types[0]; i++)
			if (strcmp(dot, types[i].ext) == 0)
				return types[i].type;
	return "application/octet-stream";
}
`)
	d.edit("Makefile", "src/request.o", "src/request.o src/mime.o")
	d.commit("kim", "2026-04-11T13:37:00+00:00", "Guess Content-Type from the file extension")

	d.git("checkout", "-q", "-b", "wip/tls")
	d.write("src/tls.c", "/* TODO: wrap the socket with a TLS library */\n")
	d.commit("robin", "2026-05-03T22:10:00+00:00", "WIP: start on TLS support")
	d.git("checkout", "-q", "main")

	d.edit("src/main.c", "listen(srv, 16);", "listen(srv, 64);")
	d.commit("robin", "2026-09-27T09:48:00+00:00", "Raise the listen backlog to 64")
	d.publish("tinyhttpd", "A tiny static file server in C")
}

// .dotfiles: a dot-named repo, plain-text README, symlinks, a script.
func (d *demo) dotfiles() {
	d.new(".dotfiles")
	d.write("README", `dotfiles
========

My configuration files. Install with:

    git clone https://git.example.com/.dotfiles ~/.dotfiles
    ~/.dotfiles/bin/install

Nothing here is clever. Steal whatever you like.
`)
	d.write(".config/fish/config.fish", `set -gx EDITOR vim
set -gx PAGER less
set -g fish_greeting

if status is-interactive
    abbr -a g git
    abbr -a gs 'git status -sb'
    abbr -a gl 'git log --oneline --graph -20'
end

fish_add_path ~/bin ~/go/bin
`)
	d.write(".config/git/config", `[user]
	name = Robin Example
	email = robin@example.com
[init]
	defaultBranch = main
[pull]
	rebase = true
[alias]
	lg = log --oneline --graph --decorate
`)
	d.write(".vimrc", `syntax on
filetype plugin indent on
set number relativenumber
set tabstop=8 shiftwidth=8 noexpandtab
set incsearch hlsearch
nnoremap <leader>w :w<CR>
`)
	d.write("bin/install", `#!/bin/sh
# Link everything into $HOME.
set -e
cd "$(dirname "$0")/.."
for f in .vimrc .config/fish/config.fish .config/git/config; do
	mkdir -p "$HOME/$(dirname "$f")"
	ln -sfn "$PWD/$f" "$HOME/$f"
	echo "linked $f"
done
`)
	d.executable("bin/install")
	d.commit("robin", "2025-08-14T21:00:00+00:00", "Initial dotfiles")
	d.symlink(".gitconfig", ".config/git/config")
	d.commit("robin", "2025-08-15T08:30:00+00:00", "Add .gitconfig symlink for old tools")
	d.edit(".config/fish/config.fish", "set -gx EDITOR vim", "set -gx EDITOR hx")
	d.commit("robin", "2026-06-22T17:45:00+00:00", "fish: switch EDITOR to helix")
	d.publish(".dotfiles", "Configuration files for fish, git and vim")
}

// notes: markdown with relative links and an SVG image.
func (d *demo) notes() {
	d.new("notes")
	d.write("README.md", `# notes

Things I had to look up more than twice.

- [Git recipes](git/recipes.md)
- [Rebasing without fear](git/rebase.md)

![how the pieces fit](images/overview.svg)

> These are personal notes. They are probably wrong somewhere.
`)
	d.write("images/overview.svg", `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 420 120" width="420" height="120" font-family="sans-serif" font-size="14">
  <rect x="10" y="35" width="110" height="50" fill="#dbe9f9" stroke="#0b5bb5"/>
  <text x="65" y="65" text-anchor="middle">working tree</text>
  <rect x="155" y="35" width="110" height="50" fill="#e2f2df" stroke="#1a7f37"/>
  <text x="210" y="65" text-anchor="middle">index</text>
  <rect x="300" y="35" width="110" height="50" fill="#f6f6f6" stroke="#6c757d"/>
  <text x="355" y="65" text-anchor="middle">repository</text>
  <path d="M120 60 H155 M265 60 H300" stroke="#333" stroke-width="2"/>
  <text x="137" y="28" text-anchor="middle" font-size="11">add</text>
  <text x="282" y="28" text-anchor="middle" font-size="11">commit</text>
</svg>
`)
	d.write("git/recipes.md", md(`# Git recipes

## Undo the last commit but keep the changes

´´´sh
git reset --soft HEAD~1
´´´

## Find which commit deleted a file

´´´sh
git log --diff-filter=D --oneline -- path/to/file
´´´

Back to the [index](../README.md).
`))
	d.commit("robin", "2025-12-01T19:12:00+00:00", "Start a notes repository")
	d.write("git/rebase.md", md(`# Rebasing without fear

1. Make a backup branch: ´git branch backup´
2. Rebase: ´git rebase -i main´
3. If it goes wrong: ´git reset --hard backup´

The reflog remembers everything for 90 days anyway.
`))
	d.commit("robin", "2026-02-10T07:55:00+00:00", "Add rebase notes")
	d.appendTo("git/recipes.md", md(`
## Ignore whitespace in blame

´´´sh
git blame -w -M -C -- src/main.c
´´´

The same thing as a config alias:

´´´ini
[alias]
	bw = blame -w -M -C
´´´

## A quick look at a patch

´´´diff
-	listen(srv, 16);
+	listen(srv, 64);
´´´

## Show a file as it was in another branch

´´´sh
git show other-branch:path/to/file
´´´
`))
	d.commit("sam", "2026-08-30T14:03:00+00:00", "recipes: git show branch:path")
	d.publish("notes", "Personal notes, mostly about git")
}

// lab/raytracer: a nested repository name and a binary file.
func (d *demo) raytracer() {
	d.new("lab/raytracer")
	d.write("raytrace.py", `"""A minimal ray tracer: one sphere, one light, PPM output."""
import math
import sys

WIDTH, HEIGHT = 160, 120
SPHERE = ((0.0, 0.0, -3.0), 1.0)
LIGHT = (-0.577, 0.577, 0.577)


def hit_sphere(origin, direction, center, radius):
    oc = [o - c for o, c in zip(origin, center)]
    b = sum(d * o for d, o in zip(direction, oc))
    c = sum(o * o for o in oc) - radius * radius
    disc = b * b - c
    if disc < 0:
        return None
    return -b - math.sqrt(disc)


def main(out):
    out.write(f"P6 {WIDTH} {HEIGHT} 255\n".encode())
    for j in range(HEIGHT):
        for i in range(WIDTH):
            x = (2 * (i + 0.5) / WIDTH - 1) * WIDTH / HEIGHT
            y = 1 - 2 * (j + 0.5) / HEIGHT
            n = math.sqrt(x * x + y * y + 1)
            d = (x / n, y / n, -1 / n)
            t = hit_sphere((0, 0, 0), d, *SPHERE)
            if t is None:
                out.write(bytes((30, 30, 40)))
                continue
            p = [t * c for c in d]
            normal = [(a - b) / SPHERE[1] for a, b in zip(p, SPHERE[0])]
            shade = max(0.0, sum(a * b for a, b in zip(normal, LIGHT)))
            v = int(40 + 215 * shade)
            out.write(bytes((v, v // 2, v // 3)))


if __name__ == "__main__":
    main(sys.stdout.buffer)
`)
	d.commit("robin", "2026-07-04T15:00:00+00:00", "One sphere, one light")
	d.write("render.ppm", string(raytrace()))
	d.commit("robin", "2026-07-04T15:30:00+00:00", "Add a sample render")
	d.publish("lab/raytracer", "")
}

// raytrace draws what raytrace.py draws, so the demo needs no Python.
func raytrace() []byte {
	const width, height = 160.0, 120.0
	center, radius := [3]float64{0, 0, -3}, 1.0
	light := [3]float64{-0.577, 0.577, 0.577}
	var b bytes.Buffer
	fmt.Fprintf(&b, "P6 %d %d 255\n", int(width), int(height))
	for j := 0.0; j < height; j++ {
		for i := 0.0; i < width; i++ {
			x := (2*(i+0.5)/width - 1) * width / height
			y := 1 - 2*(j+0.5)/height
			n := math.Sqrt(x*x + y*y + 1)
			dir := [3]float64{x / n, y / n, -1 / n}
			var bb, c float64
			for k := range 3 {
				oc := -center[k]
				bb += dir[k] * oc
				c += oc * oc
			}
			c -= radius * radius
			disc := bb*bb - c
			if disc < 0 {
				b.Write([]byte{30, 30, 40})
				continue
			}
			t := -bb - math.Sqrt(disc)
			var shade float64
			for k := range 3 {
				shade += (t*dir[k] - center[k]) / radius * light[k]
			}
			v := int(40 + 215*max(0, shade))
			b.Write([]byte{byte(v), byte(v / 2), byte(v / 3)})
		}
	}
	return b.Bytes()
}

// canopy: a snapshot of this project's own source.
func (d *demo) canopy() {
	d.new("canopy")
	for _, name := range []string{"main.go", "go.mod", "go.sum", "README.md", "install.sh", "internal", "config"} {
		src := filepath.Join(d.project, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		check(filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return err
			}
			rel, err := filepath.Rel(d.project, p)
			if err != nil {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			// A Windows checkout has CRLF line endings; commit them as LF.
			d.write(filepath.ToSlash(rel), strings.ReplaceAll(string(b), "\r\n", "\n"))
			return nil
		}))
	}
	d.executable("install.sh")
	d.commit("robin", time.Now().UTC().Format("2006-01-02T15:04:05+00:00"), `Import canopy

A read-only web interface for git repositories, in the spirit of cgit
and legit, with a sourcehut-style look.`)
	d.publish("canopy", "The web interface you are looking at")
}

// scratch: an empty repository. secret: hidden by the demo config.
func (d *demo) scratchAndSecret() {
	scratch := filepath.Join(d.out, "scratch.git")
	d.gitIn(d.work, nil, "init", "-q", "--bare", "-b", "main", scratch)
	check(os.WriteFile(filepath.Join(scratch, "description"), []byte("Nothing here yet\n"), 0o644))
	fmt.Println("  scratch (empty)")

	d.new("secret")
	d.write("README", "you should not see this\n")
	d.commit("robin", "2026-01-01T00:00:00+00:00", "Hidden repository")
	d.publish("secret", "Hidden by [repos.secret] in the config")
}
