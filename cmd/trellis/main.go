// trellis: deterministic spec tracking and story gating for LLM-driven development.
//
// The agent talks to trellis only via MCP (trellis serve); the CLI is the
// human's window: init, config, inspection, prune.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/mcpserver"
	"trellis/internal/store"
)

const version = "0.1.0"

const usage = `trellis %s — deterministic spec tracking for LLM-driven development

Usage:
  trellis init --name <name> --repo <path> [--base <branch>]   create a project
  trellis projects                                             list projects
  trellis config <project-id> [flags]                          show or set config
      --repo <path> --base <branch> --lint <cmd> --test <cmd> --junit <glob>
  trellis serve --project <project-id> [--board-addr <addr>]   run MCP server (stdio) + board UI (default 127.0.0.1:7420, off disables)
  trellis tree <project-id> <story-id>                         print a story's spec tree
  trellis log <project-id> [-n <count>]                        print the event log
  trellis prune <project-id> <story-id>                        delete a done story's tree
  trellis affected <project-id> <path>                         stories declaring a file/folder
  trellis board <project-id> [-o <file>] [--serve [--addr]]    write or serve the HTML spec board
  trellis gate <lint|test> --project <project-id>              run a configured gate (used by git hooks)
  trellis release <project-id>                                 merge base into release with feature manifest + backup
  trellis audit <project-id>                                   validate spec and reality in both directions
  trellis doctor <project-id> [--fix]                          detect and repair setup drift
  trellis next <project-id>                                    show the next startable stories
  trellis export <project-id> [-o <file>]                      write the spec database as YAML
  trellis import -f <file> --name <name> --repo <path>         restore an export into a new project

Data dir: $TRELLIS_DATA_DIR or ~/.local/share/trellis
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "trellis:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Printf(usage, version)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "init":
		return cmdInit(rest)
	case "projects":
		return cmdProjects()
	case "config":
		return cmdConfig(rest)
	case "serve":
		return cmdServe(rest)
	case "tree":
		return cmdTree(rest)
	case "log":
		return cmdLog(rest)
	case "prune":
		return cmdPrune(rest)
	case "affected":
		return cmdAffected(rest)
	case "board":
		return cmdBoard(rest)
	case "gate":
		return cmdGate(rest)
	case "release":
		return cmdRelease(rest)
	case "audit":
		return cmdAudit(rest)
	case "doctor":
		return cmdDoctor(rest)
	case "next":
		return cmdNext(rest)
	case "export":
		return cmdExport(rest)
	case "import":
		return cmdImport(rest)
	case "--version", "version":
		fmt.Println("trellis", version)
		return nil
	default:
		fmt.Printf(usage, version)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func dataDir() (string, error) {
	if d := os.Getenv("TRELLIS_DATA_DIR"); d != "" {
		return d, os.MkdirAll(d, 0o755)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, ".local", "share", "trellis")
	return d, os.MkdirAll(d, 0o755)
}

func openStore() (*store.Store, error) {
	d, err := dataDir()
	if err != nil {
		return nil, err
	}
	return store.Open(filepath.Join(d, "trellis.db"))
}

func engine(projectID string) (*core.Engine, *store.Store, error) {
	st, err := openStore()
	if err != nil {
		return nil, nil, err
	}
	e, err := core.NewEngine(st, projectID)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return e, st, nil
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	name := fs.String("name", "", "project name (required)")
	repo := fs.String("repo", "", "path to the git repository trellis manages (required)")
	base := fs.String("base", "develop", "base branch features merge into")
	fs.Parse(args)
	if *name == "" || *repo == "" {
		return fmt.Errorf("init requires --name and --repo")
	}
	repoAbs, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	suffix := make([]byte, 2)
	rand.Read(suffix)
	id := fmt.Sprintf("%s-%s", slugify(*name), hex.EncodeToString(suffix))
	if err := st.CreateProject(store.Project{ID: id, Name: *name, RepoPath: repoAbs, BaseBranch: *base}); err != nil {
		return err
	}
	fmt.Printf("project created: %s\n\nscaffolding %s:\n", id, repoAbs)
	msgs, createdFiles := scaffold(repoAbs, id)
	msgs = append(msgs, commitScaffold(repoAbs, createdFiles))
	for _, msg := range msgs {
		fmt.Println("  -", msg)
	}
	fmt.Printf(`
Next step — configure the gates:
   trellis config %s --lint '<lint cmd>' --test '<test cmd producing junit xml>' --junit '<glob, e.g. reports/*.xml>'
