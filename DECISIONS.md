# Yuno — Decision Record

Every load-bearing decision, the alternative it beat, and the reason. This is the
"discuss tradeoffs" companion to `PRD.md`. Format: **Decision → Alternative(s) → Why.**

Six principles thread through all of them: *everything is a message on the bus*; *reads are
free, writes are costly*; *configurable, not hardcoded*; *surrogate keys for joins, natural
keys for addressing*; *an entity earns a table only if it owns something no existing entity
does*; *no manure* (no speculative structure; extensibility = swap an impl, not a rewrite).

---

## Runtime & stack

**ADR-1 — Agent runtime: Goose.**
*Alternatives:* OpenClaw, OpenCode.
*Why:* Goose is provider-agnostic (drives Gemini/HF/local), tools are MCP extensions, and it
runs headless (`goose run`) so a backend can drive it. OpenClaw ships scheduler/memory/channels
free but risks cloud-lock and an opinionated channel model; OpenCode is strong but narrows to
code-only. Goose keeps the runtime real (spec: "must actually execute the agent logic") without
locking us in. The prior LM-Studio host role maps cleanly onto Goose.

**ADR-2 — Models: remote API (Gemini + Hugging Face), BYO-key per agent, encrypted at rest.**
*Alternatives:* local LLM (Ollama/LM Studio); one platform-wide key in `.env`.
*Why:* Frontier APIs are far steadier at multi-step tool calls than a local 7B, and the demo is
40%. "Fully local" is satisfied by the *platform* running locally (`docker compose up`), not the
model. Per-agent keys entered in the UI make "configurable dimensions per agent" vivid and lean
into the Factory flex; encryption keeps a reviewer happy.

**ADR-3 — Backend language: Go.**
*Alternatives:* Python, C#.
*Why:* The backend is orchestration + I/O + subprocess + HTTP — zero ML compute — so an
interpreted runtime (Python) is pure tax when the LLM is an API call away. Go wins over C# on
three stacked points: existing reusable Go, a single static binary for one-command local, and
goroutines/channels being the natural fit for the bus. C# was the pick only if EF Core's
migrations mattered more than that — they didn't (see ADR-4).

**ADR-4 — Persistence: Postgres with hand-rolled SQL (pgx), no ORM.**
*Alternatives:* EF Core / any ORM.
*Why:* An ORM's entire pitch is saving boilerplate; when boilerplate is cheap to generate you're
paying an abstraction + perf tax for a problem you no longer have. Raw SQL is faster and keeps
full control of the queries.

---

## Data model

**ADR-5 — Hierarchy: project → build → ticket → run.**
*Alternatives:* project → ticket (no build level).
*Why:* Re-running the same PRD (e.g. "Notre Dame in code" ×172) needs isolated attempts that
share one spec. `build` earns its table because it owns *attempt-level isolation over a shared
PRD* — nothing else does. You do not mint new project IDs to rebuild; same `project_id`, new
`build_id`. Earlier `build` was dropped (looked like a synonym for `run`); the re-run scenario
gave it its keep.

**ADR-6 — Keys: surrogate PK + natural key, both.** `build.id` (surrogate) + `build_number`
(per-project human key); same pattern as `run.guid`.
*Alternatives:* natural composite key `(project_id, build_number)` only.
*Why:* Single-column FKs stay clean in every child table; a composite natural key drags two
columns into every join/index — the thing you regret at scale. Humans still query
`WHERE build_number=4`; machines join on `id`. Identity ≠ ordering, so renumbering never moves
identity.

**ADR-7 — Idempotency boundary at `(project, build)`; tickets are build-relative.**
*Why:* With the composite scope, ticket #1/#2 can recur in every build without collision, so no
ticket carries a global idempotency token. The **build** is the idempotency unit — a clean redo
is a new build, no ticket-level reconciliation.

**ADR-8 — Addressing: one unified scheme for agents and humans.** `agent:<id>`,
`human:telegram:<chatid>`, `system:schedule`.
*Why:* A Telegram message is then just another row on the bus — the human is a peer, not a
special case, and there's no separate "human talks to agent" code path.

**ADR-9 — Role lives in a joined `agent_roles` table, not on the agent or in the payload.**
*Alternatives:* role as an agent column / in the message payload.
*Why:* With thousands of agents and mutable, non-unique roles (a "coder" may be doing
adversarial review), identity must be stable and role must be a single normalized write resolved
by join — correct across any number of round trips, and it can't rot mid-run.

**ADR-10 — Routing context: `decision` enum + `summary`, backed by a stack + dict.**
*Alternatives:* a fat "ticket" entity accumulating all context in the message.
*Why:* A bare yes/no is useless and a fat entity is heavy. A lightweight ordered **stack**
`(summary, ticket_id)` is the deterministic trace (walk it backward to debug/replay); a **dict**
`guid → full_detail` holds the heavy payload, hydrated on lookup. Cheap index + separate store.

**ADR-11 — The stack is persisted to the DB under `run_id`/guid, not just in memory.**
*Why:* Past runs stay queryable and replayable instead of vanishing with the process — durable
footing, so you can always see where you stand rather than firing blind.

---

## Bus & execution

**ADR-12 — The bus is a `messages` table (queue + persisted trail in one).**
*Alternatives:* Redis / RabbitMQ.
*Why:* The spec demands the full conversation trail be persisted; making the queue and the trail
the same rows gives persistence for free and adds no infra to babysit at this scale.

**ADR-13 — Guaranteed delivery: lease-claim + atomic ack, no optimistic pop.**
*Why:* A message goes `queued → processing` (leased), and only `done` after the output is durably
written — ack + result commit in one transaction. Crash mid-turn → lease expires → retried.
At-least-once; nothing lost.

