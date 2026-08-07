# seed — count-guarded workflow template seeding

The `seed` package populates a fresh Yuno database with the built-in workflow
templates so a new deployment has something runnable instead of an empty
`workflow` table. It is the only unit that wires `agents` and `workflow`
together at startup: it creates the seed agents first, then the workflow
graph that binds nodes to those agents (`agent:<id>`, ADR-16 §11).

`Seed` touches nothing but the `agents.Store` and `workflow.Store` APIs
passed in — no direct SQL, no schema knowledge. It is meant to be called once
on every process startup; it is safe to call repeatedly.

## What gets seeded

- **2 template workflows** (`workflow.is_template = true`): `"build-review"`
  and `"quick-review"`. Both share the same shape and the same two agents.
- **A `coder` agent** (role `coder`, `mode = "auto"`) and a **`reviewer`
  agent** (role `reviewer`, `mode = "approval"`).
- Each template's **nodes**: `coder` (the entry node, `is_entry = true`),
  `reviewer`, and a terminal `deployer` node. The `deployer` node is bound to
  the reviewer agent's id — this template ships no dedicated deploy agent, so
  the reviewer's agent stands in for the terminal step. Operators who clone
  the template are expected to rebind `deployer` to a real deploy agent.
- Each template's **edges** — the reject-loop from ADR-16 §11:
  - `coder --complete--> reviewer`
  - `reviewer --reject--> coder` (the loop: rejected work goes back to the
    coder, not to a dead end)
  - `reviewer --approve--> deployer`

## The reject-loop template

This is the shape ADR-16 §11 calls out by name: a coder produces work, a
reviewer either sends it back (`reject`, routing to `coder` again — the
"loop") or lets it through (`approve`, routing forward to a terminal node).
Both seeded templates use this shape today; the design leaves room for a
future template with a different graph (e.g. no review step, or a second
review stage) to be added as a third loop in `templateNames` without
disturbing the first two.

## Count-guard idempotency

`Seed` ports the count-guarded pattern from
`salvage/pi-server/reference/db/seed.go:8-53`: check whether the thing you're
about to create already exists, and no-op if so. Here the guard checks
whether an agent carrying the `coder` role already exists, via
`agents.Store.List`. `workflow.Store` deliberately exposes no "list all
workflows" query (ADR-15 callers are expected to know the workflow id they
want), so the guard can't check template workflows directly — but since
`Seed` is the only code path that creates a `coder`-role agent, and it always
creates that agent before the workflows that reference it, the coder agent's
presence is an accurate proxy for "the templates are already seeded."

Calling `Seed` twice is a no-op on the second call: no duplicate agents, no
duplicate workflows, no duplicate nodes/edges, no primary-key violation.

## How to add a template

1. Add the new workflow's name to `templateNames`.
2. If it needs a different graph than the reject-loop, factor a new
   `seedTemplateXxx` function alongside `seedTemplate` and call it instead
   for that name — `Seed`'s loop only assumes "one function builds one
   template workflow from the shared coder/reviewer agent ids."
3. If the template needs an agent beyond coder/reviewer, create it in `Seed`
   before the template loop (mirroring how coder/reviewer are created), and
   thread its id into the new seed function.

## Example

```go
box, _ := secretbox.NewFromHex(hexKey)
ag := agents.New(pool, box)
wf := workflow.New(pool)

if err := seed.Seed(ctx, ag, wf); err != nil {
    log.Fatal(err)
}

// Safe to call again on every startup — no-ops once seeded.
_ = seed.Seed(ctx, ag, wf)
```
