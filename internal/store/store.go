// Package store is the persistence layer: SQLite plus an append-only event log.
// It contains no business rules; guards live in package core.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"trellis/internal/model"
)

type Store struct {
	db   *sql.DB
	path string
}

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	repo_path   TEXT NOT NULL DEFAULT '',
	base_branch TEXT NOT NULL DEFAULT 'develop',
	lint_cmd    TEXT NOT NULL DEFAULT '',
	test_cmd    TEXT NOT NULL DEFAULT '',
	junit_glob  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS nodes (
	id            TEXT NOT NULL,
	project_id    TEXT NOT NULL,
	kind          TEXT NOT NULL,
	parent_id     TEXT NOT NULL DEFAULT '',
	title         TEXT NOT NULL,
	body          TEXT NOT NULL DEFAULT '',
	covers        TEXT NOT NULL DEFAULT '[]',
	status        TEXT NOT NULL DEFAULT '',
	position      INTEGER NOT NULL DEFAULT 0,
	activity_id   TEXT NOT NULL DEFAULT '',
	rank          INTEGER NOT NULL DEFAULT 0,
	slice         INTEGER NOT NULL DEFAULT 0,
	approved_content_hash TEXT NOT NULL DEFAULT '',
	approved_parent_hash  TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL,
	PRIMARY KEY (project_id, id)
);
CREATE TABLE IF NOT EXISTS acceptance_criteria (
	id        TEXT NOT NULL,
	project_id TEXT NOT NULL,
	story_id  TEXT NOT NULL,
	given_    TEXT NOT NULL,
	when_     TEXT NOT NULL,
	then_     TEXT NOT NULL,
	position  INTEGER NOT NULL,
	PRIMARY KEY (project_id, id)
);
CREATE TABLE IF NOT EXISTS deps (
	project_id  TEXT NOT NULL,
	node_id     TEXT NOT NULL,
	target_id   TEXT NOT NULL,
	pinned_hash TEXT NOT NULL,
	PRIMARY KEY (project_id, node_id, target_id)
);
CREATE TABLE IF NOT EXISTS counters (
	project_id TEXT NOT NULL,
	scope      TEXT NOT NULL,
	n          INTEGER NOT NULL,
	PRIMARY KEY (project_id, scope)
);
CREATE TABLE IF NOT EXISTS evidence (
	project_id  TEXT NOT NULL,
	node_id     TEXT NOT NULL,
	tests       TEXT NOT NULL,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, node_id)
);
CREATE TABLE IF NOT EXISTS story_usage (
	project_id                  TEXT NOT NULL,
	story_id                    TEXT NOT NULL,
	tokens_main                 INTEGER NOT NULL CHECK (tokens_main >= 0),
	tokens_subagents            INTEGER NOT NULL CHECK (tokens_subagents >= 0),
	tokens_main_input           INTEGER NOT NULL DEFAULT 0 CHECK (tokens_main_input >= 0),
	tokens_main_output          INTEGER NOT NULL DEFAULT 0 CHECK (tokens_main_output >= 0),
	tokens_main_cache_read      INTEGER NOT NULL DEFAULT 0 CHECK (tokens_main_cache_read >= 0),
	tokens_main_cache_write     INTEGER NOT NULL DEFAULT 0 CHECK (tokens_main_cache_write >= 0),
	tokens_subagents_input      INTEGER NOT NULL DEFAULT 0 CHECK (tokens_subagents_input >= 0),
	tokens_subagents_output     INTEGER NOT NULL DEFAULT 0 CHECK (tokens_subagents_output >= 0),
	tokens_subagents_cache_read  INTEGER NOT NULL DEFAULT 0 CHECK (tokens_subagents_cache_read >= 0),
	tokens_subagents_cache_write INTEGER NOT NULL DEFAULT 0 CHECK (tokens_subagents_cache_write >= 0),
	categorized                  INTEGER NOT NULL DEFAULT 0 CHECK (categorized IN (0, 1)),
	PRIMARY KEY (project_id, story_id)
);
CREATE TABLE IF NOT EXISTS coverage (
	project_id  TEXT NOT NULL,
	file        TEXT NOT NULL,
	covered     INTEGER NOT NULL,
	total       INTEGER NOT NULL,
	recorded_at TEXT NOT NULL,
	PRIMARY KEY (project_id, file)
);
CREATE TABLE IF NOT EXISTS coverage_meta (
	project_id     TEXT PRIMARY KEY,
	prev_total_pct REAL NOT NULL
);
CREATE TABLE IF NOT EXISTS glossary (
	project_id TEXT NOT NULL,
	term       TEXT NOT NULL,
	definition TEXT NOT NULL,
	PRIMARY KEY (project_id, term)
);
CREATE TABLE IF NOT EXISTS events (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	project_id TEXT NOT NULL,
	ts         TEXT NOT NULL,
	action     TEXT NOT NULL,
	node_id    TEXT NOT NULL DEFAULT '',
	detail     TEXT NOT NULL DEFAULT ''
);
`

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single writer; trellis is a low-traffic local tool
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// Additive migration for stores created before the paths column existed.
	if _, err := db.Exec(`ALTER TABLE nodes ADD COLUMN paths TEXT NOT NULL DEFAULT '[]'`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate paths: %w", err)
	}
	for _, column := range []struct {
		name    string
		typeSQL string
	}{{"position", "INTEGER NOT NULL DEFAULT 0"}, {"activity_id", "TEXT NOT NULL DEFAULT ''"}, {"rank", "INTEGER NOT NULL DEFAULT 0"}, {"slice", "INTEGER NOT NULL DEFAULT 0"}} {
		if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE nodes ADD COLUMN %s %s`, column.name, column.typeSQL)); err != nil &&
			!strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate nodes %s: %w", column.name, err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN release_branch TEXT NOT NULL DEFAULT 'main'`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate release_branch: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN description TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate description: %w", err)
	}
	if _, err := db.Exec(`ALTER TABLE projects ADD COLUMN coverage_glob TEXT NOT NULL DEFAULT ''`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate coverage_glob: %w", err)
	}
	usageColumns := []string{
		"tokens_main_input", "tokens_main_output", "tokens_main_cache_read", "tokens_main_cache_write",
		"tokens_subagents_input", "tokens_subagents_output", "tokens_subagents_cache_read", "tokens_subagents_cache_write",
	}
	for _, column := range usageColumns {
		statement := fmt.Sprintf(`ALTER TABLE story_usage ADD COLUMN %s INTEGER NOT NULL DEFAULT 0 CHECK (%s >= 0)`, column, column)
		if _, err := db.Exec(statement); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("migrate story usage %s: %w", column, err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE story_usage ADD COLUMN categorized INTEGER NOT NULL DEFAULT 0 CHECK (categorized IN (0, 1))`); err != nil &&
		!strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("migrate categorized story usage marker: %w", err)
	}
	// The arch singleton is a database invariant, not just an engine guard:
	// even racing code paths cannot create two arch specs for one story.
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_arch_singleton ON nodes(project_id, parent_id) WHERE kind='arch'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate arch index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_activity_position ON nodes(project_id, position) WHERE kind='activity'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate activity position index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_story_activity ON nodes(project_id, activity_id, slice, rank) WHERE kind='story' AND activity_id<>''`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate story activity index: %w", err)
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS specs_fts USING fts5(project_id UNINDEXED, node_id UNINDEXED, body)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate fts: %w", err)
	}
	st := &Store{db: db, path: path}
	// Rebuild the index for stores that predate FTS.
	var ftsRows, nodeRows int
	db.QueryRow(`SELECT count(*) FROM specs_fts`).Scan(&ftsRows)
	db.QueryRow(`SELECT count(*) FROM nodes`).Scan(&nodeRows)
	if ftsRows == 0 && nodeRows > 0 {
		if err := st.reindexAll(); err != nil {
			db.Close()
			return nil, fmt.Errorf("rebuild fts: %w", err)
		}
	}
	return st, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// ---- projects ----

type Project struct {
	ID            string
	Name          string
	Description   string
	RepoPath      string
	BaseBranch    string
	ReleaseBranch string
	CoverageGlob  string
	LintCmd       string
	TestCmd       string
	JUnitGlob     string
}

func (s *Store) CreateProject(p Project) error {
	if p.ReleaseBranch == "" {
		p.ReleaseBranch = "main"
	}
	_, err := s.db.Exec(`INSERT INTO projects (id, name, description, repo_path, base_branch, release_branch, coverage_glob, lint_cmd, test_cmd, junit_glob, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.RepoPath, p.BaseBranch, p.ReleaseBranch, p.CoverageGlob, p.LintCmd, p.TestCmd, p.JUnitGlob, now())
	return err
}

func (s *Store) GetProject(id string) (Project, error) {
	var p Project
	err := s.db.QueryRow(`SELECT id, name, description, repo_path, base_branch, release_branch, coverage_glob, lint_cmd, test_cmd, junit_glob FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.RepoPath, &p.BaseBranch, &p.ReleaseBranch, &p.CoverageGlob, &p.LintCmd, &p.TestCmd, &p.JUnitGlob)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("project %q: %w", id, ErrNotFound)
	}
	return p, err
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(`UPDATE projects SET name=?, description=?, repo_path=?, base_branch=?, release_branch=?, coverage_glob=?, lint_cmd=?, test_cmd=?, junit_glob=? WHERE id=?`,
		p.Name, p.Description, p.RepoPath, p.BaseBranch, p.ReleaseBranch, p.CoverageGlob, p.LintCmd, p.TestCmd, p.JUnitGlob, p.ID)
	return err
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, description, repo_path, base_branch, release_branch, coverage_glob, lint_cmd, test_cmd, junit_glob FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.RepoPath, &p.BaseBranch, &p.ReleaseBranch, &p.CoverageGlob, &p.LintCmd, &p.TestCmd, &p.JUnitGlob); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- counters / ids ----

