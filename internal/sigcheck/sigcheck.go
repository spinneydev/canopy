// Package sigcheck checks the signatures on commits and tags. SSH signatures
// are verified here, in Go, against the keys the config trusts; git's own
// checks would run a program (gpg.ssh.program), which canopy never lets
// git do. PGP and X.509 signatures are noticed but not checked.
package sigcheck

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/crypto/ssh"
)

type Status int

const (
	Unsigned Status = iota
	Verified        // a good SSH signature by a trusted key
	Signed          // a good SSH signature by another key, or a PGP or X.509 one (not checked)
	Bad             // an SSH signature that doesn't match the object
)

// Result is what a page shows about one commit or tag.
type Result struct {
	Status Status
	Signer string // the trusted key's name, for Verified
	Note   string // why, for Signed and Bad
}

// Keys are the trusted SSH keys, by fingerprint.
type Keys struct {
	names map[string]string
}

// NewKeys parses [signatures] keys: a name, shown on the badge, with that
// person's public keys as in authorized_keys ("ssh-ed25519 AAAA… comment").
func NewKeys(byName map[string][]string) (*Keys, error) {
	k := &Keys{names: map[string]string{}}
	for name, keys := range byName {
		if name == "" || len(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
			return nil, fmt.Errorf("%q isn't a name of at most 64 characters", name)
		}
		if len(keys) == 0 {
			return nil, fmt.Errorf("%s: no keys", name)
		}
		for _, line := range keys {
			pub, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
			if err != nil {
				return nil, fmt.Errorf("%s: %q isn't an SSH public key like \"ssh-ed25519 AAAA…\": %v", name, line, err)
			}
			if len(options) > 0 || len(bytes.TrimSpace(rest)) > 0 {
				return nil, fmt.Errorf("%s: give one key per entry, without options", name)
			}
			if _, ok := pub.(*ssh.Certificate); ok {
				return nil, fmt.Errorf("%s: certificates aren't supported; give the key itself", name)
			}
			fp := ssh.FingerprintSHA256(pub)
			if other, ok := k.names[fp]; ok {
				return nil, fmt.Errorf("%s: key %s is listed under %s too", name, fp, other)
			}
			k.names[fp] = name
		}
	}
	return k, nil
}

// Len is the number of trusted keys.
func (k *Keys) Len() int { return len(k.names) }

// List gives each key as "name fingerprint", sorted.
func (k *Keys) List() []string {
	var list []string
	for fp, name := range k.names {
		list = append(list, name+" "+fp)
	}
	slices.Sort(list)
	return list
}

// Label and Class are how a page shows the result; both are empty for an
// unsigned object.
func (r Result) Label() string {
	return [...]string{"", "verified", "signed", "bad signature"}[r.Status]
}

func (r Result) Class() string {
	return [...]string{"", "verified", "signed", "bad"}[r.Status]
}

// Object checks a raw commit or tag object, telling them apart by their
// first header.
func (k *Keys) Object(raw []byte, sha256 bool) Result {
	if bytes.HasPrefix(raw, []byte("object ")) {
		return k.Tag(raw)
	}
	return k.Commit(raw, sha256)
}

// Commit checks a raw commit object (git cat-file commit). sha256 says the
// repository uses SHA-256 object names, which keep their signature under
// another header.
func (k *Keys) Commit(raw []byte, sha256 bool) Result {
	header := "gpgsig"
	if sha256 {
		header = "gpgsig-sha256"
	}
	payload, sig, ok := splitCommit(raw, header)
	if !ok {
		return Result{}
	}
	return k.check(payload, sig)
}

// Tag checks a raw tag object (git cat-file tag), whose signature ends its
// message.
func (k *Keys) Tag(raw []byte) Result {
	start := -1
	for _, marker := range []string{"-----BEGIN PGP SIGNATURE-----", "-----BEGIN PGP MESSAGE-----",
		"-----BEGIN SIGNED MESSAGE-----", "-----BEGIN SSH SIGNATURE-----"} {
		// The last one, at the start of a line, as git does.
		for i := 0; ; {
			j := bytes.Index(raw[i:], []byte(marker))
			if j < 0 {
				break
			}
			if i+j == 0 || raw[i+j-1] == '\n' {
				start = max(start, i+j)
			}
			i += j + 1
		}
	}
	if start < 0 {
		return Result{}
	}
	return k.check(raw[:start], raw[start:])
}

