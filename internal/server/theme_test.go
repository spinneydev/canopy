package server

import (
	"strings"
	"testing"

	"spinney.dev/canopy/internal/theme"
)

// Every colour a theme can set must be one style.css uses, in both modes.
func TestThemeColorsAreInStyleCSS(t *testing.T) {
	css, _ := assets.ReadFile("static/style.css")
	light, dark, _ := strings.Cut(string(css), "@media (prefers-color-scheme: dark)")
	for _, name := range theme.Colors {
		if !strings.Contains(light, "--"+name+":") || !strings.Contains(dark, "--"+name+":") {
			t.Errorf("--%s isn't set in both of style.css's colour sets", name)
		}
	}
}
