package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"spinney.dev/canopy/internal/config"
	"spinney.dev/canopy/internal/git"
)

func TestActivityGraph(t *testing.T) {
	today := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC) // a Sunday
	start := today.AddDate(0, 0, -7*52-int(today.Weekday()))
	counts := map[time.Time]int{
		today:                    8, // the busiest day
		today.AddDate(0, 0, -1):  1,
		today.AddDate(0, 0, -30): 4,
	}
	a := &activity{}
	a.graph(start, today, counts)

	if len(a.Days) != 7*52+1 {
		t.Fatalf("%d days, want %d", len(a.Days), 7*52+1)
	}
	last := a.Days[len(a.Days)-1]
	if last.Level != 4 || last.Y != graphTop || last.Title != "8 commits on Sun, 11 Oct 2026" {
		t.Errorf("today: %+v", last)
	}
	if d := a.Days[len(a.Days)-2]; d.Level != 1 {
		t.Errorf("1 of 8 should be the lightest shade, got %d", d.Level)
	}
	if d := a.Days[len(a.Days)-31]; d.Level != 2 {
		t.Errorf("4 of 8 should be shade 2, got %d", d.Level)
	}
	if d := a.Days[0]; d.Level != 0 || d.Title != "No commits on Sun, 12 Oct 2025" {
		t.Errorf("first day: %+v", d)
	}
	if a.Width != graphLeft+53*cellStep {
		t.Errorf("width %d, want 53 weeks", a.Width)
	}
	// The first column starts on 12 October 2025, three weeks before
	// November: room enough for its name.
	if len(a.Months) != 13 || a.Months[0].Name != "Oct" || a.Months[1].Name != "Nov" || a.Months[12].Name != "Oct" {
		t.Errorf("months: %+v", a.Months)
	}

	// Starting on 26 October leaves a week of October: no room to name it.
	today = time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	a = &activity{}
	a.graph(today.AddDate(0, 0, -7*52), today, nil)
	if a.Months[0].Name != "Nov" {
		t.Errorf("months: %+v", a.Months)
	}

	// A calendar year: 1 January 2025 was a Wednesday, so the first column
	// starts three squares down.
	a = &activity{}
	a.graph(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC), nil)
	if len(a.Days) != 365 || a.Days[0].X != graphLeft || a.Days[0].Y != graphTop+3*cellStep {
		t.Errorf("2025: %d days, first at %d,%d", len(a.Days), a.Days[0].X, a.Days[0].Y)
	}
	if len(a.Months) != 12 || a.Months[0].Name != "Jan" || a.Width != graphLeft+53*cellStep {
		t.Errorf("2025: months %+v, width %d", a.Months, a.Width)
	}
}

// fakeActivity has n commits a day to repo "busy" from 1 March 2025 on, one
// to "quiet" in 2024, and a tag in 2025.
func fakeActivity(days int) *activityData {
	busy, quiet := &Repo{Name: "busy"}, &Repo{Name: "quiet"}
	data := &activityData{built: time.Now(), years: []int{2025, 2024}}
	for d := days - 1; d >= 0; d-- {
		when := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 0, d)
		for i := range 2 {
			c := &git.Commit{Hash: fmt.Sprintf("%040d", d*2+i), Message: "work"}
			c.Author.When = when.Add(-time.Duration(i) * time.Minute)
			data.commits = append(data.commits, repoCommit{busy, c})
		}
	}
	old := &git.Commit{Hash: "q", Message: "old"}
	old.Author.When = time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	data.commits = append(data.commits, repoCommit{quiet, old})
	data.tags = []activityEvent{{When: time.Date(2025, 3, 3, 18, 0, 0, 0, time.UTC), Repo: busy, Tag: &git.Ref{Name: "v1"}}}
	return data
}

func TestActivityPage(t *testing.T) {
	data := fakeActivity(40)
	a := data.page(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC))
	if a.Total != 80 {
		t.Errorf("total %d, want 80 (2024's commit is another year)", a.Total)
	}
	// Two commits a day to one repository are one event each day, plus
	// the tag.
	if len(a.Events) != 41 || a.Events[0].Count != 2 || a.Events[0].More != 0 {
		t.Errorf("%d events, first %+v", len(a.Events), a.Events[0])
	}
	if len(a.Repos) != 1 || a.Repos[0].Repo.Name != "busy" || a.Repos[0].Count != 80 {
		t.Errorf("repos: %+v", a.Repos)
	}
}

func TestActivityHandler(t *testing.T) {
	cfg := config.Default()
	cfg.Root = t.TempDir()
	cfg.Activity.Enabled = true
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s.activityCache = fakeActivity(40)
	get := func(url string) (int, string) {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w.Code, w.Body.String()
	}
	for url, want := range map[string]int{
		"/activity":                  200,
		"/activity?year=2025":        200,
		"/activity?year=2025&page=2": 200,
		"/activity?year=2025&page=5": 200,
		"/activity?year=2025&page=6": 404, // 41 events: five pages
		"/activity?year=2023":        404, // no commits that year
		"/activity?year=x":           404,
		"/activity?page=0":           404,
		"/activity?year=2024":        200,
	} {
		if code, _ := get(url); code != want {
			t.Errorf("%s: %d, want %d", url, code, want)
		}
	}
	_, body := get("/activity?year=2025")
	for _, want := range []string{"80 commits in 2025", `href="/activity?year=2025&amp;page=2"`, "page 1 of 5", `class="active" href="/activity?year=2025"`} {
		if !strings.Contains(body, want) {
			t.Errorf("year page lacks %q", want)
		}
	}
	if n := strings.Count(body, `<div class="event">`); n != activityEvents {
		t.Errorf("first page has %d events, want %d", n, activityEvents)
	}
	_, body = get("/activity?year=2025&page=5")
	if !strings.Contains(body, `href="/activity?year=2025&amp;page=4"`) || strings.Contains(body, "older") ||
		strings.Count(body, `<div class="event">`) != 1 {
		t.Errorf("last page: want its one event, a newer link to page 4 and no older link")
	}
}
