// Package theme holds the site's colour themes: the built-in ones, the
// checks for themes written in the config, and the stylesheet they become.
// A theme only sets colours (style.css's custom properties) and a syntax
// highlighting style; nothing else of the look can change.
package theme

import (
	"bytes"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/alecthomas/chroma/v2/styles"
)

// Colors are the colours a theme sets, the custom properties of style.css
// without their "--".
var Colors = []string{
	"bg", "fg", "muted", "link", "link-hover", "border",
	"header-bg", "event-bg", "code-bg", "btn-bg", "btn-hover", "target",
	"add", "add-bg", "del", "del-bg", "hunk", "branch-bg", "tag-bg",
	"act-0", "act-1", "act-2", "act-3", "act-4",
}

// Palette is a resolved theme.
type Palette struct {
	Colors    map[string]string // colour name → value; unset ones keep style.css's
	Highlight string            // chroma style; "" for the mode's usual one
}

// Default is canopy's own look: style.css as it is.
const Default = "default"

// The highlighting styles of the default theme, per mode.
const (
	DefaultLightHighlight = "github"
	DefaultDarkHighlight  = "github-dark"
)

// Names lists the built-in themes, "default" first.
func Names() []string {
	return append([]string{Default}, slices.Sorted(maps.Keys(builtin))...)
}

// colorValue matches a CSS colour: #hex, a colour function or a name. It
// leaves out anything else CSS could do with the value (url(), var(), ";").
var colorValue = regexp.MustCompile(`^(#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})|(rgba?|hsla?|hwb|lab|lch|oklab|oklch)\([0-9a-z.,%/ +-]*\)|[a-zA-Z]+)$`)

// customName matches the name of a theme written in the config.
var customName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// CheckCustom validates a theme from the config's [themes."name"]: colour
// names with colours, plus an optional "base" (a built-in theme its other
// colours come from) and "highlight" (a syntax highlighting style).
func CheckCustom(name string, def map[string]string) error {
	if !customName.MatchString(name) {
		return fmt.Errorf("%q isn't a theme name like \"my-dark\" (lower case, digits and -)", name)
	}
	if _, ok := builtin[name]; ok || name == Default {
		return fmt.Errorf("%q is a built-in theme; give yours another name and base = %q", name, name)
	}
	for key, value := range def {
		switch {
		case key == "base":
			if _, ok := builtin[value]; !ok && value != Default {
				return fmt.Errorf("%s: base: %q isn't a built-in theme; they are %s", name, value, strings.Join(Names(), ", "))
			}
		case key == "highlight":
			if _, ok := styles.Registry[value]; !ok {
				return fmt.Errorf("%s: highlight: %q isn't a highlighting style; they are %s", name, value, strings.Join(styles.Names(), ", "))
			}
		case !slices.Contains(Colors, key):
			return fmt.Errorf("%s: unknown colour %q; the colours are %s", name, key, strings.Join(Colors, ", "))
		case !colorValue.MatchString(value):
			return fmt.Errorf("%s: %s: %q isn't a colour like \"#0b5bb5\" or \"rgb(11 91 181)\"", name, key, value)
		}
	}
	return nil
}

// Resolve finds a theme by name among the built-in ones and custom (checked
// with CheckCustom). "" is the default theme.
func Resolve(name string, custom map[string]map[string]string) (Palette, error) {
	if name == "" || name == Default {
		return Palette{}, nil
	}
	if p, ok := builtin[name]; ok {
		return p, nil
	}
	def, ok := custom[name]
	if !ok {
		return Palette{}, fmt.Errorf("no theme %q: the built-in ones are %s, or add yours under [themes.%q]", name, strings.Join(Names(), ", "), name)
	}
	base := builtin[def["base"]]
	p := Palette{Colors: maps.Clone(base.Colors), Highlight: base.Highlight}
	if p.Colors == nil {
		p.Colors = map[string]string{}
	}
	for key, value := range def {
		switch key {
		case "base":
		case "highlight":
			p.Highlight = value
		default:
			p.Colors[key] = value
		}
	}
	return p, nil
}

// CSS is the stylesheet loaded after style.css for these themes and fonts
// (Google Fonts families, "" for the system font); empty if it would
// change nothing. The values were checked, so they can't break out of
// their declarations.
func CSS(light, dark Palette, sans, mono string) []byte {
	var buf bytes.Buffer
	var fontVars []string
	if sans != "" {
		fontVars = append(fontVars, fmt.Sprintf("--sans: %q, var(--system-sans);", sans))
	}
	if mono != "" {
		fontVars = append(fontVars, fmt.Sprintf("--mono: %q, var(--system-mono);", mono))
	}
	if len(fontVars) > 0 {
		fmt.Fprintf(&buf, ":root {\n  %s\n}\n", strings.Join(fontVars, "\n  "))
	}
	// The light colours are scoped too: unscoped, they would also beat
	// style.css's dark ones.
	colors := func(media string, set map[string]string) {
		if len(set) == 0 {
			return
		}
		fmt.Fprintf(&buf, "@media %s {\n  :root {\n", media)
		for _, name := range slices.Sorted(maps.Keys(set)) {
			fmt.Fprintf(&buf, "    --%s: %s;\n", name, set[name])
		}
		buf.WriteString("  }\n}\n")
	}
	colors("not all and (prefers-color-scheme: dark)", light.Colors)
	colors("(prefers-color-scheme: dark)", dark.Colors)
	return buf.Bytes()
}
