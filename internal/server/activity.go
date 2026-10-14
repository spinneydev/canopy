package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"spinney.dev/canopy/internal/git"
)

// The activity tab ([activity] in the config): commits as a GitHub-style
// graph of days, for the last year or any calendar year with commits, the
// repositories they went to, and the commits and tags of that time, a page
// at a time. Mirrors are left out; they are other people's work. Days are
// UTC days, like the dates on every other page.

const (
	activityTTL      = time.Minute
	activityPerRepo  = 10000 // commits read from one repository
	activityEvents   = 10    // events per page
	activityRepos    = 10
	eventCommitLimit = 5 // commits listed in one event; the rest are counted

	// The graph, in pixels: squares of cell with gap between them, below the
	// month names and right of the day names.
	cell      = 11
	cellStep  = cell + 3
	graphLeft = 30
	graphTop  = 18
)

type activityDay struct {
	X, Y  int
	Level int // shade from 0 (no commits) to 4
	Title string
}

type activityLabel struct {
	Name string
	X, Y int
}

type activityRepo struct {
	Repo  *Repo
	Count int
}

// activityEvent is a tag, or a run of commits to one repository on one day.
type activityEvent struct {
	When    time.Time
	Repo    *Repo
	Tag     *git.Ref
	Commits []*git.Commit // the newest eventCommitLimit of the run
	Count   int           // commits in the run
	More    int           // those not in Commits
}

type repoCommit struct {
	repo *Repo
	c    *git.Commit
}

// activityData is what every activity page is cut from, read from the
// repositories at most once a minute.
type activityData struct {
	commits []repoCommit    // the counted ones, newest first
	tags    []activityEvent // newest first
	years   []int           // years with commits, newest first
	built   time.Time
}

// activityYear links to one year's page, or to the last year's (Year 0).
type activityYear struct {
	Year   int
	URL    string
	Active bool
}

// activity is one page: a period's graph, repositories and events.
type activity struct {
	Period        string // "the last year", or a year
	Total         int
	Days          []activityDay
	Months, Wdays []activityLabel
	Width, Height int
	Repos         []activityRepo
	Events        []activityEvent
	Years         []activityYear
	Page, Pages   int
	Newer, Older  string // links to the neighbouring pages
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	data := s.activitySource()
	today := utcDay(time.Now())
	q := r.URL.Query()

	// The last year by default: 53 columns of weeks, the last holding today.
	year := 0
	from, to := today.AddDate(0, 0, -7*52-int(today.Weekday())), today
	base := "/activity"
	if y := q.Get("year"); y != "" {
		n, err := strconv.Atoi(y)
		if err != nil || !slices.Contains(data.years, n) {
			s.notFound(w, r)
			return
		}
		year = n
		from = time.Date(n, 1, 1, 0, 0, 0, 0, time.UTC)
		to = time.Date(n, 12, 31, 0, 0, 0, 0, time.UTC)
		if to.After(today) {
			to = today
		}
		base += "?year=" + y
	}
	a := data.page(from, to)
	a.Period = "the last year"
	if year != 0 {
		a.Period = strconv.Itoa(year)
	}
	a.Years = append(a.Years, activityYear{URL: "/activity", Active: year == 0})
	for _, y := range data.years {
		a.Years = append(a.Years, activityYear{Year: y, URL: "/activity?year=" + strconv.Itoa(y), Active: y == year})
	}

	// Events newest first, a page at a time: ?page=2 and on.
	a.Pages = max(1, (len(a.Events)+activityEvents-1)/activityEvents)
	a.Page = 1
	if p := q.Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > a.Pages {
			s.notFound(w, r)
			return
		}
		a.Page = n
	}
	sep := "?"
	if year != 0 {
		sep = "&"
	}
	pageURL := func(n int) string {
		if n == 1 {
			return base
		}
		return base + sep + "page=" + strconv.Itoa(n)
	}
	if a.Page > 1 {
		a.Newer = pageURL(a.Page - 1)
	}
	if a.Page < a.Pages {
		a.Older = pageURL(a.Page + 1)
	}
	start := (a.Page - 1) * activityEvents
	a.Events = a.Events[start:min(start+activityEvents, len(a.Events))]

	s.render(w, r, http.StatusOK, "activity", map[string]any{
		"Title": "activity", "Tab": "activity", "A": a,
	})
}

