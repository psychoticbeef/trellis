# trellis

> Deterministic spec tracking and story gating for LLM-driven development: hash-approved spec trees, gated gitflow in worktrees, test evidence as the definition of done.

## Features

- **US-1** — Spec tree management
- **US-2** — Structured acceptance criteria
- **US-3** — Hash-based approval and invalidation
- **US-4** — Cross-cutting architecture dependencies
- **US-5** — Story state machine with gates
- **US-6** — Git flow and test evidence
- **US-10** — Abort transition
- **US-11** — Story sequencing dependencies
- **US-12** — Spec search
- **US-13** — Spec-to-code paths
- **US-14** — Test evidence on record
- **US-15** — Board export
- **US-16** — Init scaffolding and git hooks
- **US-8** — Done trees are living context
- **US-9** — Merge gate: test what will merge
- **US-17** — Worktree isolation
- **US-18** — Live board
- **US-19** — FTS5 search
- **US-20** — Gates run where the commit happens
- **US-21** — Project glossary
- **US-22** — Release cut with feature manifest
- **US-23** — Boards served by the MCP server
- **US-24** — Project description
- **US-25** — YAML export and import
- **US-26** — Batch tree approval
- **US-27** — Atomic mutations
- **US-28** — Kanban board
- **US-29** — Init wires and commits its own scaffold
- **US-30** — Interoperable MCP output schemas
- **US-31** — Release commits carry trellis authority
- **US-32** — Coverage visibility
- **US-33** — Bidirectional audit
- **US-34** — Setup doctor
- **US-35** — Next story
- **US-36** — Coverage delta
- **US-37** — Token usage per story
- **US-38** — Unclaimed files fail audit
- **US-39** — Categorized token usage
- **US-40** — Exhaustive token usage overflow errors
- **US-41** — Separate board overview and story detail
- **US-42** — Board and serve polish
- **US-43** — Story map backbone
- **US-44** — Story placement
- **US-45** — Placement gate
- **US-46** — Map-aware overview and next story
- **US-47** — Placement hints on story creation
- **US-48** — Story map board tab
- **US-49** — Walking skeleton in doctor
- **US-50** — Story map drag and drop
- **US-51** — Placement requires an approved activity

## Glossary

- **acceptance criterion** — Structured given/when/then requirement on a story; every one must be covered by an acceptance test spec.
- **activity** — Root node naming one user activity; ordered by position on the story map.
- **activity approval check** — Placement requires target activity approved and fresh.
- **approval** — Hash-proof that a reviewer read a node's current content; parents before children, dependencies re-pinned.
- **base branch** — The branch that only receives trellis merges (default develop); direct commits are gate-blocked.
- **categorized token usage** — Token usage split into input, output, cache_read, and cache_write for main-agent and subagents.
- **cross-cutting spec** — Root-level architecture decision, referenced from arch specs via hash-pinned dependency links.
- **evidence** — The proving tests recorded per test spec at finish; latest run replaces older records.
- **gate** — A guard that must pass before a transition proceeds: structure, freshness, lint, tests, evidence, up-to-date branch.
- **integrity marker** — Story label: stale for invalidated approval, blocked for other integrity problems, fresh otherwise.
- **lane** — Stories sharing one activity and slice.
- **living context** — Done specs stay editable so context tracks reality; honesty comes from stale markers, not locks.
- **map complete** — At least one activity and no unmapped story; derived on every call, never stored.
- **open slice** — Existing slice or next slice offered as placement candidate.
- **placement** — A story's activity, rank and slice; metadata like paths, never hashed.
- **placement gate** — While map complete: story creation without placement and set_map_position clearing placement reject; import and refine stay unchanged.
- **placement hint** — Transient story-creation guidance listing ranked activities and story map gaps.
- **prune** — Hard delete of a done story's tree when its feature leaves the product; the only way spec content disappears.
- **sequencing link** — Unpinned story-to-story dependency: start waits until the prerequisite story is done.
- **slice** — Integer release cut on the story map; soft priority for next_story, never a gate by itself.
- **spec tree** — One story's hierarchy: acceptance tests, exactly one arch spec, integration tests, detail designs, unit test specs.
- **stale** — An approval invalidated by a content, parent or dependency change; cleared only by re-approval.
- **story** — Unit of work: a spec tree plus the gated lifecycle todo -> refined -> in_progress -> done.
- **story detail overlay** — Read-only overlay showing complete story context.
- **story map** — Optional 2D view: activities left to right, stories ranked below, slices across.
- **story map gap** — Empty activity-by-slice cell through highest used slice.
- **story worktree** — Per-story git worktree under .trellis-worktrees/ where all implementation work happens.
- **token usage** — Persisted per-story counts for main-agent and subagent tokens.
- **uncategorized token usage** — Token usage stored only as main-agent and subagent totals.
- **unmapped story** — Story without placement; legal only while the map is incomplete.
- **walking skeleton** — Slice 1 with at least one story under every activity.

_Generated by trellis release; this file lives only on the release branch._
