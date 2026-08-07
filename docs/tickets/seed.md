# seed — count-guarded workflow templates + coder/reviewer agents + reject loop

## Unit
seed

## Package / Owned files
`internal/seed/*.go`

## Deps
agents, workflow

## Tier
simple

## Interfaces
```go
package seed

import (
	"context"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

// Seed is count-guarded and idempotent: if template workflows already exist it
// is a no-op. Otherwise it creates 2 workflow templates, their coder + reviewer
// agents (seeded as agent rows — ADR-16, templates ship their agents), the nodes
// bound by agent:<id>, and the edges including the reject-loop row
// (reviewer → coder on: reject).
func Seed(ctx context.Context, ag *agents.Store, wf *workflow.Store) error
```

## Accept
- On an empty DB, `Seed` creates 2 `is_template` workflows, a coder and a reviewer agent, nodes binding each node to its `agent:<id>`, and the edges: `coder → reviewer on complete`, `reviewer → coder on reject`, `reviewer → <approve target> on approve`.
- The coder node is the entry node (`is_entry=true`).
- `Seed` is count-guarded: a second call detects existing templates and makes no additional rows.

## Test cases
All DB tests use `testutil.NewDB`.
- **[positive]** `Seed` on an empty DB → `workflow` has 2 `is_template=true` rows; a coder and reviewer `agent` exist; nodes and edges exist for at least one template.
- **[negative / idempotent]** Call `Seed` twice → after the second call the workflow count, agent count, and edge count are unchanged (count guard fires, no duplicates, no PK violation).
- **[edge]** The reject edge specifically: `SELECT ... FROM edge WHERE on_decision='reject'` resolves `reviewer → coder`; the coder node has `is_entry=true`.

## Salvage
PORT the count-guarded seed pattern from `salvage/pi-server/reference/db/seed.go` (seed.go:8-53). Persistence goes entirely through the agents + workflow units (no direct SQL here).