// activitySource returns the cached activityData, reading the repositories
// again when it is older than activityTTL.
func (s *Server) activitySource() *activityData {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.activityCache == nil || time.Since(s.activityCache.built) > activityTTL {
		// Not tied to a request: a visitor who leaves early shouldn't leave
		// half the repositories out of the cache.
		ctx, cancel := context.WithTimeout(context.Background(), pageTimeout)
		s.activityCache = s.readActivity(ctx)
		cancel()
	}
	return s.activityCache
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// mine reports whether c counts: everyone's commits do unless [activity]
// emails names the authors.
func (s *Server) mine(c *git.Commit) bool {
	emails := s.cfg.Activity.Emails
	return len(emails) == 0 || slices.ContainsFunc(emails, func(e string) bool { return strings.EqualFold(e, c.Author.Email) })
}

func (s *Server) readActivity(ctx context.Context) *activityData {
	data := &activityData{built: time.Now()}
	years := map[int]bool{}
	_, list := s.repos.get()
	for _, repo := range list {
		if repo.Upstream != "" || repo.Updated.IsZero() {
			continue // a mirror, or no branches yet
		}
		cs, err := repo.Since(ctx, time.Time{}, activityPerRepo)
		if err != nil {
			log.Printf("activity: %s: %v", repo.Name, err)
			continue
		}
		for _, c := range cs {
			if s.mine(c) {
				data.commits = append(data.commits, repoCommit{repo, c})
				years[c.Author.When.UTC().Year()] = true
			}
		}
		refs, err := repo.Refs(ctx, "refs/tags")
		if err != nil {
			log.Printf("activity: %s: %v", repo.Name, err)
		}
		for i := range refs {
			data.tags = append(data.tags, activityEvent{When: refs[i].When, Repo: repo, Tag: &refs[i]})
		}
	}
	// Commits are dated by when they were written, as on GitHub, not by
	// when they were committed (the order git log gives).
	sort.SliceStable(data.commits, func(i, j int) bool {
		return data.commits[i].c.Author.When.After(data.commits[j].c.Author.When)
	})
	sort.SliceStable(data.tags, func(i, j int) bool { return data.tags[i].When.After(data.tags[j].When) })
	for y := range years {
		data.years = append(data.years, y)
	}
	slices.SortFunc(data.years, func(a, b int) int { return b - a })
	return data
}

// page gathers the days from through to: their graph, the repositories with
// the most commits, and all their events, newest first.
func (data *activityData) page(from, to time.Time) *activity {
	a := &activity{}
	in := func(t time.Time) bool { d := utcDay(t); return !d.Before(from) && !d.After(to) }
	counts := map[time.Time]int{}
	perRepo := map[*Repo]int{}
	for _, rc := range data.commits {
		if !in(rc.c.Author.When) {
			continue
		}
		counts[utcDay(rc.c.Author.When)]++
		perRepo[rc.repo]++
		a.Total++
		// Commits in a row to the same repository on the same day are one
		// event.
		if n := len(a.Events); n > 0 {
			last := &a.Events[n-1]
			if last.Repo == rc.repo && utcDay(last.When).Equal(utcDay(rc.c.Author.When)) {
				if len(last.Commits) < eventCommitLimit {
					last.Commits = append(last.Commits, rc.c)
				}
				last.Count++
				continue
			}
		}
		a.Events = append(a.Events, activityEvent{When: rc.c.Author.When, Repo: rc.repo, Commits: []*git.Commit{rc.c}, Count: 1})
	}
	for _, t := range data.tags {
		if in(t.When) {
			a.Events = append(a.Events, t)
		}
	}
	sort.SliceStable(a.Events, func(i, j int) bool { return a.Events[i].When.After(a.Events[j].When) })
	for i := range a.Events {
		a.Events[i].More = a.Events[i].Count - len(a.Events[i].Commits)
	}

	for repo, n := range perRepo {
		a.Repos = append(a.Repos, activityRepo{repo, n})
	}
	sort.Slice(a.Repos, func(i, j int) bool {
		if a.Repos[i].Count != a.Repos[j].Count {
			return a.Repos[i].Count > a.Repos[j].Count
		}
		return a.Repos[i].Repo.Name < a.Repos[j].Repo.Name
	})
	a.Repos = a.Repos[:min(activityRepos, len(a.Repos))]

	a.graph(from, to, counts)
	return a
}

// graph lays out one square per day from from to to, in columns of weeks
// starting on Sunday, shaded relative to the busiest day, with month names
// over the weeks they begin in.
func (a *activity) graph(from, to time.Time, counts map[time.Time]int) {
	busiest := 0
	for _, n := range counts {
		busiest = max(busiest, n)
	}
	origin := from.AddDate(0, 0, -int(from.Weekday())) // the first column's Sunday
	last := 0
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		i := int(d.Sub(origin).Hours() / 24)
		n := counts[d]
		level := 0
		title := "No commits"
		if n > 0 {
			level = (4*n + busiest - 1) / busiest // 1 to 4, rounded up
			title = plural(n, "commit")
		}
		a.Days = append(a.Days, activityDay{
			X:     graphLeft + i/7*cellStep,
			Y:     graphTop + i%7*cellStep,
			Level: level,
			Title: fmt.Sprintf("%s on %s", title, d.Format("Mon, 2 Jan 2006")),
		})
		last = i
	}
	weeks := last/7 + 1
	a.Width = graphLeft + weeks*cellStep
	a.Height = graphTop + 7*cellStep

	prev := time.Month(0)
	for w := range weeks {
		d := origin.AddDate(0, 0, 7*w)
		if d.Before(from) {
			d = from
		}
		if d.Month() == prev {
			continue
		}
		prev = d.Month()
		// A month that only has its last days in the first column would
		// crowd the next name.
		if w == 0 && d.AddDate(0, 0, 14).Month() != d.Month() {
			continue
		}
		a.Months = append(a.Months, activityLabel{Name: d.Format("Jan"), X: graphLeft + w*cellStep, Y: graphTop - 6})
	}
	for _, wd := range []time.Weekday{time.Monday, time.Wednesday, time.Friday} {
		a.Wdays = append(a.Wdays, activityLabel{Name: wd.String()[:3], X: 0, Y: graphTop + int(wd)*cellStep + cell - 1})
	}
}
