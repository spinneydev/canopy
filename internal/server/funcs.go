package server

import (
	"fmt"
	"html/template"
	"path"
	"strings"
	"time"
)

var funcs = template.FuncMap{
	"ago":   ago,
	"age":   func(t time.Time) string { return strings.TrimSuffix(ago(t), " ago") },
	"short": func(h string) string { return h[:min(len(h), 8)] },
	"mode":  mode,
	"size":  humanSize,
	"date":  func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 UTC") },
	"add":   func(a, b int) int { return a + b },
	"dict": func(kv ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		return m
	},
	"join":   func(a ...string) string { return strings.Trim(strings.Join(a, "/"), "/") },
	"plural": plural,
	"parent": parent,
	"trimscheme": func(u string) string {
		u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		return strings.TrimSuffix(u, "/")
	},
	"lineclass": func(kind byte) string {
		switch kind {
		case '+':
			return "add"
		case '-':
			return "del"
		case '\\':
			return "nonl"
		}
		return "ctx"
	},
}

func parent(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < -time.Minute:
		return "in the future"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	case d < 30*24*time.Hour:
		return plural(int(d.Hours()/24), "day") + " ago"
	case d < 365*24*time.Hour:
		return plural(int(d.Hours()/24/30), "month") + " ago"
	default:
		return plural(int(d.Hours()/24/365), "year") + " ago"
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// mode renders a git file mode the way ls does.
func mode(m string) string {
	switch m {
	case "040000":
		return "drwxr-xr-x"
	case "100755":
		return "-rwxr-xr-x"
	case "120000":
		return "lrwxrwxrwx"
	case "160000":
		return "m---------"
	default:
		return "-rw-r--r--"
	}
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MiB", float64(n)/1024/1024)
	}
}
