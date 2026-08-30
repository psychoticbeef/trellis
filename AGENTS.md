# Agent instructions

trellis-project: trellis-980f

Specs, tickets and story state for this repository live in trellis — use its
MCP tools (server `trellis`). It is the single source of truth:

- Check `get_overview` before picking up work; work only on stories, never on
  ad-hoc tasks outside a story.
- Done stories are the context source: before designing or implementing,
  read the done trees (`get_tree`) and cross-cutting specs that touch your
  area — they tell you what was built, why, and which tests prove it. When
  reality diverges from a done spec, correct the spec in place and
  re-approve it; stale markers show what still needs review.
- A story may only be implemented via `transition(story, "start")` and
  completed via `transition(story, "finish")`. Never merge to develop yourself.
- Test names must reference the spec ids they prove (e.g. `TestFoo_UT_3`
  proves UT-3).
- Build the binary with `go build -o bin/trellis ./cmd/trellis` if
  `bin/trellis` is missing.