`, id)
	return nil
}

func cmdProjects() error {
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	projects, err := st.ListProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Println("no projects; create one with trellis init")
		return nil
	}
	for _, p := range projects {
		fmt.Printf("%-24s %-16s repo=%s base=%s\n", p.ID, p.Name, p.RepoPath, p.BaseBranch)
	}
	return nil
}

func cmdConfig(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("config requires a project id")
	}
	id, rest := args[0], args[1:]
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	repo := fs.String("repo", "", "repo path")
	base := fs.String("base", "", "base branch")
	release := fs.String("release", "", "release branch (default main)")
	desc := fs.String("desc", "", "one-line project description")
	coverage := fs.String("coverage", "", "coverage report glob (lcov or Go coverprofile), relative to repo")
	lint := fs.String("lint", "", "lint command (run before merge)")
	test := fs.String("test", "", "test command (must write junit xml)")
	junit := fs.String("junit", "", "junit report glob, relative to repo")
	fs.Parse(rest)

	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	p, err := st.GetProject(id)
	if err != nil {
		return err
	}
	changed := false
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
			changed = true
		}
	}
	set(&p.RepoPath, *repo)
	set(&p.BaseBranch, *base)
	set(&p.ReleaseBranch, *release)
	set(&p.Description, *desc)
	set(&p.CoverageGlob, *coverage)
	set(&p.LintCmd, *lint)
	set(&p.TestCmd, *test)
	set(&p.JUnitGlob, *junit)
	if changed {
		if err := st.UpdateProject(p); err != nil {
			return err
		}
	}
	fmt.Printf("project:  %s (%s)\ndesc:     %s\nrepo:     %s\nbase:     %s\nrelease:  %s\nlint_cmd: %s\ntest_cmd: %s\njunit:    %s\ncoverage: %s\n",
		p.ID, p.Name, orEmpty(p.Description), p.RepoPath, p.BaseBranch, p.ReleaseBranch, orEmpty(p.LintCmd), orEmpty(p.TestCmd), orEmpty(p.JUnitGlob), orEmpty(p.CoverageGlob))
	return nil
}

func orEmpty(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	project := fs.String("project", "", "project id (required)")
	boardAddr := fs.String("board-addr", "127.0.0.1:7420", "board UI address; off disables")
	fs.Parse(args)
	if *project == "" {
		return fmt.Errorf("serve requires --project")
	}
	e, st, err := engine(*project)
	if err != nil {
		return err
	}
	defer st.Close()
	// The board UI rides along on stderr-only logging: stdout is the MCP
	// protocol channel. A bind failure must never block the agent connection.
	if *boardAddr != "off" {
		if ln, err := net.Listen("tcp", *boardAddr); err != nil {
			fmt.Fprintf(os.Stderr, "trellis: board UI disabled (%v)\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "trellis: boards at http://%s\n", ln.Addr())
			go http.Serve(ln, board.MultiHandler(st))
		}
	}
	srv := mcpserver.New(e, version)
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

func cmdTree(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: trellis tree <project-id> <story-id>")
	}
	e, st, err := engine(args[0])
	if err != nil {
		return err
	}
	defer st.Close()
	r, err := e.Tree(args[1])
	if err != nil {
		return err
	}
	fmt.Printf("%s [%s] %s\n", r.Story.ID, r.Status, r.Story.Title)
	for _, ac := range r.ACs {
		fmt.Printf("  %s  given %s / when %s / then %s  (covered by %v)\n", ac.ID, ac.Given, ac.When, ac.Then, ac.CoveredBy)
	}
	var walk func(n core.TreeNode, indent string)
	walk = func(n core.TreeNode, indent string) {
		mark := "✓"
		if !n.Fresh {
			mark = "✗"
		}
		extra := ""
		if len(n.Covers) > 0 {
			extra = fmt.Sprintf(" covers=%v", n.Covers)
		}
		for _, d := range n.Deps {
			state := "fresh"
			if !d.Fresh {
				state = "STALE"
			}
			extra += fmt.Sprintf(" dep=%s(%s)", d.Target, state)
		}
		fmt.Printf("%s%s %s (%s) %s%s\n", indent, mark, n.ID, n.Kind, n.Title, extra)
		for _, p := range n.Problems {
			fmt.Printf("%s    ! %s\n", indent, p)
		}
		for _, c := range n.Children {
			walk(c, indent+"  ")
		}
	}
	for _, c := range r.Story.Children {
		walk(c, "  ")
	}
	if len(r.Integrity) == 0 {
		fmt.Println("gates: open")
	} else {
		fmt.Println("gates: blocked")
		for _, p := range r.Integrity {
			fmt.Println("  -", p)
		}
	}
	return nil
}

func cmdLog(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: trellis log <project-id> [-n count]")
	}
	id, rest := args[0], args[1:]
	fs := flag.NewFlagSet("log", flag.ExitOnError)
	n := fs.Int("n", 50, "number of events")
	fs.Parse(rest)
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	events, err := st.ListEvents(id, *n)
	if err != nil {
		return err
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		fmt.Printf("%s  %-15s %-8s %s\n", e.TS, e.Action, e.NodeID, e.Detail)
	}
	return nil
}

func cmdBoard(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: trellis board <project-id> [-o file] [--serve [--addr host:port]]")
	}
	id, rest := args[0], args[1:]
	fs := flag.NewFlagSet("board", flag.ExitOnError)
	out := fs.String("o", "trellis-board.html", "output file")
	serve := fs.Bool("serve", false, "serve the board over HTTP with live reload")
	addr := fs.String("addr", "127.0.0.1:7420", "listen address for --serve")
	fs.Parse(rest)
	e, st, err := engine(id)
	if err != nil {
		return err
	}
	defer st.Close()
	if *serve {
		return board.Serve(e, st, *addr)
	}
	html, err := board.Render(e)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(html), 0o644); err != nil {
		return err
	}
	fmt.Printf("board written to %s\n", *out)
	return nil
}

func cmdRelease(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: trellis release <project-id>")
	}
	e, st, err := engine(args[0])
	if err != nil {
		return err
	}
	defer st.Close()
	msg, err := e.Release()
	if err != nil {
		return err
	}
	fmt.Println(msg)
	return nil
}

func cmdAudit(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: trellis audit <project-id>")
	}
	e, st, err := engine(args[0])
	if err != nil {
		return err
	}
	defer st.Close()
	rep, err := e.Audit()
	if err != nil {
		return err
	}
	for _, v := range rep.Violations {
		fmt.Println("VIOLATION:", v)
	}
	for _, i := range rep.Infos {
		fmt.Println("info:", i)
	}
	if len(rep.Violations) == 0 {
		fmt.Println("audit clean: spec and reality agree in both directions")
		return nil
	}
	return fmt.Errorf("%d violation(s)", len(rep.Violations))
}

func cmdExport(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: trellis export <project-id> [-o file]")
	}
	id, rest := args[0], args[1:]
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	out := fs.String("o", "trellis-specs.yaml", "output file")
	fs.Parse(rest)
	e, st, err := engine(id)
	if err != nil {
		return err
	}
	defer st.Close()
	doc, err := e.ExportYAML()
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, []byte(doc), 0o644); err != nil {
		return err
	}
	fmt.Printf("export written to %s\n", *out)
	return nil
}

func cmdImport(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	file := fs.String("f", "", "export file (required)")
	name := fs.String("name", "", "new project name (required)")
	repo := fs.String("repo", "", "repo path for the new project (required)")
	fs.Parse(args)
	if *file == "" || *name == "" || *repo == "" {
		return fmt.Errorf("import requires -f, --name and --repo")
	}
	data, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	repoAbs, err := filepath.Abs(*repo)
	if err != nil {
		return err
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	suffix := make([]byte, 2)
	rand.Read(suffix)
	id := fmt.Sprintf("%s-%s", slugify(*name), hex.EncodeToString(suffix))
	if err := core.Import(st, data, store.Project{ID: id, Name: *name, RepoPath: repoAbs}); err != nil {
		return err
	}
	fmt.Printf("imported as project %s\n", id)
	return nil
}

func cmdNext(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: trellis next <project-id>")
	}
	e, st, err := engine(args[0])
	if err != nil {
		return err
	}
	defer st.Close()
	candidates, blocked, err := e.NextStories()
	if err != nil {
		return err
	}
	if len(candidates) == 0 && len(blocked) == 0 {
		fmt.Println("no refined story — refine one first")
		return nil
	}
	for _, c := range candidates {
		fmt.Printf("start:   %-6s %s\n", c.ID, c.Title)
	}
	for _, b := range blocked {
		fmt.Printf("blocked: %-6s %s — waiting on %s\n", b.ID, b.Title, strings.Join(b.WaitingOn, ", "))
	}
	return nil
}

func cmdAffected(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: trellis affected <project-id> <path>")
	}
	e, st, err := engine(args[0])
	if err != nil {
		return err
	}
	defer st.Close()
	stories, err := e.StoriesForPath(args[1])
	if err != nil {
		return err
	}
	for _, s := range stories {
		fmt.Printf("%-6s %-12s %s\n", s.ID, s.Status, s.Title)
	}
	return nil
}

func cmdPrune(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: trellis prune <project-id> <story-id>")
	}
	e, st, err := engine(args[0])
	if err != nil {
		return err
	}
	defer st.Close()
	if err := e.Prune(args[1]); err != nil {
		return err
	}
	fmt.Printf("%s pruned\n", args[1])
	return nil
}
