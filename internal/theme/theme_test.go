package theme

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2/styles"
)

// A built-in theme sets every colour, with a colour, and has a
// highlighting style that exists (not chroma's fallback).
func TestBuiltinThemesAreComplete(t *testing.T) {
	if len(builtin) != 8 {
		t.Errorf("%d built-in themes besides default, want 8", len(builtin))
	}
	for name, p := range builtin {
		if got := slices.Sorted(maps.Keys(p.Colors)); !slices.Equal(got, slices.Sorted(slices.Values(Colors))) {
			t.Errorf("%s sets %v, want every one of %v", name, got, Colors)
		}
		for color, value := range p.Colors {
			if !colorValue.MatchString(value) {
				t.Errorf("%s: %s: %q isn't a colour", name, color, value)
			}
		}
		if _, ok := styles.Registry[p.Highlight]; !ok {
			t.Errorf("%s: no highlighting style %q", name, p.Highlight)
		}
	}
}

func TestCheckCustom(t *testing.T) {
	ok := map[string]string{"base": "night-owl", "highlight": "dracula", "link": "#ff79c6", "bg": "rgb(40 42 54)"}
	if err := CheckCustom("my-dark", ok); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		def  map[string]string
		want string
	}{
		{"My Theme", nil, "isn't a theme name"},
		{"night-owl", nil, "is a built-in theme"},
		{"default", nil, "is a built-in theme"},
		{"x", map[string]string{"base": "x"}, "isn't a built-in theme"},
		{"x", map[string]string{"highlight": "nope"}, "isn't a highlighting style"},
		{"x", map[string]string{"links": "#fff"}, "unknown colour"},
		{"x", map[string]string{"bg": "#fff; } body { x: y"}, "isn't a colour"},
		{"x", map[string]string{"bg": "url(//example.com/x.png)"}, "isn't a colour"},
		{"x", map[string]string{"bg": "var(--fg)"}, "isn't a colour"},
		{"x", map[string]string{"bg": "#12345"}, "isn't a colour"},
		{"x", map[string]string{"bg": "rgb(1 2 3) /* x */"}, "isn't a colour"},
	} {
		err := CheckCustom(c.name, c.def)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: got %v, want an error containing %q", c.name, c.def, err, c.want)
		}
	}
}

func TestResolve(t *testing.T) {
	custom := map[string]map[string]string{
		"mine":  {"base": "night-owl", "link": "#ff0000"},
		"plain": {"link": "#00ff00"},
	}
	p, err := Resolve("mine", custom)
	if err != nil || p.Colors["link"] != "#ff0000" || p.Colors["bg"] != "#011627" || p.Highlight != "night-owl" {
		t.Fatalf("mine: %+v %v", p, err)
	}
	if builtin["night-owl"].Colors["link"] != "#82aaff" {
		t.Fatal("resolving a custom theme changed its base")
	}
	// Without a base, only its own colours change; the rest stay style.css's.
	if p, _ := Resolve("plain", custom); len(p.Colors) != 1 || p.Highlight != "" {
		t.Fatalf("plain: %+v", p)
	}
	if p, err := Resolve("", nil); err != nil || p.Colors != nil {
		t.Fatalf("empty name: %+v %v", p, err)
	}
	if _, err := Resolve("dracula", custom); err == nil || !strings.Contains(err.Error(), "[themes.") {
		t.Fatalf("unknown theme: %v", err)
	}
}

func TestCSS(t *testing.T) {
	if css := CSS(Palette{}, Palette{}, "", ""); len(css) != 0 {
		t.Fatalf("the default theme gave %q", css)
	}
	css := string(CSS(
		Palette{Colors: map[string]string{"link": "#123", "bg": "white"}},
		Palette{Colors: map[string]string{"link": "#456"}},
		"", "JetBrains Mono"))
	for _, want := range []string{
		`--mono: "JetBrains Mono", var(--system-mono);`,
		"@media not all and (prefers-color-scheme: dark) {\n  :root {\n    --bg: white;\n    --link: #123;\n",
		"@media (prefers-color-scheme: dark) {\n  :root {\n    --link: #456;\n",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("missing %q in:\n%s", want, css)
		}
	}
	if strings.Contains(css, "--sans") {
		t.Errorf("sans set without a sans font:\n%s", css)
	}
}
