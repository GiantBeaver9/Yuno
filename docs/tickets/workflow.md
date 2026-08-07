# workflow — nodes + edges as data; route on decision

## Unit
workflow

## Package / Owned files
`internal/workflow/*.go`

## Deps
(none beyond store — leaf)

## Tier
standard

## Interfaces
```go
package workflow

import (
	"context"
	"errors"

	"github.com/giantbeaver9/yuno/internal/store"
)

// ErrNoRoute is returned by Route when no edge matches (workflow, from, decision).
var ErrNoRoute = errors.New("workflow: no matching edge")

type Workflow struct {
	ID         int64
	Name       string
	IsTemplate bool
}

type Node struct {
	WorkflowID int64
	NodeKey    string // node.node_key
	AgentID    int64  // node.agent_id (→ agent.id)
	IsEntry    bool
}

type Edge struct {
	WorkflowID int64
	FromNode   string // edge.from_node (a node_key)
	ToNode     string
	OnDecision string // approve | reject | complete
}

type Store struct{ /* q store.Querier */ }

func New(q store.Querier) *Store

func (s *Store) CreateWorkflow(ctx context.Context, name string, isTemplate bool) (Workflow, error) // INSERT ... RETURNING id
func (s *Store) GetWorkflow(ctx context.Context, id int64) (Workflow, error)
func (s *Store) AddNode(ctx context.Context, n Node) error // PK (workflow_id, node_key)
func (s *Store) AddEdge(ctx context.Context, e Edge) error // PK (workflow_id, from_node, on_decision)
func (s *Store) Nodes(ctx context.Context, workflowID int64) ([]Node, error)
func (s *Store) Edges(ctx context.Context, workflowID int64) ([]Edge, error)

// EntryNode returns the node with is_entry=true. Zero or >1 entries → error.
func (s *Store) EntryNode(ctx context.Context, workflowID int64) (Node, error)

// Route reads the single edge keyed (workflow_id, from_node, on_decision) and
// returns its to_node. No such edge → ErrNoRoute. The orchestrator calls this
// knowing NOTHING about roles — it routes on the decision enum only (ADR-15).
func (s *Store) Route(ctx context.Context, workflowID int64, fromNode, decision string) (toNode string, err error)
```

## Accept
- `Route` returns the `to_node` of the edge matching `(workflow_id, from_node, on_decision)`; the reject-loop `reviewer → coder on: reject` is a single edge row that `Route(wf,"reviewer","reject")` resolves to `"coder"`.
- `EntryNode` returns the unique `is_entry=true` node.
- `AddEdge` enforces the PK `(workflow_id, from_node, on_decision)` — one decision per from-node maps to exactly one destination.
- All reads scope by `workflow_id`; no cross-workflow leakage.

## Test cases
All DB tests use `testutil.NewDB`. `node.agent_id` → `agent.id` (no ON DELETE) so tests must insert agent rows first (raw INSERT into `agent` is fine).
- **[positive]** Create a workflow, insert `coder`/`reviewer` nodes (coder `is_entry`), add edges coder→reviewer `on complete` and reviewer→coder `on reject`; `Route(wf,"reviewer","reject")` == `"coder"`, `Route(wf,"coder","complete")` == `"reviewer"`, `EntryNode(wf).NodeKey` == `"coder"`.
- **[negative]** `Route(wf,"coder","reject")` when no such edge exists → `ErrNoRoute`; `AddNode` with an `agent_id` that has no `agent` row → FK error.
- **[edge]** Two edges from the same node on different decisions (`reject`→coder, `approve`→deployer) coexist under the PK; `EntryNode` on a workflow with no `is_entry` node → error.

## Salvage
Net-new (§18 step 3). LIFT the shared-scanner idiom (events.go:35-57) for Node/Edge scanning; PORT the CRUD scan/readback shape (store.go:33-182) with `?`→`$n` and `LastInsertId`→`RETURNING`.