**ADR-14 — A turn is a disposable `goose run`; the DB is the single source of truth.**
*Alternatives:* a long-lived Goose session per agent.
*Why:* Disposable-per-turn is the actor model — isolated, reproducible, all state in our DB.
Long-lived sessions split state between Goose's store and ours, worsening observability and
coupling us to Goose internals.

**ADR-15 — Workflows are nodes + edges as data; the orchestrator routes on `decision` only.**
*Why:* This is what makes "configurable, not hardcoded" real — the engine knows nothing about
"coders" or "reviewers"; the reject-loop is one `reviewer → coder on: reject` row. Rewire from
the UI, no deploy.

**ADR-16 — Workflow nodes bind to `agent:<id>`; templates ship their agents as seed rows.**
*Alternatives:* bind by role.
*Why:* Role is non-unique and mutable (ADR-9), so role-binding is ambiguous and rots mid-run.
Agent-id is the only stable key; template portability is solved by seeding the template's agents,
not by late role resolution. (This one reversed an initial lean toward role-binding.)

---

## Tools & agents

**ADR-17 — Tools are Goose MCP extensions; custom ones are small stdio MCP servers.**
*Why:* Per-agent "tool access" maps to which extensions a recipe enables — a real switch, not a
fake dropdown. Coder/Reviewer/Deployer ride Goose's built-in Developer extension (free real
execution); `create_agent`/`memory` are servers we write with `mark3labs/mcp-go` over stdio
(the transport Goose spawns). We'd already written a real MCP server, so this is fill-in-the-blanks.

**ADR-18 — Factory agent = a `create_agent` MCP tool; one endpoint, two callers.**
*Why:* The UI "new agent" form and the Factory agent hit the *same* backend endpoint, so agents
spawning agents is a thin conversational wrapper over CRUD we build anyway — no self-modifying-code
scariness. The tool is a validated function, never arbitrary shell. It directly targets the
"time from zero to a working workflow" metric.

**ADR-19 — Memory is the guid handle, not a separate subsystem.**
*Alternatives:* a dedicated working-memory store keyed by agent.
*Why:* Everything already lives under a `guid`. Spawn with no guid → blank slate; spawn with a
guid → reads the stack/dict/tickets under it → no amnesia, and failure recovery is "respawn with
the dead agent's guid." Exposed as an MCP memory tool available to all agents. Keying by agent
would break the moment a disposable agent is respawned under a new id.

**ADR-20 — Approval, loop-safety, and cost breaches are one mechanic: halt → wait → resume.**
*Why:* `auto` vs `approval` mode, `max_iterations` runaway, and guardrail breaches all park the
run as `pending_approval`/`needs_human` and resume on a human signal via one endpoint (UI or
Telegram). No separate machinery per case.

---

## Integrations

**ADR-21 — GitHub in the core (PAT): project → repo, build → branch, PR per ticket.**
*Alternatives:* local `git init` only.
*Why:* Real PRs on real GitHub are the interview money-shot, and the routing enum becomes the PR
lifecycle (approve → merge, reject → comment + amend). 172 attempts = 172 branches, diffable.
The `auto` vs `reviewed` PR behavior is the ADR-20 interaction knob at the GitHub layer, so it's
configurable, not a global call. **PR is always created** so there's a durable trail even in
auto-merge. Fine-grained PAT, decrypted only at git-op time, never logged.

**ADR-22 — Telegram is a transport on the bus (required, not optional).**
*Why:* The spec mandates an external channel four times and says the demo must show it. Given
unified addressing (ADR-8) it's a thin adapter — inbound writes a bus row, outbound posts back —
so the human lands in the same live trail as agent-to-agent messages. Chosen over WhatsApp/Slack
for the simplest Bot API and existing code to reference.

---

## Observability & ops

**ADR-23 — Live monitor: `messages.seq` durable log + Postgres LISTEN/NOTIFY tail +
`Last-Event-ID` replay, behind a one-method `Publisher` interface. SSE, not WebSocket.**
*Alternatives:* in-process Go channels for fan-out; WebSocket.
*Why:* In-process channels drop events on restart/no-subscriber and force a rewrite to scale.
NOTIFY emitted in the same transaction as the insert can't diverge from the durable row, works
multi-instance from day one, and `Last-Event-ID` replay makes reconnects gap-free. The `Publisher`
seam means Redis/NATS is a later drop-in, not a rewrite. SSE because the feed is one-directional
(server→UI); commands go over REST.

**ADR-24 — Guardrails gate the write/spawn path only.**
*Why:* Reads are free, writes are costly — cost/rate/blocked-action checks sit right before the
expensive thing (spawn, spend, push); reads stay unguarded. Breaches reuse the ADR-20 halt→resume
path, so guardrails are a checkbox you were going to need, not overhead.

**ADR-25 — Schedules are a third message source (in-process ticker → bus).**
*Alternatives:* an external scheduler / Azure-Functions-style hosting.
*Why:* A ticker goroutine polls due schedules (cheap read) and enqueues a wake message; downstream
a cron fire is indistinguishable from a Telegram DM — both are just rows. Guardrails still apply.

**ADR-26 — One `PRD.md` as the single source of truth; scope-cut the drag-drop node editor.**
*Why:* A fresh (human or agent) builder reads one file, not a scattered spec. And "configurable
not hardcoded" is graded on the *engine* reading edges as data — a form/JSON editor plus a
read-only rendered DAG gets ~90% of the credit for a fraction of the effort, protecting the 2-day
timeline for the parts that are actually graded.
