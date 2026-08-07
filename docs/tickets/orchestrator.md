# orchestrator — the worker loop (claim → run → route → atomic ack)

## Unit
orchestrator

## Package / Owned files
`internal/orchestrator/*.go`

## Deps
bus, workflow, runner, agents

## Tier
hard

## Interfaces
```go
package orchestrator

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/bus"
	"github.com/giantbeaver9/yuno/internal/runner"
	"github.com/giantbeaver9/yuno/internal/workflow"
)

type Orchestrator struct {
	/* Pool *pgxpool.Pool; Bus *bus.Bus; Workflows *workflow.Store;
	   Agents *agents.Store; Runner runner.Runner; Lease time.Duration */
}

func New(
	pool *pgxpool.Pool,
	b *bus.Bus,
	wf *workflow.Store,
	ag *agents.Store,
	r runner.Runner,
	lease time.Duration,
) *Orchestrator

// Step processes at most one message:
//   1. Bus.ClaimNext(lease) — nothing claimable → (false, nil).
//   2. Resolve the target agent from to_ref ("agent:<id>") and its workflow node.
//   3. Load the run; increment run.iterations. If iterations >= run.max_iterations,
//      park the run needs_human (no enqueue) and ack the input — return (true,nil).
//   4. Assemble a runner.TurnRequest and Runner.Run.
//   5. Route(workflow, currentNodeKey, result.Decision) → next node's agent.
//   6. In ONE tx: bus.Ack{ mark input done, enqueue the next node's message
//      (to_ref agent:<nextAgentID>, decision+summary carried), push stack
//      breadcrumb, upsert dict }. A decision with no matching edge → terminal:
//      ack input, no enqueue, mark run done.
// Returns (true, nil) when a message was processed.
func (o *Orchestrator) Step(ctx context.Context) (bool, error)

// Run loops Step until ctx is done, backing off briefly when idle. It also
// periodically calls Bus.ReclaimExpired to recover crashed leases.
func (o *Orchestrator) Run(ctx context.Context)
```

Notes for the builder:
- **Node resolution.** The current node's `node_key` is resolved by matching the
  claimed message's `to_ref` agent id against the workflow's nodes (helper
  `resolveNode(workflowID, agentID) (nodeKey, error)`). For the seeded linear/
  reject workflows each agent appears once, so this is unambiguous — document the
  one-agent-per-node assumption with a `// NOTE:`.
- **The orchestrator knows nothing about roles** — it routes on the `decision`
  enum only (ADR-15). Coder vs reviewer is entirely in the edges.
- Uses `runner.FakeRunner` in tests to drive the workflow deterministically.

## Accept
- `Step` claims one queued message, runs the turn, routes on `decision`, and in a single transaction acks the input + enqueues the next node's message + pushes the stack breadcrumb + upserts dict (delegating to `bus.Ack`).
- A `decision` with no matching edge terminates the run cleanly (input acked, run marked done, nothing enqueued).
- Loop guard: each step increments `run.iterations`; when it reaches `run.max_iterations` the run is parked `needs_human` and no further message is enqueued.
- `Run` drains the queue to completion for a finite workflow and recovers expired leases.

## Test cases
All DB tests use `testutil.NewDB` and a `runner.FakeRunner`. Seed project→build→ticket→run plus a workflow (via the seed or workflow unit).
- **[positive]** 2-node linear workflow coder→reviewer (coder emits `complete`→reviewer, reviewer emits `approve`→terminal), FakeRunner scripted `[{Decision:"complete"},{Decision:"approve"}]`. Enqueue the entry message to coder, then loop `Step` until it returns `(false,nil)`: the run ends terminal, `message` rows show coder then reviewer, `stack` has two breadcrumbs, all input rows are `done`.
- **[negative]** FakeRunner returns a decision with no matching edge (e.g. `reject` where no reject edge exists) → the input is still acked (not lost / not re-looped forever) and the run does not enqueue a phantom next message.
- **[edge]** Reject loop (reviewer→coder on reject) with `run.max_iterations=2` and a FakeRunner that always returns `reject` → after 2 iterations the run is parked `needs_human` and no further message is enqueued (loop guard fires, paid APIs protected).

## Salvage
Net-new (§18 steps 2–4 — the graded spine). Composes bus (claim + atomic ack), workflow (Route/EntryNode), runner (Run), agents (turn assembly). No orchestration precedent in salvage.
