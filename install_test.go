package main

// Tests for install.sh. They run its deploy and install flows as dry runs,
// with fakes first on the PATH for ssh, curl (GitHub) and the account
// lookups, and run the canopy-github-keys script it writes against files of
// their own. Nothing leaves this machine or changes it. They need Linux and
// bash, as install.sh does.

import (
	"crypto/ed25519"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeCurl answers https://HOST/PATH with the file $FAKE_WEB/HOST/PATH and
// the HTTP status in PATH.status (200 without one); no file is a 404. With
// $FAKE_WEB/down nothing answers. Every URL asked for goes in $FAKE_WEB/asked.
const fakeCurl = `#!/usr/bin/env bash
out= format= fail=0 url=
while (($#)); do
	case $1 in
	-o) out=$2; shift ;;
	-w) format=$2; shift ;;
	-H | --max-time | --proto) shift ;;
	--*) ;;
	-*f*) fail=1 ;;
	-*) ;;
	*) url=$1 ;;
	esac
	shift
done
echo "$url" >>"$FAKE_WEB/asked"
if [[ -e $FAKE_WEB/down ]]; then
	echo "curl: (6) Could not resolve host" >&2
	exit 6
fi
file=$FAKE_WEB/${url#https://}
status=404 body=/dev/null
if [[ -f $file ]]; then
	status=$(cat "$file.status" 2>/dev/null || echo 200)
	body=$file
fi
if ((fail && status >= 400)); then
	exit 22
fi
if [[ -n $out ]]; then cat "$body" >"$out"; else cat "$body"; fi
if [[ -n $format ]]; then printf '%s' "${format//'%{http_code}'/$status}"; fi
`

// fakeSSH answers what install.sh asks a server, runs the upload in
// $FAKE_REMOTE, and notes every other command in $FAKE_REMOTE/commands.
const fakeSSH = `#!/usr/bin/env bash
cmd=
while (($#)); do
	case $1 in
	-O) exit 0 ;;
	-o | -F | -p | -i | -l | -J) shift ;;
	-*) ;;
	*) shift; cmd=$*; break ;;
	esac
	shift
done
case $cmd in
"") ;;
"id -un") echo root ;;
"uname -m") echo x86_64 ;;
*os-release*) echo "Test Linux" ;;
"mktemp -d") mktemp -d -p "$FAKE_REMOTE" ;;
"mkdir -p "*" && tar -C "*) cd "$FAKE_REMOTE" && bash -c "$cmd" ;;
*) echo "$cmd" >>"$FAKE_REMOTE/commands" ;;
esac
`

// fakeID knows only the accounts in $FAKE_PASSWD, all in the sudo group;
// about whoever runs it, it asks the real id (REAL).
const fakeID = `#!/usr/bin/env bash
user=${!#}
if (($# == 0)) || [[ $user == -* ]]; then
	exec REAL "$@"
fi
if ! grep -q "^$user:" "$FAKE_PASSWD"; then
	echo "id: '$user': no such user" >&2
	exit 1
fi
case $1 in
-nG) echo "$user sudo" ;;
-gn) echo "$user" ;;
*) echo "uid=1000($user) gid=1000($user) groups=1000($user),27(sudo)" ;;
esac
`

// fakeGetent has the accounts in $FAKE_PASSWD, and asks the real one (REAL)
// about the rest.
const fakeGetent = `#!/usr/bin/env bash
if [[ $1 == passwd && $# == 2 ]]; then
	grep "^$2:" "$FAKE_PASSWD" || exit 2
	exit 0
fi
exec REAL "$@"
`

// fakeCanopy is the binary install.sh installs, as far as -check goes.
const fakeCanopy = `#!/bin/sh
printf 'listen: 127.0.0.1:8080\nroot: /srv/git\nfavicon: \nabout: \nmirrors: 0\n'
`

// sandbox is one test's world for install.sh.
type sandbox struct {
	t   *testing.T
	dir string
	env []string
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("install.sh runs on Linux")
	}
	real := map[string]string{}
	for _, cmd := range []string{"bash", "ssh-keygen", "id", "getent"} {
		path, err := exec.LookPath(cmd)
		if err != nil {
			t.Skip(cmd + " not installed")
		}
		real[cmd] = path
	}
	s := &sandbox{t: t, dir: t.TempDir()}
	for name, script := range map[string]string{
		"curl":      fakeCurl,
		"ssh":       fakeSSH,
		"id":        strings.Replace(fakeID, "REAL", real["id"], 1),
		"getent":    strings.Replace(fakeGetent, "REAL", real["getent"], 1),
		"canopy":    fakeCanopy,
		"caddy":     "#!/bin/sh\n",
		"sudo":      "#!/bin/sh\n",
		"git-shell": "#!/bin/sh\n",
	} {
		s.file("bin/"+name, script)
		os.Chmod(s.path("bin/"+name), 0o755)
	}
	s.file("passwd", "")
	s.file("config/canopy.toml", "root = \"/srv/git\"\ntitle = \"test\"\n")
	for _, d := range []string{"web", "remote", "home"} {
		os.MkdirAll(s.path(d), 0o755)
	}
	s.env = append(os.Environ(),
		"PATH="+s.path("bin")+":"+os.Getenv("PATH"),
		"HOME="+s.path("home"),
		"TMPDIR="+s.dir,
		"FAKE_WEB="+s.path("web"),
		"FAKE_REMOTE="+s.path("remote"),
		"FAKE_PASSWD="+s.path("passwd"),
		"CANOPY_CONFIG_DIR="+s.path("config"),
		"DEPLOY_SSH_OPTS=",
		"TS_AUTHKEY=",
	)
	return s
}

