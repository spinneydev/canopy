package sigcheck

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// newKey makes an ed25519 key from a fixed seed, writes its private half
// where ssh-keygen can use it and returns the public line.
func newKey(t *testing.T, dir string, seed byte) (ssh.Signer, string, string) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "key"+string('a'+seed))
	if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return signer, file, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// sign makes an armored SSHSIG signature, as ssh-keygen -Y sign would.
func sign(t *testing.T, s ssh.Signer, namespace string, message []byte) []byte {
	t.Helper()
	h := sha512.Sum512(message)
	sig, err := s.Sign(nil, signedData(namespace, "sha512", h[:]))
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte("SSHSIG")
	blob = append(blob, ssh.Marshal(struct {
		Version   uint32
		PublicKey []byte
		Namespace string
		Reserved  string
		HashAlg   string
		Signature []byte
	}{1, s.PublicKey().Marshal(), namespace, "", "sha512", ssh.Marshal(sig)})...)
	return []byte(sshArmor + "\n" + base64.StdEncoding.EncodeToString(blob) + "\n-----END SSH SIGNATURE-----\n")
}

func git(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=alice", "GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=alice", "GIT_COMMITTER_EMAIL=alice@example.com")
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = exit.Stderr
		}
		t.Fatalf("git %v: %v\n%s", args, err, stderr)
	}
	return out
}

// TestGitSignatures checks commits and tags signed by git itself, with
// ssh-keygen, so the payload is cut exactly as git cuts it.
func TestGitSignatures(t *testing.T) {
	for _, p := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skip(p + " not installed")
		}
	}
	dir := t.TempDir()
	_, keyFile, pub := newKey(t, dir, 1)
	_, _, other := newKey(t, dir, 2)

	work := filepath.Join(dir, "work")
	os.Mkdir(work, 0o755)
	git(t, work, "init", "-q", "-b", "main")
	git(t, work, "config", "gpg.format", "ssh")
	git(t, work, "config", "user.signingkey", keyFile)
	os.WriteFile(filepath.Join(work, "a"), []byte("a\n"), 0o644)
	git(t, work, "add", "a")
	git(t, work, "commit", "-q", "-m", "unsigned")
	unsigned := git(t, work, "cat-file", "commit", "HEAD")
	git(t, work, "commit", "-q", "-S", "--allow-empty", "-m", "signed\n\nwith a body\n")
	commit := git(t, work, "cat-file", "commit", "HEAD")
	git(t, work, "tag", "-s", "-m", "v1.0.0", "v1.0.0")
	tag := git(t, work, "cat-file", "tag", "v1.0.0")

	trusted, err := NewKeys(map[string][]string{"alice": {pub + " alice@laptop"}})
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := NewKeys(map[string][]string{"bob": {other}})
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(commit, []byte("with a body"), []byte("with a bodY"), 1)

	for _, c := range []struct {
		name   string
		got    Result
		status Status
	}{
		{"unsigned commit", trusted.Commit(unsigned, false), Unsigned},
		{"signed commit", trusted.Commit(commit, false), Verified},
		{"signed tag", trusted.Tag(tag), Verified},
		{"unknown key", stranger.Commit(commit, false), Signed},
		{"changed message", trusted.Commit(tampered, false), Bad},
		{"sha256 header", trusted.Commit(commit, true), Unsigned},
	} {
		if c.got.Status != c.status {
			t.Errorf("%s: %+v, want status %d", c.name, c.got, c.status)
		}
	}
	if r := trusted.Commit(commit, false); r.Signer != "alice" {
		t.Errorf("signer = %q, want alice", r.Signer)
	}
}

func TestSignatureKinds(t *testing.T) {
	signer, _, pub := newKey(t, t.TempDir(), 3)
	keys, err := NewKeys(map[string][]string{"alice": {pub}})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("object 0123\ntype commit\ntag v1\ntagger alice <a@b> 1700000000 +0000\n\nv1\n")
	for _, c := range []struct {
		name   string
		raw    []byte
		status Status
	}{
		{"good", append(body, sign(t, signer, "git", body)...), Verified},
		{"other namespace", append(body, sign(t, signer, "file", body)...), Bad},
		{"other message", append(body, sign(t, signer, "git", []byte("x"))...), Bad},
		{"cut off", append(body, sign(t, signer, "git", body)[:60]...), Bad},
		{"pgp", append(body, "-----BEGIN PGP SIGNATURE-----\n\nx\n-----END PGP SIGNATURE-----\n"...), Signed},
		{"marker inside a line", append(body, "see -----BEGIN PGP SIGNATURE-----\n"...), Unsigned},
	} {
		if got := keys.Tag(c.raw); got.Status != c.status {
			t.Errorf("%s: %+v, want status %d", c.name, got, c.status)
		}
	}
}

func TestNewKeysRefuses(t *testing.T) {
	_, _, pub := newKey(t, t.TempDir(), 4)
	for name, keys := range map[string]map[string][]string{
		"not a key":  {"alice": {"ssh-ed25519 nonsense"}},
		"no keys":    {"alice": {}},
		"empty name": {"": {pub}},
		"twice":      {"alice": {pub}, "bob": {pub}},
		"options":    {"alice": {`restrict ` + pub}},
		"two keys":   {"alice": {pub + "\n" + pub}},
	} {
		if _, err := NewKeys(keys); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
