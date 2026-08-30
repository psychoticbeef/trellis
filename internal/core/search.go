package core

import (
	"strings"

	"trellis/internal/model"
)

type SearchHit struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Story   string `json:"story,omitempty"` // owning story; empty for cross-cutting
	Snippet string `json:"snippet"`
}

// Search finds specs by full-text match over node titles/bodies and
// acceptance-criterion text, case-insensitively. One hit per node.
func (e *Engine) Search(query string) ([]SearchHit, error) {
	hits := []SearchHit{}
	if strings.TrimSpace(query) == "" {
		return hits, nil
	}
	seen := map[string]bool{}

	nodes, err := e.st.SearchNodes(e.pid(), query)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		hit, err := e.hitFor(n, firstMatch(query, n.Title, n.Body))
		if err != nil {
			return nil, err
		}
		hits = append(hits, hit)
		seen[n.ID] = true
	}

	acs, err := e.st.SearchACs(e.pid(), query)
	if err != nil {
		return nil, err
	}
	for _, ac := range acs {
		if seen[ac.StoryID] {
			continue
		}
		story, err := e.st.GetNode(e.pid(), ac.StoryID)
		if err != nil {
			return nil, err
		}
		hit, err := e.hitFor(story, firstMatch(query, ac.Given, ac.When, ac.Then))
		if err != nil {
			return nil, err
		}
		hit.Snippet = ac.ID + ": " + hit.Snippet
		hits = append(hits, hit)
		seen[ac.StoryID] = true
	}
	return hits, nil
}

func (e *Engine) hitFor(n model.Node, snippet string) (SearchHit, error) {
	hit := SearchHit{ID: n.ID, Kind: string(n.Kind), Title: n.Title, Snippet: snippet}
	if story, ok, err := e.storyOf(n); err != nil {
		return hit, err
	} else if ok {
		hit.Story = story.ID
	}
	return hit, nil
}

// firstMatch returns a snippet of ±60 chars around the first case-insensitive
// occurrence of query in any of the fields.
func firstMatch(query string, fields ...string) string {
	lq := strings.ToLower(query)
	for _, f := range fields {
		idx := strings.Index(strings.ToLower(f), lq)
		if idx < 0 {
			continue
		}
		start := idx - 60
		if start < 0 {
			start = 0
		}
		end := idx + len(query) + 60
		if end > len(f) {
			end = len(f)
		}
		snippet := f[start:end]
		if start > 0 {
			snippet = "…" + snippet
		}
		if end < len(f) {
			snippet += "…"
		}
		return snippet
	}
	return ""
}
