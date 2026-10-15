package server

import (
	"context"
	"slices"
	"strconv"

	"spinney.dev/canopy/internal/git"
)

// The log's commit graph, drawn without JavaScript. Each line of history
// runs down a lane, a column beside the commits: a commit's row holds its
// dot and straight lines for the lanes passing it (CSS backgrounds, so they
// stretch with the row), and the gap between two rows is a small SVG in
// which lines bend from lane to lane, where branches fork and merge.
const (
	laneWidth    = 12   // px, as .lane in style.css
	bendHeight   = 12   // px, as .bend in style.css
	laneColors   = 4    // .lane-0 to .lane-3 in style.css
	maxLanes     = 16   // lanes past this aren't drawn
	maxGraphSkip = 5000 // deeper log pages are shown without a graph
)

// logGraph is the graph beside a page of the log: a row per commit, and
// the lines into the next page's first commit, if there is one.
type logGraph struct {
	Rows  []graphRow
	Below *bend
}

type graphRow struct {
	Above *bend    // the lines from the commit before; nil above the very first
	Cells []string // per lane, its classes in style.css
}

type bend struct {
	Width int // px
	Lines []bendLine
}

// bendLine runs from lane centre X1 at the top to X2 at the bottom (px).
type bendLine struct {
	X1, X2, Color int
}

// lanes lays the graph out one commit at a time, in log order.
type lanes struct {
	want   []string // per lane, the commit its line runs down to; "" if free
	color  []int
	from   []int    // per lane, where its line starts at the top of the next bend
	joins  [][2]int // lines from the last commit into lanes already open: {from, into}
	colors int
	seen   map[string]bool
}

// step places commit c, with parents, below the commits before it. It
// returns the lines from the previous commit's row down to c's, and c's row.
func (l *lanes) step(c string, parents []string) (above []bendLine, cells []string) {
	me := slices.Index(l.want, c)
	in := me >= 0
	if !in {
		me = l.free() // a branch's newest commit starts a line
		l.color[me] = l.newColor()
	}

	// Lines waiting for c bend into its lane; the others go straight on.
	into := func(k int) int {
		if l.want[k] == c {
			return me
		}
		return k
	}
	for k, h := range l.want {
		if h != "" {
			above = append(above, bendLine{x(l.from[k]), x(into(k)), l.color[k]})
		}
	}
	for _, j := range l.joins {
		above = append(above, bendLine{x(j[0]), x(into(j[1])), l.color[j[1]]})
	}

	cells = make([]string, max(len(l.want), me+1))
	for k, h := range l.want {
		if h != "" && h != c {
			cells[k] = "pass " + colorClass(l.color[k])
		}
		if h == c {
			l.want[k] = ""
		}
	}

	// The first parent carries c's line on; each other one starts a line
	// of its own, or joins the line already waiting for it. A parent shown
	// already (dates out of order) gets none, rather than one never ending.
	l.seen[c] = true
	for k := range l.from {
		l.from[k] = k
	}
	l.joins = nil
	if len(parents) > 0 && !l.seen[parents[0]] {
		l.want[me] = parents[0]
	}
	for _, p := range parents[min(1, len(parents)):] {
		if l.seen[p] {
			continue
		}
		if k := slices.Index(l.want, p); k >= 0 {
			l.joins = append(l.joins, [2]int{me, k})
			continue
		}
		k := l.free()
		l.want[k], l.color[k], l.from[k] = p, l.newColor(), me
	}

	cell := "node " + colorClass(l.color[me])
	if in {
		cell += " in"
	}
	if l.want[me] != "" {
		cell += " out"
	}
	if len(parents) > 1 {
		cell += " merge"
	}
	cells[me] = cell

	for len(l.want) > 0 && l.want[len(l.want)-1] == "" {
		n := len(l.want) - 1
		l.want, l.color, l.from = l.want[:n], l.color[:n], l.from[:n]
	}
	return above, cells
}

// free returns the leftmost free lane, adding one if all are taken.
func (l *lanes) free() int {
	if k := slices.Index(l.want, ""); k >= 0 {
		l.from[k] = k
		return k
	}
	l.want = append(l.want, "")
	l.color = append(l.color, 0)
	l.from = append(l.from, len(l.from))
	return len(l.want) - 1
}

func (l *lanes) newColor() int {
	l.colors++
	return (l.colors - 1) % laneColors
}

func colorClass(c int) string { return "lane-" + strconv.Itoa(c) }

// x is the centre of lane k, in px from the graph's left edge.
func x(k int) int { return k*laneWidth + laneWidth/2 }

// graph lays out the graph beside a page of tip's log: commits are the
// page's, which come after skip others, followed by the next page's first
// when there is one. It returns nil when the page is too deep to draw.
func graph(ctx context.Context, repo *Repo, tip string, skip int, commits []*git.Commit) *logGraph {
	if skip > maxGraphSkip {
		return nil
	}
	l := &lanes{seen: map[string]bool{}}
	if skip > 0 {
		// The lanes at the top of the page are where the pages before
		// left them.
		earlier, err := repo.Parents(ctx, tip, skip)
		if err != nil {
			return nil
		}
		for _, c := range earlier {
			l.step(c[0], c[1:])
		}
	}

	g := &logGraph{}
	var bends []*bend
	width := 0
	for i, c := range commits {
		above, cells := l.step(c.Hash, c.Parents)
		for _, b := range above {
			width = max(width, b.X1/laneWidth+1, b.X2/laneWidth+1)
		}
		var b *bend
		if i > 0 || skip > 0 {
			b = &bend{Lines: above}
			bends = append(bends, b)
		}
		if i == logPageSize {
			g.Below = b
			break
		}
		width = max(width, len(cells))
		g.Rows = append(g.Rows, graphRow{Above: b, Cells: cells})
	}

	// Every row is as wide as the widest, so the commits line up.
	width = min(width, maxLanes)
	for i, row := range g.Rows {
		cells := row.Cells[:min(len(row.Cells), width)]
		g.Rows[i].Cells = append(cells, make([]string, width-len(cells))...)
	}
	for _, b := range bends {
		b.Width = width * laneWidth
		b.Lines = slices.DeleteFunc(b.Lines, func(line bendLine) bool {
			return line.X1 >= b.Width || line.X2 >= b.Width
		})
	}
	return g
}
