package core

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"trellis/internal/store"

	"trellis/internal/model"
)

// SetPaths declares which repo-relative files/folders realize a story. Paths
// are metadata: they never touch the content hash, so pointer maintenance
// cannot invalidate approvals. An empty list clears the declaration.
func (e *Engine) setPathsUnlocked(storyID string, paths []string) ([]string, error) {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return nil, err
	}
	if story.Kind != model.KindStory {
		return nil, fmt.Errorf("%s is a %s; paths are declared on stories", storyID, story.Kind)
	}
	cleaned := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		c, err := cleanRepoPath(p)
		if err != nil {
			return nil, err
		}
		if !seen[c] {
			seen[c] = true
			cleaned = append(cleaned, c)
		}
	}
	if err := e.st.SetNodePaths(e.pid(), storyID, cleaned); err != nil {
		return nil, err
	}
	e.st.AppendEvent(e.pid(), "paths", storyID, strings.Join(cleaned, ", "))
	return cleaned, nil
}

func cleanRepoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be repo-relative, not absolute", p)
	}
	c := path.Clean(filepath.ToSlash(p))
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("path %q escapes the repository", p)
	}
	return c, nil
}

// missingPathsIn returns the declared paths of a story that do not exist
// under root — the finish gate over spec-to-code pointers, evaluated inside
// the story worktree.
func (e *Engine) missingPathsIn(story model.Node, root string) []string {
	var missing []string
	for _, p := range story.Paths {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
			missing = append(missing, p)
		}
	}
	return missing
}

// PathCovers reports whether a declared path covers the given file: exact
// match, or the declared path is a folder prefix ("api" covers "api/x.go" but
// not "apix.go").
func PathCovers(declared, file string) bool {
	return declared == file || strings.HasPrefix(file, declared+"/")
}

// StoriesForPath returns every story whose declared paths cover the file.
func (e *Engine) StoriesForPath(file string) ([]StorySummary, error) {
	c, err := cleanRepoPath(file)
	if err != nil {
		return nil, err
	}
	stories, err := e.st.ListNodesByKind(e.pid(), model.KindStory)
	if err != nil {
		return nil, err
	}
	out := []StorySummary{}
	for _, s := range stories {
		for _, declared := range s.Paths {
			if PathCovers(declared, c) {
				out = append(out, StorySummary{ID: s.ID, Title: s.Title, Status: s.Status})
				break
			}
		}
	}
	return out, nil
}

// Glossary limits: entries stay ultra short by construction.
const (
	maxTermLen = 64
	maxDefLen  = 240
)

// DefineTerm creates or updates a glossary entry within the length limits.
func (e *Engine) defineTermUnlocked(term, definition string) error {
	term = strings.TrimSpace(term)
	definition = strings.TrimSpace(definition)
	if term == "" || definition == "" {
		return fmt.Errorf("term and definition must be non-empty")
	}
	if len(term) > maxTermLen {
		return fmt.Errorf("term %q exceeds %d characters — glossary terms stay short", term, maxTermLen)
	}
	if len(definition) > maxDefLen {
		return fmt.Errorf("definition for %q exceeds %d characters (%d) — glossary entries stay ultra short: cut it down", term, maxDefLen, len(definition))
	}
	if err := e.st.DefineTerm(e.pid(), term, definition); err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "glossary", "", "define "+term)
	return nil
}

func (e *Engine) deleteTermUnlocked(term string) error {
	if err := e.st.DeleteTerm(e.pid(), strings.TrimSpace(term)); err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "glossary", "", "delete "+term)
	return nil
}

// Terms returns the full glossary.
func (e *Engine) Terms() ([]store.TermDef, error) {
	return e.st.ListTerms(e.pid())
}

const maxDescriptionLen = 200

// SetDescription stores the GitHub-style one-line project description.
func (e *Engine) setDescriptionUnlocked(desc string) error {
	desc = strings.TrimSpace(desc)
	if len(desc) > maxDescriptionLen {
		return fmt.Errorf("description exceeds %d characters (%d) — one line, GitHub style: cut it down", maxDescriptionLen, len(desc))
	}
	e.Project.Description = desc
	if err := e.st.UpdateProject(e.Project); err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "describe", "", desc)
	return nil
}

func (e *Engine) SetPaths(storyID string, paths []string) ([]string, error) {
	var out []string
	err := e.locked(func() error {
		var err error
		out, err = e.setPathsUnlocked(storyID, paths)
		return err
	})
	return out, err
}

func (e *Engine) DefineTerm(term, definition string) error {
	return e.locked(func() error { return e.defineTermUnlocked(term, definition) })
}

func (e *Engine) DeleteTerm(term string) error {
	return e.locked(func() error { return e.deleteTermUnlocked(term) })
}

func (e *Engine) SetDescription(desc string) error {
	return e.locked(func() error { return e.setDescriptionUnlocked(desc) })
}
