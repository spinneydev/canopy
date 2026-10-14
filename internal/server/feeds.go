package server

import (
	"encoding/xml"
	"net/http"
	"time"
)

// Atom feeds of a repository, for feed readers: /repo/log.atom has the
// newest commits on the default branch, /repo/tags.atom the newest tags.

const feedSize = 20

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Author  atomAuthor  `xml:"author"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}

type atomEntry struct {
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Author  *atomAuthor `xml:"author,omitempty"`
	Link    atomLink    `xml:"link"`
	Content atomContent `xml:"content"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

func atomTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// feed serves the commits ("log") or tags feed of repo.
func (s *Server) feed(w http.ResponseWriter, r *http.Request, repo *Repo, kind string) {
	ctx := r.Context()
	base := s.baseURL(r)
	self := base + repo.URL(kind+".atom")
	f := atomFeed{
		ID:     self,
		Author: atomAuthor{Name: s.cfg.Title},
		Links:  []atomLink{{Href: self, Rel: "self"}, {Href: base + repo.URL()}},
	}
	var newest time.Time
	switch kind {
	case "log":
		ref, hash, err := s.resolve(ctx, repo, "")
		f.Title = repo.Name + ": commits on " + ref
		if err == nil { // an empty repository has an empty feed
			commits, err := repo.Log(ctx, hash, "", feedSize)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			for _, c := range commits {
				link := base + repo.URL("commit", c.Hash)
				f.Entries = append(f.Entries, atomEntry{
					Title: c.Subject(), ID: link, Updated: atomTime(c.Committer.When),
					Author:  &atomAuthor{Name: c.Author.Name},
					Link:    atomLink{Href: link},
					Content: atomContent{Type: "text", Text: c.Message},
				})
				if c.Committer.When.After(newest) {
					newest = c.Committer.When
				}
			}
		}
	case "tags":
		tags, err := repo.Refs(ctx, "refs/tags")
		if err != nil {
			s.fail(w, r, err)
			return
		}
		f.Title = repo.Name + ": tags"
		for _, t := range tags[:min(feedSize, len(tags))] {
			link := base + repo.ItemURL("tree", t.Name, "")
			e := atomEntry{Title: t.Name, ID: link, Updated: atomTime(t.When), Link: atomLink{Href: link}, Content: atomContent{Type: "text"}}
			if t.Annotated {
				e.Content.Text = t.Message
			}
			f.Entries = append(f.Entries, e)
			if t.When.After(newest) {
				newest = t.When
			}
		}
	}
	if newest.IsZero() {
		newest = s.started
	}
	f.Updated = atomTime(newest)

	out, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/atom+xml; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=300")
	w.Write([]byte(xml.Header))
	w.Write(out)
}
