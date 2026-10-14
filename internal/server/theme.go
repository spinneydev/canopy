package server

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"spinney.dev/canopy/internal/config"
	"spinney.dev/canopy/internal/fonts"
	"spinney.dev/canopy/internal/render"
	"spinney.dev/canopy/internal/theme"
)

// themeStyles turns [theme] into two stylesheets: the colours and fonts
// loaded after style.css, so its custom properties win (empty if they
// change nothing), and the syntax highlighting.
func themeStyles(cfg *config.Config) (page, highlight []byte, err error) {
	light, err := theme.Resolve(cfg.Theme.Light, cfg.Themes)
	if err != nil {
		return nil, nil, fmt.Errorf("theme: light: %w", err)
	}
	dark, err := theme.Resolve(cfg.Theme.Dark, cfg.Themes)
	if err != nil {
		return nil, nil, fmt.Errorf("theme: dark: %w", err)
	}
	page = theme.CSS(light, dark, cfg.Theme.Sans, cfg.Theme.Mono)
	highlight = render.CSS(cmp.Or(light.Highlight, theme.DefaultLightHighlight), cmp.Or(dark.Highlight, theme.DefaultDarkHighlight))
	return page, highlight, nil
}

// serveFonts answers /_static/fonts.css and the files under fonts.Path.
// The stylesheet grows as fonts finish downloading, so browsers check it
// each time; the files are named by their content and never change.
func (s *Server) serveFonts(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	if r.URL.Path == "/_static/fonts.css" {
		css := s.fonts.CSS()
		sum := sha256.Sum256(css)
		h.Set("Content-Type", "text/css; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(css))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, fonts.Path)
	b, ok := s.fonts.File(name)
	if !fonts.ValidName(name) || !ok {
		http.NotFound(w, r)
		return
	}
	h.Set("Content-Type", "font/woff2")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	w.Write(b)
}
