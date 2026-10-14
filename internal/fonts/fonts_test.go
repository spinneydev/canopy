package fonts

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What fonts.googleapis.com/css2 sends a recent browser.
const googleCSS = `/* cyrillic */
@font-face {
  font-family: 'Inter';
  font-style: italic;
  font-weight: 400 700;
  font-display: swap;
  src: url(https://fonts.gstatic.com/s/inter/v20/a.woff2) format('woff2');
  unicode-range: U+0301, U+0400-045F, U+0490-0491;
}
/* latin */
@font-face {
  font-family: 'Inter';
  font-style: normal;
  font-weight: 400 700;
  font-display: swap;
  src: url(https://fonts.gstatic.com/s/inter/v20/b.woff2) format('woff2');
  unicode-range: U+0000-00FF, U+0131, U+FEFF, U+FFFD;
}
`

func TestParse(t *testing.T) {
	faces, err := parse([]byte(googleCSS))
	if err != nil {
		t.Fatal(err)
	}
	if len(faces) != 2 || faces[0] != (face{"italic", "400 700", "U+0301, U+0400-045F, U+0490-0491", "https://fonts.gstatic.com/s/inter/v20/a.woff2"}) {
		t.Fatalf("got %+v", faces)
	}
	var buf bytes.Buffer
	faces[1].write(&buf, "Inter", "0123456789abcdef0123456789abcdef.woff2")
	want := `@font-face {
  font-family: "Inter";
  font-style: normal;
  font-weight: 400 700;
  font-display: swap;
  src: url(/_static/fonts/0123456789abcdef0123456789abcdef.woff2) format("woff2");
  unicode-range: U+0000-00FF, U+0131, U+FEFF, U+FFFD;
}
`
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

// Only woff2 files on fonts.gstatic.com and plain values get through.
func TestParseRefusesOddReplies(t *testing.T) {
	for _, bad := range []string{
		"",
		strings.Replace(googleCSS, "https://fonts.gstatic.com/", "https://example.com/", 1),
		strings.Replace(googleCSS, "a.woff2", "a.ttf", 1),
		strings.Replace(googleCSS, "font-style: italic", "font-style: italic url(x)", 1),
		strings.Replace(googleCSS, "U+0301", "U+0301 /* */", 1),
	} {
		if faces, err := parse([]byte(bad)); err == nil {
			t.Errorf("accepted %+v from:\n%s", faces, bad)
		}
	}
}

func TestCache(t *testing.T) {
	dir := t.TempDir()
	font := []byte("wOF2 not really")
	name := fileName(font)
	var css bytes.Buffer
	face{style: "normal", url: "x"}.write(&css, "Inter", name)
	cssPath := filepath.Join(dir, slug("Inter")+".css")
	if err := writeCache(dir, cssPath, css.Bytes(), map[string][]byte{name: font}); err != nil {
		t.Fatal(err)
	}
	got, files, err := readCache(dir, cssPath)
	if err != nil || !bytes.Equal(got, css.Bytes()) || !bytes.Equal(files[name], font) {
		t.Fatalf("read back %q %v %v", got, files, err)
	}
	// A damaged file means downloading the family again.
	os.WriteFile(filepath.Join(dir, name), []byte("damaged"), 0o644)
	if _, _, err := readCache(dir, cssPath); err == nil {
		t.Fatal("damaged font file accepted")
	}
}