// NextID allocates the next monotonic id for a scope. Scopes are either a
// node-kind prefix ("US", "AT", ...) or "ac:<story-id>" for acceptance criteria.
// Counters never reset, so ids are never reused even after deletes.
func (s *Store) NextID(projectID, scope string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	err = tx.QueryRow(`SELECT n FROM counters WHERE project_id=? AND scope=?`, projectID, scope).Scan(&n)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		n = 0
	case err != nil:
		return 0, err
	}
	n++
	if _, err := tx.Exec(`INSERT INTO counters (project_id, scope, n) VALUES (?,?,?)
		ON CONFLICT(project_id, scope) DO UPDATE SET n=excluded.n`, projectID, scope, n); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// ListCounters returns every id counter of a project (scope -> value).
func (s *Store) ListCounters(projectID string) (map[string]int, error) {
	rows, err := s.db.Query(`SELECT scope, n FROM counters WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var scope string
		var n int
		if err := rows.Scan(&scope, &n); err != nil {
			return nil, err
		}
		out[scope] = n
	}
	return out, rows.Err()
}

// SetCounter overwrites one id counter — used by import to preserve
// monotonicity so restored projects never reuse ids.
func (s *Store) SetCounter(projectID, scope string, n int) error {
	_, err := s.db.Exec(`INSERT INTO counters (project_id, scope, n) VALUES (?,?,?)
		ON CONFLICT(project_id, scope) DO UPDATE SET n=excluded.n`, projectID, scope, n)
	return err
}

// ---- nodes ----

const nodeCols = `id, project_id, kind, parent_id, title, body, covers, paths, status, position, activity_id, rank, slice, approved_content_hash, approved_parent_hash, created_at, updated_at`

func scanNode(row interface{ Scan(...any) error }) (model.Node, error) {
	var n model.Node
	var covers, paths, created, updated string
	err := row.Scan(&n.ID, &n.ProjectID, (*string)(&n.Kind), &n.ParentID, &n.Title, &n.Body, &covers, &paths, &n.Status,
		&n.Position, &n.ActivityID, &n.Rank, &n.Slice, &n.ApprovedContentHash, &n.ApprovedParentHash, &created, &updated)
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal([]byte(covers), &n.Covers); err != nil {
		return n, fmt.Errorf("node %s: bad covers: %w", n.ID, err)
	}
	if err := json.Unmarshal([]byte(paths), &n.Paths); err != nil {
		return n, fmt.Errorf("node %s: bad paths: %w", n.ID, err)
	}
	n.CreatedAt, _ = time.Parse(time.RFC3339, created)
	n.UpdatedAt, _ = time.Parse(time.RFC3339, updated)
	return n, nil
}

func coversJSON(covers []string) string {
	if covers == nil {
		covers = []string{}
	}
	b, _ := json.Marshal(covers)
	return string(b)
}

func (s *Store) InsertNode(n model.Node) error {
	_, err := s.db.Exec(`INSERT INTO nodes (`+nodeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.ProjectID, string(n.Kind), n.ParentID, n.Title, n.Body, coversJSON(n.Covers), stringsJSON(n.Paths), n.Status,
		n.Position, n.ActivityID, n.Rank, n.Slice, n.ApprovedContentHash, n.ApprovedParentHash, now(), now())
	if err != nil {
		return err
	}
	return s.reindexNode(n.ProjectID, n.ID)
}

func stringsJSON(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Store) SetNodePaths(projectID, id string, paths []string) error {
	_, err := s.db.Exec(`UPDATE nodes SET paths=?, updated_at=? WHERE project_id=? AND id=?`,
		stringsJSON(paths), now(), projectID, id)
	return err
}

func (s *Store) SetNodePosition(projectID, id string, position int) error {
	_, err := s.db.Exec(`UPDATE nodes SET position=?, updated_at=? WHERE project_id=? AND id=?`, position, now(), projectID, id)
	return err
}

// SetStoryActivity persists backbone activity metadata without rank or slice.
// Story placement uses SetStoryPlacement.
func (s *Store) SetStoryActivity(projectID, storyID, activityID string) error {
	_, err := s.db.Exec(`UPDATE nodes SET activity_id=?, updated_at=? WHERE project_id=? AND id=? AND kind='story'`,
		activityID, now(), projectID, storyID)
	return err
}

func (s *Store) SetStoryPlacement(projectID, storyID, activityID string, rank, slice int) error {
	res, err := s.db.Exec(`UPDATE nodes SET activity_id=?, rank=?, slice=?, updated_at=? WHERE project_id=? AND id=? AND kind='story'`,
		activityID, rank, slice, now(), projectID, storyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("story %q: %w", storyID, ErrNotFound)
	}
	return nil
}

func (s *Store) GetNode(projectID, id string) (model.Node, error) {
	n, err := scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? AND id=?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, fmt.Errorf("node %q: %w", id, ErrNotFound)
	}
	return n, err
}

// UpdateNodeContent rewrites title/body/covers and clears nothing else;
// approval invalidation is implicit because the content hash changes.
func (s *Store) UpdateNodeContent(n model.Node) error {
	_, err := s.db.Exec(`UPDATE nodes SET title=?, body=?, covers=?, updated_at=? WHERE project_id=? AND id=?`,
		n.Title, n.Body, coversJSON(n.Covers), now(), n.ProjectID, n.ID)
	if err != nil {
		return err
	}
	return s.reindexNode(n.ProjectID, n.ID)
}

func (s *Store) UpdateNodeContentAndPosition(n model.Node) error {
	_, err := s.db.Exec(`UPDATE nodes SET title=?, body=?, covers=?, position=?, updated_at=? WHERE project_id=? AND id=?`,
		n.Title, n.Body, coversJSON(n.Covers), n.Position, now(), n.ProjectID, n.ID)
	if err != nil {
		return err
	}
	return s.reindexNode(n.ProjectID, n.ID)
}

func (s *Store) SetNodeStatus(projectID, id, status string) error {
	_, err := s.db.Exec(`UPDATE nodes SET status=?, updated_at=? WHERE project_id=? AND id=?`, status, now(), projectID, id)
	return err
}

func (s *Store) SetApproval(projectID, id, contentHash, parentHash string) error {
	_, err := s.db.Exec(`UPDATE nodes SET approved_content_hash=?, approved_parent_hash=?, updated_at=? WHERE project_id=? AND id=?`,
		contentHash, parentHash, now(), projectID, id)
	return err
}

func (s *Store) DeleteNode(projectID, id string) error {
	_, err := s.db.Exec(`DELETE FROM nodes WHERE project_id=? AND id=?`, projectID, id)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM deps WHERE project_id=? AND node_id=?`, projectID, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM specs_fts WHERE project_id=? AND node_id=?`, projectID, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM story_usage WHERE project_id=? AND story_id=?`, projectID, id); err != nil {
		return err
	}
	return s.DeleteEvidence(projectID, id)
}

func (s *Store) listNodes(query string, args ...any) ([]model.Node, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) ListNodes(projectID string) ([]model.Node, error) {
	return s.listNodes(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? ORDER BY created_at, id`, projectID)
}

func (s *Store) ListNodesByKind(projectID string, kind model.Kind) ([]model.Node, error) {
	return s.listNodes(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? AND kind=? ORDER BY created_at, id`, projectID, string(kind))
}

func (s *Store) ListActivities(projectID string) ([]model.Node, error) {
	return s.listNodes(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? AND kind='activity' ORDER BY position, CAST(SUBSTR(id, 4) AS INTEGER)`, projectID)
}

func (s *Store) NextActivityPosition(projectID string) (int, error) {
	var position int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM nodes WHERE project_id=? AND kind='activity'`, projectID).Scan(&position)
	return position, err
}

func (s *Store) ListStoriesByActivity(projectID, activityID string) ([]model.Node, error) {
	return s.listNodes(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? AND kind='story' AND activity_id=? ORDER BY slice, rank, CAST(SUBSTR(id, 4) AS INTEGER)`, projectID, activityID)
}

func (s *Store) NextStoryRank(projectID, activityID string, slice int) (int, error) {
	var rank int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(rank), 0) + 1 FROM nodes WHERE project_id=? AND kind='story' AND activity_id=? AND slice=?`,
		projectID, activityID, slice).Scan(&rank)
	return rank, err
}

func (s *Store) ListChildren(projectID, parentID string) ([]model.Node, error) {
	return s.listNodes(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? AND parent_id=? ORDER BY created_at, id`, projectID, parentID)
}

// ---- acceptance criteria ----

func (s *Store) InsertAC(projectID string, ac model.AC) error {
	_, err := s.db.Exec(`INSERT INTO acceptance_criteria (id, project_id, story_id, given_, when_, then_, position) VALUES (?,?,?,?,?,?,?)`,
		ac.ID, projectID, ac.StoryID, ac.Given, ac.When, ac.Then, ac.Position)
	if err != nil {
		return err
	}
	return s.reindexNode(projectID, ac.StoryID)
}

func (s *Store) UpdateAC(projectID string, ac model.AC) error {
	res, err := s.db.Exec(`UPDATE acceptance_criteria SET given_=?, when_=?, then_=? WHERE project_id=? AND id=?`,
		ac.Given, ac.When, ac.Then, projectID, ac.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("acceptance criterion %q: %w", ac.ID, ErrNotFound)
	}
	return s.reindexNode(projectID, ac.StoryID)
}

func (s *Store) DeleteAC(projectID, id string) error {
	var storyID string
	s.db.QueryRow(`SELECT story_id FROM acceptance_criteria WHERE project_id=? AND id=?`, projectID, id).Scan(&storyID)
	res, err := s.db.Exec(`DELETE FROM acceptance_criteria WHERE project_id=? AND id=?`, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("acceptance criterion %q: %w", id, ErrNotFound)
	}
	return s.reindexNode(projectID, storyID)
}

func (s *Store) GetAC(projectID, id string) (model.AC, error) {
	var ac model.AC
	err := s.db.QueryRow(`SELECT id, story_id, given_, when_, then_, position FROM acceptance_criteria WHERE project_id=? AND id=?`, projectID, id).
		Scan(&ac.ID, &ac.StoryID, &ac.Given, &ac.When, &ac.Then, &ac.Position)
	if errors.Is(err, sql.ErrNoRows) {
		return ac, fmt.Errorf("acceptance criterion %q: %w", id, ErrNotFound)
	}
	return ac, err
}

func (s *Store) ListACs(projectID, storyID string) ([]model.AC, error) {
	rows, err := s.db.Query(`SELECT id, story_id, given_, when_, then_, position FROM acceptance_criteria WHERE project_id=? AND story_id=? ORDER BY position`, projectID, storyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AC
	for rows.Next() {
		var ac model.AC
		if err := rows.Scan(&ac.ID, &ac.StoryID, &ac.Given, &ac.When, &ac.Then, &ac.Position); err != nil {
			return nil, err
		}
		out = append(out, ac)
	}
	return out, rows.Err()
}

func (s *Store) DeleteACsForStory(projectID, storyID string) error {
	_, err := s.db.Exec(`DELETE FROM acceptance_criteria WHERE project_id=? AND story_id=?`, projectID, storyID)
	return err
}

// ---- deps ----

func (s *Store) LinkDep(projectID string, d model.Dep) error {
	_, err := s.db.Exec(`INSERT INTO deps (project_id, node_id, target_id, pinned_hash) VALUES (?,?,?,?)`,
		projectID, d.NodeID, d.TargetID, d.PinnedHash)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return fmt.Errorf("dependency %s -> %s already exists", d.NodeID, d.TargetID)
	}
	return err
}

func (s *Store) UnlinkDep(projectID, nodeID, targetID string) error {
	res, err := s.db.Exec(`DELETE FROM deps WHERE project_id=? AND node_id=? AND target_id=?`, projectID, nodeID, targetID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("dependency %s -> %s: %w", nodeID, targetID, ErrNotFound)
	}
	return nil
}

func (s *Store) PinDep(projectID, nodeID, targetID, hash string) error {
	_, err := s.db.Exec(`UPDATE deps SET pinned_hash=? WHERE project_id=? AND node_id=? AND target_id=?`, hash, projectID, nodeID, targetID)
	return err
}

func (s *Store) ListDeps(projectID, nodeID string) ([]model.Dep, error) {
	return s.listDeps(`SELECT node_id, target_id, pinned_hash FROM deps WHERE project_id=? AND node_id=? ORDER BY target_id`, projectID, nodeID)
}

func (s *Store) ListDependents(projectID, targetID string) ([]model.Dep, error) {
	return s.listDeps(`SELECT node_id, target_id, pinned_hash FROM deps WHERE project_id=? AND target_id=? ORDER BY node_id`, projectID, targetID)
}

func (s *Store) listDeps(query string, args ...any) ([]model.Dep, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Dep
	for rows.Next() {
		var d model.Dep
		if err := rows.Scan(&d.NodeID, &d.TargetID, &d.PinnedHash); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- evidence ----

type Evidence struct {
	Tests      []string
	RecordedAt string
}

// SetEvidence records the proving tests for a spec node, replacing any
// earlier record — evidence is current state, not history.
func (s *Store) SetEvidence(projectID, nodeID string, tests []string) error {
	return s.SetEvidenceAt(projectID, nodeID, tests, now())
}

// SetEvidenceAt is SetEvidence with an explicit timestamp — import uses it
// to preserve the original recording time.
func (s *Store) SetEvidenceAt(projectID, nodeID string, tests []string, recordedAt string) error {
	_, err := s.db.Exec(`INSERT INTO evidence (project_id, node_id, tests, recorded_at) VALUES (?,?,?,?)
		ON CONFLICT(project_id, node_id) DO UPDATE SET tests=excluded.tests, recorded_at=excluded.recorded_at`,
		projectID, nodeID, stringsJSON(tests), recordedAt)
	return err
}

// GetEvidence returns the last recorded evidence, or ok=false when none exists.
func (s *Store) GetEvidence(projectID, nodeID string) (Evidence, bool, error) {
	var ev Evidence
	var tests string
	err := s.db.QueryRow(`SELECT tests, recorded_at FROM evidence WHERE project_id=? AND node_id=?`, projectID, nodeID).
		Scan(&tests, &ev.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ev, false, nil
	}
	if err != nil {
		return ev, false, err
	}
	if err := json.Unmarshal([]byte(tests), &ev.Tests); err != nil {
		return ev, false, err
	}
	return ev, true, nil
}

func (s *Store) DeleteEvidence(projectID, nodeID string) error {
	_, err := s.db.Exec(`DELETE FROM evidence WHERE project_id=? AND node_id=?`, projectID, nodeID)
	return err
}

// ---- coverage ----

type CoverageRow struct {
	File       string `json:"file"`
	Covered    int    `json:"covered"`
	Total      int    `json:"total"`
	RecordedAt string `json:"recorded_at"`
}

// SetCoverage replaces the project's coverage snapshot — current state, no
// history (CC-4).
func (s *Store) SetCoverage(projectID string, rows []CoverageRow) error {
	if _, err := s.db.Exec(`DELETE FROM coverage WHERE project_id=?`, projectID); err != nil {
		return err
	}
	ts := now()
	for _, r := range rows {
		if _, err := s.db.Exec(`INSERT INTO coverage (project_id, file, covered, total, recorded_at) VALUES (?,?,?,?,?)`,
			projectID, r.File, r.Covered, r.Total, ts); err != nil {
			return err
		}
	}
	return nil
}

// SetCoveragePrevTotal remembers the outgoing snapshot's overall percentage
// — one scalar, no history.
func (s *Store) SetCoveragePrevTotal(projectID string, pct float64) error {
	_, err := s.db.Exec(`INSERT INTO coverage_meta (project_id, prev_total_pct) VALUES (?,?)
		ON CONFLICT(project_id) DO UPDATE SET prev_total_pct=excluded.prev_total_pct`, projectID, pct)
	return err
}

// CoveragePrevTotal returns the remembered percentage, ok=false when none.
func (s *Store) CoveragePrevTotal(projectID string) (float64, bool, error) {
	var pct float64
	err := s.db.QueryRow(`SELECT prev_total_pct FROM coverage_meta WHERE project_id=?`, projectID).Scan(&pct)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return pct, err == nil, err
}

func (s *Store) ListCoverage(projectID string) ([]CoverageRow, error) {
	rows, err := s.db.Query(`SELECT file, covered, total, recorded_at FROM coverage WHERE project_id=? ORDER BY file`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoverageRow
	for rows.Next() {
		var r CoverageRow
		if err := rows.Scan(&r.File, &r.Covered, &r.Total, &r.RecordedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- glossary ----

type TermDef struct {
	Term       string `json:"term"`
	Definition string `json:"definition"`
}

// DefineTerm creates or updates a glossary entry.
func (s *Store) DefineTerm(projectID, term, definition string) error {
	_, err := s.db.Exec(`INSERT INTO glossary (project_id, term, definition) VALUES (?,?,?)
		ON CONFLICT(project_id, term) DO UPDATE SET definition=excluded.definition`,
		projectID, term, definition)
	return err
}

func (s *Store) DeleteTerm(projectID, term string) error {
	res, err := s.db.Exec(`DELETE FROM glossary WHERE project_id=? AND term=?`, projectID, term)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("glossary term %q: %w", term, ErrNotFound)
	}
	return nil
}

func (s *Store) ListTerms(projectID string) ([]TermDef, error) {
	rows, err := s.db.Query(`SELECT term, definition FROM glossary WHERE project_id=? ORDER BY term COLLATE NOCASE`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TermDef
	for rows.Next() {
		var td TermDef
		if err := rows.Scan(&td.Term, &td.Definition); err != nil {
			return nil, err
		}
		out = append(out, td)
	}
	return out, rows.Err()
}

// ---- events ----

type Event struct {
	Seq    int64
	TS     string
	Action string
	NodeID string
	Detail string
}

func (s *Store) AppendEvent(projectID, action, nodeID, detail string) {
	// The event log is a flight recorder; a failed write must never block a mutation.
	_, _ = s.db.Exec(`INSERT INTO events (project_id, ts, action, node_id, detail) VALUES (?,?,?,?,?)`,
		projectID, now(), action, nodeID, detail)
}

func (s *Store) ListEvents(projectID string, limit int) ([]Event, error) {
	rows, err := s.db.Query(`SELECT seq, ts, action, node_id, detail FROM events WHERE project_id=? ORDER BY seq DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Seq, &e.TS, &e.Action, &e.NodeID, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- search (FTS5) ----

// buildFTSQuery turns free text into a safe FTS5 query: terms are quoted so
// operators and punctuation stay literal, joined with AND, and the last term
// matches as a word prefix. Terms without any letter or digit are dropped.
func buildFTSQuery(q string) string {
	terms := strings.Fields(q)
	var parts []string
	for _, t := range terms {
		hasWord := false
		for _, r := range t {
			if ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') || r > 127 {
				hasWord = true
				break
			}
		}
		if !hasWord {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	parts[len(parts)-1] += "*"
	return strings.Join(parts, " AND ")
}

type FTSHit struct {
	NodeID  string
	Snippet string
}

// SearchFTS returns matching nodes ordered by BM25 relevance with a snippet
// around the matches. AC text is folded into its story's index row.
func (s *Store) SearchFTS(projectID, query string) ([]FTSHit, error) {
	match := buildFTSQuery(query)
	if match == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT node_id, snippet(specs_fts, 2, '', '', '…', 12)
		FROM specs_fts WHERE specs_fts MATCH ? AND project_id=? ORDER BY bm25(specs_fts)`, match, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FTSHit
	for rows.Next() {
		var h FTSHit
		if err := rows.Scan(&h.NodeID, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// reindexNode rewrites a node's FTS row from title, body and (for stories)
// its acceptance-criterion text.
func (s *Store) reindexNode(projectID, nodeID string) error {
	if nodeID == "" {
		return nil
	}
	n, err := s.GetNode(projectID, nodeID)
	if errors.Is(err, ErrNotFound) {
		_, err := s.db.Exec(`DELETE FROM specs_fts WHERE project_id=? AND node_id=?`, projectID, nodeID)
		return err
	}
	if err != nil {
		return err
	}
	text := n.Title + "\n" + n.Body
	if n.Kind == model.KindStory {
		acs, err := s.ListACs(projectID, nodeID)
		if err != nil {
			return err
		}
		for _, ac := range acs {
			text += "\n" + ac.Given + " " + ac.When + " " + ac.Then
		}
	}
	if _, err := s.db.Exec(`DELETE FROM specs_fts WHERE project_id=? AND node_id=?`, projectID, nodeID); err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO specs_fts (project_id, node_id, body) VALUES (?,?,?)`, projectID, nodeID, text)
	return err
}

func (s *Store) reindexAll() error {
	rows, err := s.db.Query(`SELECT project_id, id FROM nodes`)
	if err != nil {
		return err
	}
	type key struct{ p, n string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.p, &k.n); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		if err := s.reindexNode(k.p, k.n); err != nil {
			return err
		}
	}
	return nil
}

// MaxEventSeq returns the highest event sequence for a project (0 when none).
// The live board polls this to detect changes across processes.
func (s *Store) MaxEventSeq(projectID string) (int64, error) {
	var seq sql.NullInt64
	err := s.db.QueryRow(`SELECT MAX(seq) FROM events WHERE project_id=?`, projectID).Scan(&seq)
	if err != nil {
		return 0, err
	}
	return seq.Int64, nil
}