func (s *sandbox) path(name string) string { return filepath.Join(s.dir, name) }

// file writes name in the sandbox, making its folders, and returns its path.
func (s *sandbox) file(name, content string) string {
	s.t.Helper()
	path := s.path(name)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
	return path
}

func (s *sandbox) read(name string) string {
	b, _ := os.ReadFile(s.path(name))
	return string(b)
}

// answer has the fake web answer url with status and body; status 0 takes
// the answer away (a 404).
func (s *sandbox) answer(url string, status int, body string) {
	s.t.Helper()
	name := "web/" + strings.TrimPrefix(url, "https://")
	os.Remove(s.path(name + ".status"))
	if status == 0 {
		os.Remove(s.path(name))
		return
	}
	s.file(name, body)
	if status != 200 {
		s.file(name+".status", strconv.Itoa(status))
	}
}

// run runs a bash script (a file, or -c and the script) in the sandbox.
func (s *sandbox) run(env []string, args ...string) (string, error) {
	cmd := exec.Command("bash", args...)
	cmd.Env = append(s.env, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *sandbox) install(args ...string) (string, error) {
	return s.run(nil, append([]string{"install.sh"}, args...)...)
}

// written returns what a dry run shows it would write to path: "    write
// PATH (mode M):", then the file's lines after "      | ".
func written(t *testing.T, out, path string) string {
	t.Helper()
	lines := strings.Split(out, "\n")
	for i, line := range slices.Backward(lines) {
		if !strings.HasPrefix(line, "    write "+path+" (mode ") {
			continue
		}
		var body strings.Builder
		for _, l := range lines[i+1:] {
			rest, ok := strings.CutPrefix(l, "      | ")
			if !ok {
				break
			}
			body.WriteString(rest)
			body.WriteByte('\n')
		}
		return body.String()
	}
	t.Fatalf("the dry run doesn't write %s:\n%s", path, out)
	return ""
}

// shellFunc returns the definition of install.sh's function name, from its
// "name() {" line to the "}" closing it, at the same indentation.
func shellFunc(t *testing.T, name string) string {
	t.Helper()
	src, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	for i, l := range lines {
		body := strings.TrimLeft(l, "\t")
		if !strings.HasPrefix(body, name+"() {") {
			continue
		}
		if strings.HasSuffix(body, "}") {
			return body
		}
		end := l[:len(l)-len(body)] + "}"
		for j := i + 1; j < len(lines); j++ {
			if lines[j] == end {
				return strings.Join(lines[i:j+1], "\n")
			}
		}
	}
	t.Fatalf("install.sh has no function %s", name)
	return ""
}

// testKeys returns n different public keys, "ssh-ed25519 AAAA…".
func testKeys(t *testing.T, n int) []string {
	t.Helper()
	var keys []string
	for i := range n {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		pub, err := ssh.NewPublicKey(ed25519.NewKeyFromSeed(seed).Public())
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))))
	}
	return keys
}

