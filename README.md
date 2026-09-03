# trellis

Deterministic spec tracking and story gating for LLM-driven development.

trellis is the single source of truth for user stories, spec trees and story
state. An agent interacts with it exclusively through MCP tools; the database
is hidden outside the repo. Every illegal move — wrong state transition,
incomplete spec tree, stale approval, missing test evidence — is rejected with
an error that says exactly what was illegal. The judgment about "done" is made
by trellis (by running the tests and reading the JUnit reports), never by the
agent.

## Model

One tree per story, strict parent/child (a child has exactly one parent):

```
US  story  (+ structured acceptance criteria: given/when/then)
├── AT  acceptance_test   — must cover every AC (covers: [...])
└── AS  arch              — exactly one per story
    ├── IT  integration_test
    └── DD  detail_design
        └── UT  unit_test
```

Cross-cutting architecture lives in `CC cross_cutting` root nodes. Story nodes
reference them via hash-pinned `depends_on` links.

Every node has a content hash. Approval stores the hash of the node and of its
parent; dependency links pin the target's hash. Editing anything invalidates
every child and dependent approval and automatically drops affected
refined/in_progress stories back to `todo`. Approving requires passing the
current content hash (and the hashes of all dependency targets) — proof that
the approver actually read what it approved.

## State machine

```
todo --refine--> refined --start--> in_progress --finish--> done
```

- **refine**: tree complete (every AC covered, one arch, ITs, DDs, UTs), every
  node approved and fresh.
- **start**: creates a dedicated git worktree at `.trellis-worktrees/US-n`
  on branch `feature/US-n` from the base branch (default `develop`). Stories
  run in parallel by construction; the main worktree stays parked on the
  base branch, and the scaffolded pre-commit hook rejects direct commits
  there. `.trellis-worktrees/` must be git-ignored.
- **finish**: runs the lint command, runs the test command, parses the JUnit
  reports and verifies that every test spec in the tree (AT/IT/UT) is
  referenced by at least one passing, non-skipped test — a test proves spec
  `UT-3` iff its name contains `UT-3` or `UT_3`. Then merges `--no-ff` into
  the base branch via the main worktree and removes worktree and branch.
  `abort` discards worktree and branch (clean worktree required) and drops
  the story back to refined.

Statuses can never be set directly; only transitions move them. Done trees
are the durable context source: traceability from acceptance criteria to
architecture to test evidence for everything that was built. They stay
editable so the context can track reality — honesty comes from hashes, not
locks: every unreviewed change shows as a stale marker until re-approved,
and done stories are never reopened automatically. When a feature leaves the
product, `trellis prune` removes its tree whole. No war stories: content is
either current context or deleted.

## Setup

```sh
go build -o ~/bin/trellis ./cmd/trellis

trellis init --name myproject --repo /path/to/repo        # prints project id
trellis config <id> --lint 'make lint' \
                    --test 'make test-junit' \
                    --junit 'reports/*.xml'
```

Register the MCP server in the target repo's `.mcp.json`:

```json
{"mcpServers": {"trellis": {"command": "trellis", "args": ["serve", "--project", "<id>"]}}}
```

And point the agent at it in `AGENTS.md`:

```
trellis-project: <id>
Specs, tickets and story state live in trellis; use its MCP tools.
It is the single source of truth.
```

Data lives in `~/.local/share/trellis/trellis.db` (override with
`$TRELLIS_DATA_DIR`) — deliberately outside the repo.

## Interfaces

- **MCP (the agent)**: get_overview, create_node, set_map_position, update_node,
  delete_node, get_node, get_tree, add/update/delete_acceptance_criterion, approve,
  link_dependency, unlink_dependency, transition.
- **CLI (humans and trusted extension code)**: init, projects, config (gate
  commands are configurable only here — the agent cannot weaken the gates),
  tree, log (append-only event log / flight recorder), prune, serve, and
  `usage add` for extension-reported token usage. No MCP tool writes usage.

## Development

```sh
go test ./...
go vet ./...
```
