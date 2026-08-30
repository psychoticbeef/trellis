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

// Search finds specs via the FTS5 index: every whitespace-separated term must
// match (the last one as a word prefix), results are ordered by BM25
// relevance. AC text counts toward its story.
func (e *Engine) Search(query string) ([]SearchHit, error) {
	hits := []SearchHit{}
	if strings.TrimSpace(query) == "" {
		return hits, nil
	}
	raw, err := e.st.SearchFTS(e.pid(), query)
	if err != nil {
		return nil, err
	}
	for _, r := range raw {
		n, err := e.st.GetNode(e.pid(), r.NodeID)
		if err != nil {
			continue // index row for a concurrently deleted node
		}
		hit := SearchHit{ID: n.ID, Kind: string(n.Kind), Title: n.Title, Snippet: r.Snippet}
		if story, ok, err := e.storyOf(n); err != nil {
			return nil, err
		} else if ok {
			hit.Story = story.ID
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

var _ = model.KindStory // keep model import stable for future use
