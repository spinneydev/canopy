// Package render turns repository content into HTML: syntax-highlighted
// source and sanitized markdown.
package render

import (
	"bytes"
	"html"
	"html/template"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// MaxHighlightBytes is the largest file we run through the lexer; bigger
// files are shown as plain text.
const MaxHighlightBytes = 512 << 10

// Lines returns content split into lines of highlighted HTML, picking the
// language from the file name or, failing that, the content.
func Lines(filename string, content []byte) []template.HTML {
	src := normalize(content)
	var lexer chroma.Lexer
	if len(content) <= MaxHighlightBytes {
		lexer = lexers.Match(path.Base(filename))
		if lexer == nil {
			lexer = lexers.Analyse(src)
		}
	}
	return highlight(lexer, src)
}

func normalize(content []byte) string {
	src := strings.ReplaceAll(string(content), "\r\n", "\n")
	return strings.TrimSuffix(src, "\n")
}

// highlight tokenizes src with lexer; a nil lexer means plain text.
func highlight(lexer chroma.Lexer, src string) []template.HTML {
	if lexer == nil {
		return plainLines(src)
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, src+"\n")
	if err != nil {
		return plainLines(src)
	}
	var out []template.HTML
	for _, line := range chroma.SplitTokensIntoLines(it.Tokens()) {
		var b strings.Builder
		for _, tok := range line {
			val := strings.TrimSuffix(tok.Value, "\n")
			if val == "" {
				continue
			}
			if cls := tokenClass(tok.Type); cls != "" {
				b.WriteString(`<span class="` + cls + `">`)
				b.WriteString(html.EscapeString(val))
				b.WriteString(`</span>`)
			} else {
				b.WriteString(html.EscapeString(val))
			}
		}
		out = append(out, template.HTML(b.String()))
	}
	return out
}

func plainLines(src string) []template.HTML {
	parts := strings.Split(src, "\n")
	out := make([]template.HTML, len(parts))
	for i, p := range parts {
		out[i] = template.HTML(html.EscapeString(p))
	}
	return out
}

func tokenClass(t chroma.TokenType) string {
	for _, tt := range []chroma.TokenType{t, t.SubCategory(), t.Category()} {
		if cls, ok := chroma.StandardTypes[tt]; ok && cls != "" {
			return cls
		}
	}
	return ""
}

// CSS returns the highlighting stylesheet: the chroma style named light,
// and dark for prefers-color-scheme: dark. The light rules apply only
// outside dark mode: the dark style leaves some token types (punctuation,
// for one) at the default text colour, so a light rule left in force would
// draw them near-black on the dark background.
func CSS(light, dark string) []byte {
	f := chromahtml.New(chromahtml.WithClasses(true))
	var b bytes.Buffer
	b.WriteString("@media not all and (prefers-color-scheme: dark) {\n")
	f.WriteCSS(&b, styles.Get(light))
	b.WriteString("}\n")
	b.WriteString("\n@media (prefers-color-scheme: dark) {\n")
	f.WriteCSS(&b, styles.Get(dark))
	b.WriteString("}\n")
	// The page provides its own code background.
	b.WriteString(".chroma, .bg { background: transparent !important; }\n")
	return b.Bytes()
}

// newPolicy returns the sanitizer for rendered markdown. It is built per
// document because the policy carries the src rewriter for that document.
func newPolicy(links Links) *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("id").OnElements("h1", "h2", "h3", "h4", "h5", "h6")
	p.AllowAttrs("align").OnElements("p", "div", "img", "h1", "h2", "h3", "td", "th")
	p.AllowAttrs("width", "height").OnElements("img")
	p.AllowElements("details", "summary", "picture", "source")
	p.AllowAttrs("srcset", "media").OnElements("source")
	// Classes emitted by the code block highlighter.
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^[a-z][a-z0-9]{0,3}$`)).OnElements("span")
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^chroma$`)).OnElements("pre")
	// Rewriting src here, after parsing, covers images written as raw HTML
	// as well as markdown ones.
	p.RewriteSrc(func(u *url.URL) {
		if v, err := url.Parse(string(links.rewrite([]byte(u.String()), links.RawRoot))); err == nil {
			*u = *v
		}
	})
	return p
}

// Links tells Markdown where relative links in a document should point.
// Roots are unescaped URL paths; Dir is the document's directory inside
// the repository.
type Links struct {
	TreeRoot string // e.g. /repo/tree/main/item
	RawRoot  string // e.g. /repo/blob/main/item
	Dir      string
}

// Markdown renders a README-style markdown document, rewriting relative
// links and images to point into the repository.
func Markdown(src []byte, links Links) template.HTML {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(&linkRewriter{links}, 100)),
		),
		goldmark.WithRendererOptions(
			gmhtml.WithUnsafe(),
			renderer.WithNodeRenderers(util.Prioritized(codeBlockRenderer{}, 200)),
		),
	)
	var b bytes.Buffer
	if err := md.Convert(src, &b); err != nil {
		return template.HTML("<pre>" + html.EscapeString(string(src)) + "</pre>")
	}
	return template.HTML(newPolicy(links).SanitizeBytes(b.Bytes()))
}

// Inline renders a one-line markdown snippet, such as a description, without
// the paragraph Markdown puts around it, so it can sit inside other markup.
func Inline(src string) template.HTML {
	out := strings.TrimSpace(string(Markdown([]byte(src), Links{})))
	if inner, ok := strings.CutPrefix(out, "<p>"); ok && strings.Count(out, "<p>") == 1 {
		out = strings.TrimSuffix(inner, "</p>")
	}
	return template.HTML(out)
}

// codeBlockRenderer highlights fenced code blocks by their info string
// (```go, ```sh, ...). Blocks without a known language stay plain.
type codeBlockRenderer struct{}

func (codeBlockRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderCodeBlock)
}

func renderCodeBlock(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	block := n.(*ast.FencedCodeBlock)
	var code bytes.Buffer
	for i := 0; i < block.Lines().Len(); i++ {
		seg := block.Lines().At(i)
		code.Write(seg.Value(src))
	}
	var lexer chroma.Lexer
	if lang := string(block.Language(src)); lang != "" && code.Len() <= MaxHighlightBytes {
		lexer = lexers.Get(lang)
	}
	w.WriteString(`<pre class="chroma"><code>`)
	for i, line := range highlight(lexer, normalize(code.Bytes())) {
		if i > 0 {
			w.WriteByte('\n')
		}
		w.WriteString(string(line))
	}
	w.WriteString("</code></pre>\n")
	return ast.WalkSkipChildren, nil
}

type linkRewriter struct{ Links }

func (t *linkRewriter) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Link:
			n.Destination = t.rewrite(n.Destination, t.TreeRoot)
		}
		return ast.WalkContinue, nil
	})
}

func (l Links) rewrite(dest []byte, root string) []byte {
	s := string(dest)
	if s == "" || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "//") {
		return dest
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Path == "" {
		return dest
	}
	// Leading-slash links are repository-root-relative, as on most forges.
	// Joining under "/" first keeps ".." from climbing out of the repo.
	p := u.Path
	if !strings.HasPrefix(p, "/") {
		p = path.Join("/", l.Dir, p)
	}
	u.Path = root + path.Clean("/"+p)
	return []byte(u.String())
}