// splitCommit takes the signature header out of a commit, as git's
// parse_buffer_signed_by_header does: the rest is what was signed.
// Signatures for the other hash algorithm are left out of both.
func splitCommit(raw []byte, header string) (payload, sig []byte, ok bool) {
	inSig, otherSig := false, false
	for i := 0; i < len(raw); {
		end := len(raw)
		if j := bytes.IndexByte(raw[i:], '\n'); j >= 0 {
			end = i + j + 1
		}
		line := raw[i:end]
		i = end
		var s []byte
		switch {
		case inSig && line[0] == ' ':
			s = line[1:]
		case bytes.HasPrefix(line, []byte(header+" ")):
			s = line[len(header)+1:]
			otherSig = false
		case bytes.HasPrefix(line, []byte("gpgsig")):
			otherSig = true
		case otherSig && line[0] != ' ':
			otherSig = false
		}
		if s != nil {
			sig = append(sig, s...)
			ok, inSig = true, true
			continue
		}
		inSig = false
		if line[0] == '\n' {
			// The message: everything from here on was signed.
			return append(payload, raw[end-len(line):]...), sig, ok
		}
		if !otherSig {
			payload = append(payload, line...)
		}
	}
	return payload, sig, ok
}

const sshArmor = "-----BEGIN SSH SIGNATURE-----"

func (k *Keys) check(payload, sig []byte) Result {
	if !bytes.HasPrefix(sig, []byte(sshArmor)) {
		kind := "a PGP"
		if bytes.HasPrefix(sig, []byte("-----BEGIN SIGNED MESSAGE-----")) {
			kind = "an X.509"
		}
		return Result{Status: Signed, Note: "signed with " + kind + " key; this site checks SSH signatures only"}
	}
	pub, err := verify(payload, sig, "git")
	if err != nil {
		return Result{Status: Bad, Note: err.Error()}
	}
	fp := ssh.FingerprintSHA256(pub)
	if name, ok := k.names[fp]; ok {
		return Result{Status: Verified, Signer: name, Note: "signed with " + name + "'s key " + fp}
	}
	return Result{Status: Signed, Note: "signed with an SSH key this site doesn't list: " + fp}
}

// verify checks an armored SSHSIG signature of message (OpenSSH's
// PROTOCOL.sshsig) and returns the key that made it.
func verify(message, armored []byte, namespace string) (ssh.PublicKey, error) {
	body, ok := bytes.CutPrefix(bytes.TrimSpace(armored), []byte(sshArmor))
	if !ok {
		return nil, errors.New("not an SSH signature")
	}
	body, ok = bytes.CutSuffix(body, []byte("-----END SSH SIGNATURE-----"))
	if !ok {
		return nil, errors.New("the signature is cut off")
	}
	blob, err := base64.StdEncoding.DecodeString(string(bytes.Join(bytes.Fields(body), nil)))
	if err != nil {
		return nil, errors.New("the signature isn't valid base64")
	}
	var s struct {
		Magic     [6]byte
		Version   uint32
		PublicKey []byte
		Namespace string
		Reserved  string
		HashAlg   string
		Signature []byte
	}
	if err := ssh.Unmarshal(blob, &s); err != nil || string(s.Magic[:]) != "SSHSIG" || s.Version != 1 {
		return nil, errors.New("the signature can't be read")
	}
	if s.Namespace != namespace {
		return nil, fmt.Errorf("the signature is for %q, not %q", s.Namespace, namespace)
	}
	pub, err := ssh.ParsePublicKey(s.PublicKey)
	if err != nil {
		return nil, errors.New("the signature's key can't be read")
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(s.Signature, &sig); err != nil {
		return nil, errors.New("the signature can't be read")
	}
	var digest []byte
	switch s.HashAlg {
	case "sha256":
		h := sha256.Sum256(message)
		digest = h[:]
	case "sha512":
		h := sha512.Sum512(message)
		digest = h[:]
	default:
		return nil, fmt.Errorf("unknown hash %q", s.HashAlg)
	}
	if err := pub.Verify(signedData(s.Namespace, s.HashAlg, digest), &sig); err != nil {
		return nil, errors.New("the signature doesn't match: the object was changed after signing, or the signature is forged")
	}
	return pub, nil
}

// signedData is what an SSHSIG signature signs.
func signedData(namespace, hashAlg string, digest []byte) []byte {
	var b []byte
	b = append(b, "SSHSIG"...)
	for _, s := range [][]byte{[]byte(namespace), nil, []byte(hashAlg), digest} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
		b = append(b, s...)
	}
	return b
}
