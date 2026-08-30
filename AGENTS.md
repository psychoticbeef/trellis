# Agent instructions

trellis-project: trellis-980f

Specs, tickets and story state for this repository live in trellis — use its
MCP tools (server `trellis`). It is the single source of truth:

- Check `get_overview` before picking up work; work only on stories, never on
  ad-hoc tasks outside a story.
- A story may only be implemented via `transition(story, "start")` and
  completed via `transition(story, "finish")`. Never merge to develop yourself.
- Test names must reference the spec ids they prove (e.g. `TestFoo_UT_3`
  proves UT-3).
- Build the binary with `go build -o bin/trellis ./cmd/trellis` if
  `bin/trellis` is missing.
