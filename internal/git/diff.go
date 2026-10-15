package git

import (
	"bufio"
	"context"
	"io"
	"strconv"
	"strings"
)

// Limits that keep huge commits from producing multi-megabyte pages.
const (
	maxDiffBytes     = 8 << 20
	maxDiffFileLines = 5000
)

type FileDiff struct {
	OldPath, NewPath string
	Status           string // A, D, M, R or C
	OldMode, NewMode string
	Similarity       string
	Binary           bool
	Added, Deleted   int
	Hunks            []*Hunk
	Truncated        bool
}

func (f *FileDiff) Path() string {
	if f.Status == "D" {
		return f.OldPath
	}
	return f.NewPath
}

type Hunk struct {
	Header string
	Lines  []DiffLine
}

type DiffLine struct {
	Kind         byte // ' ', '+', '-' or '\\'
	Text         string
	OldNo, NewNo int
}

type Diff struct {
	Files          []*FileDiff
	Added, Deleted int
	Truncated      bool
}

// Diff returns the changes introduced by c relative to its first parent.
func (r *Repo) Diff(ctx context.Context, c *Commit) (*Diff, error) {
	return r.DiffBetween(ctx, c.FirstParent(), c.Hash)
}

// FirstParent is the commit c follows on its branch, or "" for a root
// commit.
func (c *Commit) FirstParent() string {
	if len(c.Parents) == 0 {
		return ""
	}
	return c.Parents[0]
}

// diffArgs runs diff-tree from commit from to commit to. With from empty,
// to's whole tree counts as added, as for a root commit.
func diffArgs(from, to string) []string {
	args := []string{"diff-tree", "-p", "-M", "--no-commit-id", "--root", "--no-textconv", "--no-ext-diff"}
	if from != "" {
		args = append(args, from)
	}
	return append(args, to)
}

// DiffBetween returns the changes from commit from to commit to (see
// diffArgs).
func (r *Repo) DiffBetween(ctx context.Context, from, to string) (*Diff, error) {
	cmd := r.cmd(ctx, diffArgs(from, to)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lr := &io.LimitedReader{R: stdout, N: maxDiffBytes}
	d := parseDiff(lr)
	if lr.N <= 0 {
		d.Truncated = true
	}
	cmd.Process.Kill()
	cmd.Wait()
	return d, nil
}

// WriteDiff streams the changes from commit from to commit to (see
// diffArgs) as a plain unified diff.
func (r *Repo) WriteDiff(ctx context.Context, w io.Writer, from, to string) error {
	cmd := r.cmd(ctx, diffArgs(from, to)...)
	cmd.Stdout = w
	return cmd.Run()
}

func parseDiff(rd io.Reader) *Diff {
	d := &Diff{}
	br := bufio.NewReader(rd)
	var f *FileDiff
	var h *Hunk
	var oldNo, newNo, fileLines int
	for {
		line, err := br.ReadString('\n')
		if line == "" && err != nil {
			break
		}
		line = strings.TrimSuffix(line, "\n")

		if strings.HasPrefix(line, "diff --git ") {
			f = &FileDiff{Status: "M"}
			f.OldPath, f.NewPath = splitDiffHeader(strings.TrimPrefix(line, "diff --git "))
			d.Files = append(d.Files, f)
			h, fileLines = nil, 0
			continue
		}
		if f == nil {
			continue
		}
		if h == nil {
			switch {
			case strings.HasPrefix(line, "new file mode "):
				f.Status, f.NewMode = "A", strings.TrimPrefix(line, "new file mode ")
			case strings.HasPrefix(line, "deleted file mode "):
				f.Status, f.OldMode = "D", strings.TrimPrefix(line, "deleted file mode ")
			case strings.HasPrefix(line, "old mode "):
				f.OldMode = strings.TrimPrefix(line, "old mode ")
			case strings.HasPrefix(line, "new mode "):
				f.NewMode = strings.TrimPrefix(line, "new mode ")
			case strings.HasPrefix(line, "similarity index "):
				f.Similarity = strings.TrimPrefix(line, "similarity index ")
			case strings.HasPrefix(line, "rename from "):
				f.Status, f.OldPath = "R", unquote(strings.TrimPrefix(line, "rename from "))
			case strings.HasPrefix(line, "rename to "):
				f.NewPath = unquote(strings.TrimPrefix(line, "rename to "))
			case strings.HasPrefix(line, "copy from "):
				f.Status, f.OldPath = "C", unquote(strings.TrimPrefix(line, "copy from "))
			case strings.HasPrefix(line, "copy to "):
				f.NewPath = unquote(strings.TrimPrefix(line, "copy to "))
			case strings.HasPrefix(line, "Binary files "):
				f.Binary = true
			case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
				// Paths are already known from the header lines.
			}
		}
		if strings.HasPrefix(line, "@@ ") {
			h = &Hunk{Header: line}
			oldNo, newNo = parseHunkHeader(line)
			if !f.Truncated {
				f.Hunks = append(f.Hunks, h)
			}
			continue
		}
		if h == nil {
			continue
		}
		kind := byte(' ')
		if len(line) > 0 {
			kind = line[0]
		}
		dl := DiffLine{Kind: kind}
		if len(line) > 0 {
			dl.Text = line[1:]
		}
		switch kind {
		case '+':
			dl.NewNo = newNo
			newNo++
			f.Added++
			d.Added++
		case '-':
			dl.OldNo = oldNo
			oldNo++
			f.Deleted++
			d.Deleted++
		case '\\':
		default:
			dl.OldNo, dl.NewNo = oldNo, newNo
			oldNo++
			newNo++
		}
		fileLines++
		if fileLines > maxDiffFileLines {
			f.Truncated = true
			continue
		}
		h.Lines = append(h.Lines, dl)
	}
	return d
}

// splitDiffHeader extracts paths from "a/X b/Y". For renames the
// "rename from/to" lines override this, so only the X == Y case matters.
func splitDiffHeader(s string) (string, string) {
	if strings.HasPrefix(s, `"`) {
		// Quoted paths: "a/x" "b/x"
		if i := strings.Index(s, `" "`); i > 0 {
			a, b := unquote(s[:i+1]), unquote(s[i+2:])
			return strings.TrimPrefix(a, "a/"), strings.TrimPrefix(b, "b/")
		}
	}
	n := (len(s) - 5) / 2
	if n > 0 && len(s) == 2*n+5 {
		return s[2 : 2+n], s[len(s)-n:]
	}
	a, b, _ := strings.Cut(s, " b/")
	return strings.TrimPrefix(a, "a/"), b
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

func parseHunkHeader(s string) (oldStart, newStart int) {
	// @@ -a,b +c,d @@
	f := strings.Fields(s)
	if len(f) < 3 {
		return 0, 0
	}
	parse := func(x string) int {
		x = strings.TrimLeft(x, "-+")
		x, _, _ = strings.Cut(x, ",")
		n, _ := strconv.Atoi(x)
		return n
	}
	return parse(f[1]), parse(f[2])
}
