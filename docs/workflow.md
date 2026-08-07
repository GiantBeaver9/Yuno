# workflow — nodes + edges as data

The `workflow` package stores a workflow's graph — its nodes and edges — as
plain data rows in Postgres, not as a hardcoded state machine in Go. The
orchestrator never encodes "reviewer routes to coder on reject" in code; it
asks `Route` for the next node given a decision. This is ADR-15's seam: swap
in a different workflow (different roles, different graph shape) by changing
rows, not by shipping a new binary.

## Model

- **Workflow** (`workflow` table): `id`, `name`, `is_template`. A named graph.
- **Node** (`node` table): one row per step in the graph, keyed by
  `(workflow_id, node_key)`. Each node binds a `node_key` (e.g. `"coder"`,
  `"reviewer"`) to an `agent_id` — the agent that runs when the workflow
  reaches that node. Exactly one node per workflow should have
  `is_entry = true`.
- **Edge** (`edge` table): a transition, keyed by
  `(workflow_id, from_node, on_decision)`. `on_decision` is one of
  `approve | reject | complete`. The PK means a given node can have at most
  one outgoing edge per decision — one decision always maps to exactly one
  destination — but different decisions from the same node can fan out to
  different destinations (e.g. `reviewer` on `reject` goes back to `coder`,
  on `approve` goes on to `deployer`).

## Routing

`Route(ctx, workflowID, fromNode, decision)` looks up the single edge row
matching `(workflow_id, from_node, on_decision)` and returns its `to_node`.
The caller — the orchestrator — supplies only the decision enum; it has no
knowledge of role names or graph shape. If no edge matches, `Route` returns
`ErrNoRoute` rather than panicking or defaulting, so a workflow author who
forgets to wire a decision gets a clear, catchable failure at run time
instead of the run silently stalling.

`EntryNode(ctx, workflowID)` returns the node with `is_entry = true`, the
starting point for a new run. Zero or more than one entry node is treated as
a configuration error, not resolved by convention (first row, lowest key,
etc.) — an ambiguous or missing entry point should fail loudly when the
workflow is defined, not be guessed at run time.

## Example: build-review loop

```
node:  coder (is_entry) --agent--> coder-agent
       reviewer         --agent--> reviewer-agent

edge:  coder    -[complete]-> reviewer
       reviewer -[reject]->   coder      -- the reject loop
       reviewer -[approve]->  deployer
```

`Route(wf, "coder", "complete")` → `"reviewer"`.
`Route(wf, "reviewer", "reject")` → `"coder"` (the loop back).
`Route(wf, "coder", "reject")` → `ErrNoRoute` (no such edge was defined).

## Scoping

Every read (`Nodes`, `Edges`, `EntryNode`, `Route`) filters by `workflow_id`.
Two workflows can reuse the same `node_key` values (e.g. both have a
`"coder"` node) without any cross-workflow leakage, because `node_key` and
`from_node`/`to_node` are only ever meaningful in combination with the owning
`workflow_id`.

## Foreign keys

`node.agent_id` references `agent.id` with no `ON DELETE` action — inserting
a node for a nonexistent agent fails with a foreign-key violation, and the
agent row must exist before the node is added. `node` and `edge` both
reference `workflow.id` with `ON DELETE CASCADE`, so deleting a workflow
cleans up its whole graph.
