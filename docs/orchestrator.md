# orchestrator — the worker loop

The orchestrator is the graded spine (PRD §6-8, §17; ADR-13/14/15/20): the
2-agent workflow that executes a real task end to end. It owns
`internal/orchestrator/` only and composes four built subsystems — `bus`
(claim + atomic ack), `workflow` (Route / nodes), `runner` (Run), and `agents`
(turn assembly).

## The turn lifecycle

`Step(ctx) (processed bool, err error)` runs exactly one claim→run→route→ack
cycle. `Run(ctx)` is a thin loop over `Step` that pauses when idle and
periodically calls `Bus.ReclaimExpired`.

1. **Claim** (short tx). Begin a transaction, `bus.New(tx).ClaimNext(ctx,
   lease)`, commit. The oldest queued row is leased via `FOR UPDATE SKIP
   LOCKED`, so concurrent workers never claim the same one. It is now
   `processing` with a lease. A nil claim means nothing to do → `(false, nil)`.
2. **Resolve the current node.** The claimed message's `to_ref` is
   `agent:<id>`. Load the run row (its `workflow_id`, `status`,
   `max_iterations`, `iterations`), read the workflow's nodes, and find the node
   whose `agent_id` equals that id. NOTE: this assumes **one agent per node** —
   true for the seeded linear/reject templates, so the match is unambiguous.
3. **Loop guard (ADR-20 halt → park).** If the run has already taken
   `max_iterations` turns (`iterations >= max_iterations`), do **not** run
   another paid turn: set `run.status = 'needs_human'`, `bus.Ack` the input with
   **no outputs** (retire it + push a park breadcrumb), commit, return
   `(true, nil)`. Checking the guard *before* the runner protects the paid API.
4. **Run the turn.** Assemble a `runner.TurnRequest` from the agent config
   (`recipe_path`, `model`, `prompt`) plus the message content, and call
   `Runner.Run`. Get back `{Decision, Summary, ...}`. Tests inject
   `runner.FakeRunner` to script the verdicts deterministically.
5. **Route on decision.** Call `workflow.Route(workflowID, currentNode,
   decision)`. The orchestrator knows nothing about roles (ADR-15) — coder vs
   reviewer lives entirely in the edges; it routes on the `decision` enum only.
   - A matching edge → the next node, whose agent becomes the hand-off target.
   - `ErrNoRoute` (no matching edge) → this decision is **terminal**.
6. **Atomic ack** (one tx). Begin a transaction, increment `run.iterations`
   (and set `run.status = 'done'` when terminal), then `bus.Ack` on the same tx:
   - **routes onward** → `Outputs` is one message `to_ref: agent:<nextAgentID>`,
     `from_ref: agent:<currentAgentID>`, carrying the summary + decision, plus a
     stack breadcrumb and a dict upsert;
   - **terminal** → no outputs; the run is marked `done` and only the input is
     acked. Commit. The whole acknowledgement commits or not at all.

## Crash recovery (at-least-once)

A crash between claim (step 1) and ack (step 6) leaves the row `processing` with
a lease. When the lease expires, `Bus.ReclaimExpired` (swept periodically by
`Run`) reverts it to `queued` and it is claimed again — at-least-once delivery.
`bus.Ack` only retires a row still in `processing`, guarding double-ack.

## Loop guard as halt → park

Without a guard, a reject loop (reviewer→coder on `reject`) with an agent that
always rejects would enqueue forever and bill paid APIs indefinitely. ADR-20's
halt is realised here: once a run reaches `max_iterations` turns, the next claim
**parks** it `needs_human` and stops enqueuing. The queue is bounded and the run
waits for a human instead of spinning.

## Test coverage

- **[positive]** money shot — coder→reviewer linear workflow, FakeRunner
  `[complete, approve]`. Driving `Step` to exhaustion: the coder's input is
  acked `done`, a message flows coder→reviewer carrying `complete`, the run ends
  `done`, and a stack breadcrumb is recorded for each of the two turns.
- **[negative]** loop guard — reject loop with `max_iterations=2` and a
  FakeRunner that always rejects parks the run `needs_human` at the cap, with a
  bounded message count and nothing left queued.
- **[edge]** an empty queue returns `(false, nil)`; a terminal decision with no
  matching edge ends the run `done` without emitting a phantom output message.
