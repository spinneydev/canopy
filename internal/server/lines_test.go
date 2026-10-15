package server

import (
	"html/template"
	"net/url"
	"testing"
)

func TestLineSelection(t *testing.T) {
	for _, c := range []struct {
		v          string
		start, end int
	}{
		{"", 0, 0}, {"x", 0, 0}, {"3", 3, 3}, {"3-5", 3, 5}, {"5-3", 3, 5},
		{"0", 0, 0}, {"11", 0, 0}, {"8-20", 8, 10}, {"3-x", 0, 0},
	} {
		if s, e := selection(c.v, 10); s != c.start || e != c.end {
			t.Errorf("selection(%q) = %d-%d, want %d-%d", c.v, s, e, c.start, c.end)
		}
	}

	code := make([]template.HTML, 6)
	links := func(raw string) []codeLine {
		u, _ := url.Parse(raw)
		return codeLines(u, code)
	}
	// Nothing selected: a number selects its line.
	if got := links("/r/tree/main/item/a.c")[3].Href; got != "./a.c?lines=4#L4" {
		t.Errorf("unselected link = %q", got)
	}
	// One line: another extends to it, its own clears it, and other
	// parameters stay.
	ls := links("/r/tree/main/item/a.md?plain=1&lines=4")
	for i, want := range map[int]string{
		1: "./a.md?lines=2-4&plain=1#L2",
		3: "./a.md?plain=1#L4",
		5: "./a.md?lines=4-6&plain=1#L4",
	} {
		if ls[i].Href != want {
			t.Errorf("line %d link = %q, want %q", i+1, ls[i].Href, want)
		}
	}
	// A range: numbers start again, and the range is marked.
	ls = links("/r/tree/main/item/a.c?lines=2-3")
	if ls[4].Href != "./a.c?lines=5#L5" || ls[0].Sel || !ls[1].Sel || !ls[2].Sel || ls[3].Sel {
		t.Errorf("range links = %+v", ls)
	}
}
