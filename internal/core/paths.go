package core

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"trellis/internal/model"
)

// SetPaths declares which repo-relative files/folders realize a story. Paths
// are metadata: they never touch the content hash, so pointer maintenance
// cannot invalidate approvals. An empty list clears the declaration.
func (e *Engine) SetPaths(storyID string, paths []string) ([]string, error) {
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

// missingPaths returns the declared paths of a story that do not exist in the
// repo — the finish gate over spec-to-code pointers.
func (e *Engine) missingPaths(story model.Node) []string {
	var missing []string
	for _, p := range story.Paths {
		if _, err := os.Stat(filepath.Join(e.Project.RepoPath, filepath.FromSlash(p))); err != nil {
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
