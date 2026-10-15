package server

import (
	"slices"
	"strings"
	"testing"
)

// A merge M of A and B, which both come from O:
//
//	M      ●╮
//	B      │●
//	A      ●│
//	O      ●╯
func TestLanes(t *testing.T) {
	l := &lanes{seen: map[string]bool{}}
	type step struct {
		c, parents string
		above      []bendLine
		cells      []string
	}
	for _, s := range []step{
		{"M", "A B", nil, []string{"node lane-0 out merge"}},
		{"B", "O", []bendLine{{6, 6, 0}, {6, 18, 1}}, []string{"pass lane-0", "node lane-1 in out"}},
		{"A", "O", []bendLine{{6, 6, 0}, {18, 18, 1}}, []string{"node lane-0 in out", "pass lane-1"}},
		{"O", "", []bendLine{{6, 6, 0}, {18, 6, 1}}, []string{"node lane-0 in", ""}},
	} {
		above, cells := l.step(s.c, strings.Fields(s.parents))
		if !slices.Equal(above, s.above) || !slices.Equal(cells, s.cells) {
			t.Errorf("%s: lines %v, cells %q; want %v, %q", s.c, above, cells, s.above, s.cells)
		}
	}
	if len(l.want) != 0 {
		t.Errorf("lanes left open: %q", l.want)
	}
}

// A parent the log showed before its child (clock skew) starts no line,
// which would otherwise run on to the end.
func TestLanesSkipParentsAlreadyShown(t *testing.T) {
	l := &lanes{seen: map[string]bool{}}
	l.step("P", nil)
	if _, cells := l.step("C", []string{"P"}); !slices.Equal(cells, []string{"node lane-1"}) || len(l.want) != 0 {
		t.Errorf("cells %q, lanes %q", cells, l.want)
	}
}
