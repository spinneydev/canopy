package server

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/google/licensecheck"
	"spinney.dev/canopy/internal/git"
)

// maxLicenseBytes is the largest file scanned for a license; the longest
// common ones (the GPLs) are about 35 KB.
const maxLicenseBytes = 128 << 10

// minLicenseCoverage is how much of a file, in percent, has to be known
// license text before we name it. Below that the file has its own terms or
// changes to a standard license, and naming one would mislead.
const minLicenseCoverage = 75

// licenseFile is a license file at the top of a repository.
type licenseFile struct {
	Name string
	IDs  []string // SPDX identifiers; empty if the text isn't recognised
}

// Label is what the summary shows: the licenses, or the file's name.
func (l licenseFile) Label() string {
	if len(l.IDs) == 0 {
		return l.Name
	}
	return strings.Join(l.IDs, ", ")
}

// isLicenseFile reports whether a file name is one licenses are kept in:
// LICENSE, LICENCE, COPYING or UNLICENSE, with an extension or a suffix
// like LICENSE-MIT for dual-licensed projects.
func isLicenseFile(name string) bool {
	lower := strings.ToLower(name)
	for _, base := range []string{"license", "licence", "copying", "unlicense"} {
		if rest, ok := strings.CutPrefix(lower, base); ok && (rest == "" || rest[0] == '.' || rest[0] == '-' || rest[0] == '_') {
			return true
		}
	}
	return false
}

// licenses finds and identifies the license files among a root tree's
// entries.
func licenses(ctx context.Context, repo *Repo, hash string, entries []git.TreeEntry) []licenseFile {
	var out []licenseFile
	for _, e := range entries {
		if e.Type != "blob" || !isLicenseFile(e.Name) {
			continue
		}
		f := licenseFile{Name: e.Name}
		if e.Size <= maxLicenseBytes {
			if content, err := repo.Blob(ctx, hash, e.Name); err == nil {
				f.IDs = identify(content)
			}
		}
		out = append(out, f)
	}
	return out
}

// knownLicenses are the licenses we look for, by SPDX identifier; GNU
// licenses include their -only and -or-later notices. licensecheck knows
// over 500, but a scanner for all of them takes about 100 MB to build and
// keeps 11 MB; one for these is built in a few milliseconds. A file under
// some other license shows its name instead.
var knownLicenses = []string{
	"0BSD", "AGPL-3.0", "Apache-2.0", "Artistic-2.0", "BSD-2-Clause",
	"BSD-2-Clause-Patent", "BSD-3-Clause", "BSD-3-Clause-Clear", "BSL-1.0",
	"CC-BY-4.0", "CC-BY-SA-4.0", "CC0-1.0", "EPL-2.0", "EUPL-1.2", "GPL-2.0",
	"GPL-3.0", "ISC", "LGPL-2.1", "LGPL-3.0", "MIT", "MIT-0", "MPL-2.0",
	"MPL-2.0-no-copyleft-exception", "Unlicense", "WTFPL", "Zlib",
}

var licenseScanner = sync.OnceValue(func() *licensecheck.Scanner {
	var list []licensecheck.License
	for _, l := range licensecheck.BuiltinLicenses() {
		id := strings.TrimSuffix(strings.TrimSuffix(l.ID, "-only"), "-or-later")
		if slices.Contains(knownLicenses, id) {
			list = append(list, l)
		}
	}
	s, err := licensecheck.NewScanner(list)
	if err != nil {
		return nil
	}
	return s
})

// identify returns the SPDX identifiers of the licenses in text, or none
// if too little of it is license text.
func identify(text []byte) []string {
	scanner := licenseScanner()
	if scanner == nil {
		return nil
	}
	cov := scanner.Scan(text)
	if cov.Percent < minLicenseCoverage {
		return nil
	}
	var ids []string
	for _, m := range cov.Match {
		if !m.IsURL && !slices.Contains(ids, m.ID) {
			ids = append(ids, m.ID)
		}
	}
	// GNU's license texts end with a sample notice ("either version 3 of
	// the License, or (at your option) any later version"), which matches
	// on its own. It shows how to apply the license, not which variant the
	// project chose, so it adds nothing to the license it belongs to.
	return slices.DeleteFunc(ids, func(id string) bool {
		for _, suffix := range []string{"-or-later", "-only"} {
			if base, ok := strings.CutSuffix(id, suffix); ok && slices.Contains(ids, base) {
				return true
			}
		}
		return false
	})
}
