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
	db *sql.DB
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
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// ---- projects ----

type Project struct {
	ID         string
	Name       string
	RepoPath   string
	BaseBranch string
	LintCmd    string
	TestCmd    string
	JUnitGlob  string
}

func (s *Store) CreateProject(p Project) error {
	_, err := s.db.Exec(`INSERT INTO projects (id, name, repo_path, base_branch, lint_cmd, test_cmd, junit_glob, created_at)
		VALUES (?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.RepoPath, p.BaseBranch, p.LintCmd, p.TestCmd, p.JUnitGlob, now())
	return err
}

func (s *Store) GetProject(id string) (Project, error) {
	var p Project
	err := s.db.QueryRow(`SELECT id, name, repo_path, base_branch, lint_cmd, test_cmd, junit_glob FROM projects WHERE id = ?`, id).
		Scan(&p.ID, &p.Name, &p.RepoPath, &p.BaseBranch, &p.LintCmd, &p.TestCmd, &p.JUnitGlob)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("project %q: %w", id, ErrNotFound)
	}
	return p, err
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(`UPDATE projects SET name=?, repo_path=?, base_branch=?, lint_cmd=?, test_cmd=?, junit_glob=? WHERE id=?`,
		p.Name, p.RepoPath, p.BaseBranch, p.LintCmd, p.TestCmd, p.JUnitGlob, p.ID)
	return err
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT id, name, repo_path, base_branch, lint_cmd, test_cmd, junit_glob FROM projects ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.RepoPath, &p.BaseBranch, &p.LintCmd, &p.TestCmd, &p.JUnitGlob); err != nil {
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

// ---- nodes ----

const nodeCols = `id, project_id, kind, parent_id, title, body, covers, status, approved_content_hash, approved_parent_hash, created_at, updated_at`

func scanNode(row interface{ Scan(...any) error }) (model.Node, error) {
	var n model.Node
	var covers, created, updated string
	err := row.Scan(&n.ID, &n.ProjectID, (*string)(&n.Kind), &n.ParentID, &n.Title, &n.Body, &covers, &n.Status,
		&n.ApprovedContentHash, &n.ApprovedParentHash, &created, &updated)
	if err != nil {
		return n, err
	}
	if err := json.Unmarshal([]byte(covers), &n.Covers); err != nil {
		return n, fmt.Errorf("node %s: bad covers: %w", n.ID, err)
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
	_, err := s.db.Exec(`INSERT INTO nodes (`+nodeCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.ProjectID, string(n.Kind), n.ParentID, n.Title, n.Body, coversJSON(n.Covers), n.Status,
		n.ApprovedContentHash, n.ApprovedParentHash, now(), now())
	return err
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
	return err
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
	_, err = s.db.Exec(`DELETE FROM deps WHERE project_id=? AND node_id=?`, projectID, id)
	return err
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

func (s *Store) ListChildren(projectID, parentID string) ([]model.Node, error) {
	return s.listNodes(`SELECT `+nodeCols+` FROM nodes WHERE project_id=? AND parent_id=? ORDER BY created_at, id`, projectID, parentID)
}

// ---- acceptance criteria ----

func (s *Store) InsertAC(projectID string, ac model.AC) error {
	_, err := s.db.Exec(`INSERT INTO acceptance_criteria (id, project_id, story_id, given_, when_, then_, position) VALUES (?,?,?,?,?,?,?)`,
		ac.ID, projectID, ac.StoryID, ac.Given, ac.When, ac.Then, ac.Position)
	return err
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
	return nil
}

func (s *Store) DeleteAC(projectID, id string) error {
	res, err := s.db.Exec(`DELETE FROM acceptance_criteria WHERE project_id=? AND id=?`, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("acceptance criterion %q: %w", id, ErrNotFound)
	}
	return nil
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
