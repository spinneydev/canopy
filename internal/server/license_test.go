package server

import (
	"os"
	"slices"
	"testing"
)

func TestIsLicenseFile(t *testing.T) {
	for name, want := range map[string]bool{
		"LICENSE": true, "LICENSE.md": true, "licence.txt": true, "COPYING": true,
		"COPYING.LESSER": true, "LICENSE-MIT": true, "LICENSE_APACHE": true, "UNLICENSE": true,
		"LICENSES": false, "licensecheck.go": false, "README": false, "COPYRIGHT": false,
	} {
		if got := isLicenseFile(name); got != want {
			t.Errorf("isLicenseFile(%q) = %v, want %v", name, got, want)
		}
	}
}

const mitText = `MIT License

Copyright (c) 2026 Someone

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
`

func TestIdentify(t *testing.T) {
	gpl, err := os.ReadFile("../../LICENSE")
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		text string
		want []string
	}{
		"mit": {mitText, []string{"MIT"}},
		// The GPL's own sample notice must not show up as a second license.
		"gpl":    {string(gpl), []string{"GPL-3.0"}},
		"custom": {"You may use this for anything except selling it.\n", nil},
		"mostly custom": {mitText +
			"\nAdditionally, you may not use this software for any commercial purpose, nor\n" +
			"may you run it on a server that others can reach, nor may you give copies to\n" +
			"anyone without first asking the author in writing, and you must send the author\n" +
			"a postcard every year that you keep a copy, and these terms override the above\n" +
			"permission in every case where the two disagree, and the author may change\n" +
			"these terms at any time without notice, and you agree to every change by\n" +
			"continuing to keep a copy, and these terms survive the end of this license.\n", nil},
	} {
		if got := identify([]byte(c.text)); !slices.Equal(got, c.want) {
			t.Errorf("%s: identify = %v, want %v", name, got, c.want)
		}
	}
}