// TestDeployUninstall: ./install.sh HOST --uninstall runs the uninstall on
// the server and succeeds. It used to stop with "ts_arg: unbound variable",
// and its cleanup at exit failed the run even so.
func TestDeployUninstall(t *testing.T) {
	s := newSandbox(t)
	out, err := s.install("root@test.example", "--uninstall", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if cmds := s.read("remote/commands"); !strings.Contains(cmds, "canopy/install.sh --uninstall") {
		t.Errorf("the server wasn't asked to uninstall; it was asked:\n%s", cmds)
	}
}

// TestDeployLeavesGitHubKeysToTheServer: a deploy checks the GitHub names
// it is given, but uploads only the keys from files: the server fetches the
// GitHub keys itself, from the accounts it follows, with sync or without.
func TestDeployLeavesGitHubKeysToTheServer(t *testing.T) {
	s := newSandbox(t)
	keys := testKeys(t, 2)
	s.answer("https://github.com/alice.keys", 200, keys[1]+"\n")
	pub := s.file("me.pub", keys[0]+" me@laptop\n")
	s.file("config/deploy.env", "github_sync=0\nharden=0\nadmin_user=none\nsigners=0\n")
	out, err := s.install("root@test.example", "--yes", "--dry-run", "--binary", s.path("bin/canopy"),
		"--domain", "test.example", "--push-key", pub, "--github-keys", "alice")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	uploads, _ := filepath.Glob(s.path("remote/tmp.*/install-command.sh"))
	if len(uploads) != 1 {
		t.Fatalf("no upload (%v):\n%s", uploads, out)
	}
	upload := filepath.Dir(uploads[0])
	command, _ := os.ReadFile(uploads[0])
	if !strings.Contains(string(command), " --github-keys alice ") || strings.Contains(string(command), "--github-sync") {
		t.Errorf("install-command.sh: want --github-keys alice and no --github-sync:\n%s", command)
	}
	if b, _ := os.ReadFile(filepath.Join(upload, "push-keys.pub")); string(b) != keys[0]+" me@laptop\n" {
		t.Errorf("push-keys.pub = %q, want only the key from the file", b)
	}
}

// TestGitHubAccount: install.sh follows the account a GitHub name named the
// first time it was given, by ID, not whoever has the name now.
func TestGitHubAccount(t *testing.T) {
	s := newSandbox(t)
	s.answer("https://api.github.com/users/alice", 200, `{"login": "alice", "id": 101, "node_id": "x"}`)
	s.answer("https://api.github.com/user/101", 200, "{\n  \"login\": \"alice-new\",\n  \"id\": 101,\n  \"node_id\": \"x\"\n}\n")
	s.answer("https://api.github.com/users/carol", 403, `{"message": "API rate limit exceeded"}`)
	script := strings.Join([]string{"set -euo pipefail",
		`note() { echo "note: $*"; }`, `die() { echo "die: $*"; exit 1; }`,
		"GH_IDS=/etc/canopy/github-ids", "declare -A GH_ID=()",
		shellFunc(t, "is_github_user"), shellFunc(t, "json_field"), shellFunc(t, "github_account"),
		`[[ -z $PINNED ]] || GH_ID[${NAME,,}]=$PINNED`,
		`github_account "$NAME"`,
		`echo "$GH_LOGIN ${GH_ID[${NAME,,}]}"`}, "\n")
	for _, c := range []struct {
		name, pinned string
		ok           bool
		want, asked  string
	}{
		{"alice", "", true, "alice 101", "https://api.github.com/users/alice"},              // first given: looked up by name
		{"Alice", "101", true, "alice-new 101", "https://api.github.com/user/101"},          // since: by ID, though renamed
		{"alice", "102", false, "no longer exists", "https://api.github.com/user/102"},      // the account is gone
		{"nobody", "", false, "no such GitHub user", "https://api.github.com/users/nobody"}, // never was
		{"carol", "", false, "HTTP 403", "https://api.github.com/users/carol"},
	} {
		os.Remove(s.path("web/asked"))
		out, err := s.run([]string{"NAME=" + c.name, "PINNED=" + c.pinned}, "-c", script)
		if (err == nil) != c.ok || !strings.Contains(out, c.want) {
			t.Errorf("%s (pinned %q): %v\n%s\nwant %q", c.name, c.pinned, err, out, c.want)
		}
		if asked := strings.TrimSpace(s.read("web/asked")); asked != c.asked {
			t.Errorf("%s (pinned %q): asked %q, want %q", c.name, c.pinned, asked, c.asked)
		}
	}
}

// TestInstallWithGitHubKeys runs the installer with keys from GitHub for
// pushes and the admin account, kept in sync, then the sync script it writes.
func TestInstallWithGitHubKeys(t *testing.T) {
	s := newSandbox(t)
	k := testKeys(t, 6)
	s.answer("https://api.github.com/users/alice", 200, `{"login": "alice", "id": 101}`)
	s.answer("https://api.github.com/user/101", 200, `{"login": "alice", "id": 101}`)
	s.answer("https://github.com/alice.keys", 200, k[1]+"\n"+k[2]+"\n")
	pub := s.file("me.pub", k[0]+" me@laptop\n")
	s.file("passwd", "bob:x:1000:1000::"+s.path("home/bob")+":/bin/bash\n")
	// The admin's own keys: one with options that GitHub has too, a
	// comment, another key.
	adminKeys := s.file("home/bob/.ssh/authorized_keys",
		`from="10.0.0.0/8" `+k[1]+" bob@laptop\n\n# an old key\n"+k[3]+" bob@desktop\n")
	out, err := s.install("--dry-run", "--binary", s.path("bin/canopy"), "--config", s.path("config/canopy.toml"),
		"--domain", "test.example", "--push-key", pub, "--github-keys", "alice", "--github-sync",
		"--admin-user", "bob", "--admin-github", "alice")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	// The account behind the name is kept, and the sync follows it.
	if got := written(t, out, "/etc/canopy/github-ids"); !strings.HasSuffix(got, "\nalice 101\n") {
		t.Errorf("github-ids:\n%s\nwant alice 101", got)
	}
	conf := written(t, out, "/etc/canopy/github-keys.conf")
	if !strings.Contains(conf, "\npush alice 101\n") || !strings.Contains(conf, "\nadmin bob alice 101\n") {
		t.Errorf("github-keys.conf:\n%s\nwant push alice 101 and admin bob alice 101", conf)
	}
	gitKeys := written(t, out, "/etc/ssh/canopy_authorized_keys")
	if want := "restrict " + k[0] + " me@laptop\nrestrict " + k[1] + " github:alice\nrestrict " + k[2] + " github:alice\n"; gitKeys != want {
		t.Errorf("push keys:\n%s\nwant:\n%s", gitKeys, want)
	}
	// The admin's lines stay as they were, and GitHub's copy of the key
	// with options doesn't come in beside it without them.
	admin := written(t, out, adminKeys)
	if want := `from="10.0.0.0/8" ` + k[1] + " bob@laptop\n# an old key\n" + k[3] + " bob@desktop\n" + k[2] + " github:alice\n"; admin != want {
		t.Errorf("admin keys:\n%s\nwant:\n%s", admin, want)
	}

	// The sync script, pointed at files of the test's own.
	script := written(t, out, "/usr/local/sbin/canopy-github-keys")
	for from, to := range map[string]string{
		"conf=/etc/canopy/github-keys.conf\n":        "conf=" + s.file("sync/github-keys.conf", conf) + "\n",
		"git_keys=/etc/ssh/canopy_authorized_keys\n": "git_keys=" + s.file("sync/push-keys", gitKeys) + "\n",
	} {
		if !strings.Contains(script, from) {
			t.Fatalf("canopy-github-keys has no %q:\n%s", from, script)
		}
		script = strings.Replace(script, from, to, 1)
	}
	s.file("sync/canopy-github-keys", script)
	s.file("home/bob/.ssh/authorized_keys", admin)
	sync := func() (string, error) { return s.run(nil, s.path("sync/canopy-github-keys")) }

	// Renamed, with a key added, while someone else registers "alice": the
	// keys come from the renamed account, which is looked up once.
	s.answer("https://api.github.com/user/101", 200, `{"login": "alice-new", "id": 101}`)
	s.answer("https://github.com/alice-new.keys", 200, k[1]+"\n"+k[4]+"\n")
	s.answer("https://api.github.com/users/alice", 200, `{"login": "alice", "id": 999}`)
	s.answer("https://github.com/alice.keys", 200, k[5]+"\n")
	os.Remove(s.path("web/asked"))
	if out, err := sync(); err != nil {
		t.Fatalf("sync after the rename: %v\n%s", err, out)
	}
	if got, want := s.read("sync/push-keys"), "restrict "+k[0]+" me@laptop\nrestrict "+k[1]+" github:alice\nrestrict "+k[4]+" github:alice\n"; got != want {
		t.Errorf("push keys after the rename:\n%s\nwant:\n%s", got, want)
	}
	if got, want := s.read("home/bob/.ssh/authorized_keys"), `from="10.0.0.0/8" `+k[1]+" bob@laptop\n# an old key\n"+k[3]+" bob@desktop\n"+k[4]+" github:alice\n"; got != want {
		t.Errorf("admin keys after the rename:\n%s\nwant:\n%s", got, want)
	}
	asked := s.read("web/asked")
	if strings.Count(asked, "https://api.github.com/user/101\n") != 1 || strings.Contains(asked, "/alice\n") || strings.Contains(asked, "/alice.keys") {
		t.Errorf("asked:\n%swant account 101 once, and nothing about the name alice", asked)
	}

	// GitHub down: nothing changes.
	before := s.read("sync/push-keys") + s.read("home/bob/.ssh/authorized_keys")
	s.file("web/down", "")
	if out, err := sync(); err == nil {
		t.Errorf("sync with GitHub down succeeded:\n%s", out)
	}
	if after := s.read("sync/push-keys") + s.read("home/bob/.ssh/authorized_keys"); after != before {
		t.Errorf("GitHub down changed the keys:\n%s", after)
	}
	os.Remove(s.path("web/down"))

	// The account is deleted: its keys go, though the name has an account.
	s.answer("https://api.github.com/user/101", 0, "")
	if out, err := sync(); err != nil {
		t.Fatalf("sync after the account went: %v\n%s", err, out)
	}
	if got, want := s.read("sync/push-keys"), "restrict "+k[0]+" me@laptop\n"; got != want {
		t.Errorf("push keys after the account went:\n%s\nwant:\n%s", got, want)
	}
	if got, want := s.read("home/bob/.ssh/authorized_keys"), `from="10.0.0.0/8" `+k[1]+" bob@laptop\n# an old key\n"+k[3]+" bob@desktop\n"; got != want {
		t.Errorf("admin keys after the account went:\n%s\nwant:\n%s", got, want)
	}
}

// TestCaddySetsTheVisitorsAddress: Caddy always sets X-Real-IP itself, with
// Anubis or without. canopy takes it for the visitor's address, so one sent
// by the visitor must never get through.
func TestCaddySetsTheVisitorsAddress(t *testing.T) {
	s := newSandbox(t)
	for _, extra := range [][]string{nil, {"--anubis"}} {
		args := append([]string{"--dry-run", "--binary", s.path("bin/canopy"), "--config", s.path("config/canopy.toml"),
			"--domain", "test.example"}, extra...)
		out, err := s.install(args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", extra, err, out)
		}
		site := written(t, out, "/etc/caddy/Caddyfile.d/canopy.caddyfile")
		if strings.Count(site, "header_up X-Real-IP {remote_host}\n") != 1 {
			t.Errorf("%v: want X-Real-IP set once:\n%s", extra, site)
		}
	}
}

// TestSetTitle: the title goes into the config as typed, & and | too.
func TestSetTitle(t *testing.T) {
	s := newSandbox(t)
	cfg := s.file("canopy.toml", "# title = \"git\"\ntitle = \"git\"\nheader = \"x\"\n")
	title := "Notes & code | ~/$HOME's &amp;"
	out, err := s.run([]string{"CFG=" + cfg, "NEW_TITLE=" + title}, "-c",
		"set -euo pipefail\n"+shellFunc(t, "set_title")+"\nset_title \"$CFG\" \"$NEW_TITLE\"\n")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got, want := s.read("canopy.toml"), "# title = \"git\"\ntitle = \""+title+"\"\nheader = \"x\"\n"; got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestShellBlocksEndInACommand: a function or { } block whose last line is
// "test && command" ends with status 1 when the test is false. Under set -e
// that stops the script without a word when the function is called, or the
// block is piped (with pipefail): install.sh 0.4.0 stopped half way like
// that, and a deploy's cleanup at exit turned success into failure. Write
// "test || command", or an if.
func TestShellBlocksEndInACommand(t *testing.T) {
	strip := regexp.MustCompile(`"[^"]*"|'[^']*'|\[\[.*?\]\]`)
	for _, name := range []string{"install.sh", "update.sh"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
		for i, line := range lines {
			if l := strings.TrimSpace(line); l != "}" && !strings.HasPrefix(l, "} ") {
				continue
			}
			j := i - 1
			for j > 0 && (strings.TrimSpace(lines[j]) == "" || strings.HasPrefix(strings.TrimSpace(lines[j]), "#")) {
				j--
			}
			last := strip.ReplaceAllString(lines[j], "")
			if k := strings.LastIndex(last, "&&"); k >= 0 && !strings.Contains(last[k:], "||") {
				t.Errorf("%s:%d: the block ends in an && list: %s", name, j+1, strings.TrimSpace(lines[j]))
			}
		}
	}
}
