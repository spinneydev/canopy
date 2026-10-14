package render

import (
	"strings"
	"testing"
)

var links = Links{TreeRoot: "/repo/tree/main/item", RawRoot: "/repo/blob/main/item", Dir: "docs"}

func TestMarkdownHighlightsFencedCode(t *testing.T) {
	out := string(Markdown([]byte("```go\nfunc main() {}\n```\n"), links))
	if !strings.Contains(out, `<pre class="chroma"><code>`) {
		t.Fatalf("missing chroma wrapper: %s", out)
	}
	if !strings.Contains(out, `<span class="kd">func</span>`) {
		t.Fatalf("keyword not highlighted: %s", out)
	}
}

func TestMarkdownLeavesUnknownLanguagePlain(t *testing.T) {
	for _, src := range []string{"```\nfunc x\n```\n", "```nosuchlang\nfunc x\n```\n"} {
		out := string(Markdown([]byte(src), links))
		if strings.Contains(out, "<span") || !strings.Contains(out, "func x") {
			t.Errorf("%q: want plain code, got %s", src, out)
		}
	}
}

func TestMarkdownEscapesCode(t *testing.T) {
	out := string(Markdown([]byte("```html\n<script>alert(1)</script>\n```\n"), links))
	if strings.Contains(out, "<script>") {
		t.Fatalf("code block was not escaped: %s", out)
	}
}

func TestMarkdownSanitizes(t *testing.T) {
	src := "<script>alert(1)</script>\n\n<span onclick=\"x()\" class=\"chroma-evil\">hi</span>\n\n<a href=\"javascript:alert(1)\">x</a>\n"
	out := string(Markdown([]byte(src), links))
	for _, bad := range []string{"<script", "onclick", "chroma-evil", "javascript:"} {
		if strings.Contains(out, bad) {
			t.Errorf("output contains %q: %s", bad, out)
		}
	}
}

func TestMarkdownRewritesRelativeLinks(t *testing.T) {
	src := "[a](guide.md) [b](/top.md) [c](../up.md) [d](https://example.com) [e](#anchor)\n\n![img](pic.png)\n"
	out := string(Markdown([]byte(src), links))
	for _, want := range []string{
		`href="/repo/tree/main/item/docs/guide.md"`,
		`href="/repo/tree/main/item/top.md"`,
		`href="/repo/tree/main/item/up.md"`,
		`href="https://example.com"`,
		`href="#anchor"`,
		`src="/repo/blob/main/item/docs/pic.png"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestMarkdownRewritesRawHTMLImages(t *testing.T) {
	src := `<img src="anim/shapes.gif" width="250"> <img src="/top.gif"> <img src="https://example.com/x.gif">` + "\n\n![b](/up.gif)\n"
	out := string(Markdown([]byte(src), links))
	for _, want := range []string{
		`src="/repo/blob/main/item/docs/anim/shapes.gif"`,
		`src="/repo/blob/main/item/top.gif"`,
		`src="https://example.com/x.gif"`,
		`src="/repo/blob/main/item/up.gif"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestInline(t *testing.T) {
	for src, want := range map[string]string{
		"":                                 "",
		"plain":                            "plain",
		"for [Alice](https://example.com)": `for <a href="https://example.com" rel="nofollow">Alice</a>`,
		`<a href="javascript:alert(1)">x</a><script>y</script>`: "x",
	} {
		if got := string(Inline(src)); got != want {
			t.Errorf("Inline(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestLinesPicksLexerFromFilename(t *testing.T) {
	lines := Lines("main.go", []byte("package main\n\nfunc main() {}\n"))
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	if !strings.Contains(string(lines[0]), `<span class="kn">package</span>`) {
		t.Fatalf("not highlighted: %s", lines[0])
	}
}
