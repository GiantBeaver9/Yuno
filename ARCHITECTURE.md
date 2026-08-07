# Yuno — AI Agent Orchestration Platform

**Architecture spec — frozen for build.** This is the decision record from the design
session. Every choice here is deliberate; deviations should be conscious, not accidental.

---

## 0. What we're building

A platform where users create AI agents, configure them, and wire them into
collaborative multi-agent workflows. Agents run on a **real runtime (Goose)**, execute
**real tools (MCP)**, talk to each other over a **persisted message bus**, and at least
one is reachable by a human through **Telegram**. A web UI manages and monitors
everything live.

The headline feature — the interview flex — is a **Factory agent**: you hand it a key and
a description and it *writes a new agent's definition and brings it online live*. Agents
spawning working agents, on screen.

---

## 1. Stack

| Layer | Choice | Why |
|---|---|---|
| Runtime | **Goose** (headless, `goose run`) | Provider-agnostic, MCP tools, real execution. Not our own loop. |
| Backend | **Go** | Reuse existing Go (Telegram, scheduler); goroutine+channel bus; single static binary = one-command local. |
| DB | **Postgres, hand-rolled SQL (pgx)** | No ORM — boilerplate is cheap to generate, EF/ORM is an abstraction + perf tax we don't need. |
| Frontend | **React** | SSE for live stream, REST for commands. |
| Model providers | **Gemini (native) + Hugging Face (OpenAI-compatible)** | BYO-key per agent, entered in UI, encrypted at rest. |
| Channel | **Telegram** (reused Go bot code) | Simplest Bot API of the three; required by spec. |

**Runs fully local with one command.** "Local" = the platform runs on your box
(`docker compose up`); the model is a remote API you supply a key for. That still
satisfies the spec's single-command local requirement.

---

## 2. Principles (the house rules)

- **Everything is a message on the bus.** Human (Telegram), agent-to-agent, cron wake, and
  the Factory are all just *producers* of rows. No special code paths.
- **Reads are free, writes are costly.** The costly writes (spawns, spend, git pushes) are
  gated by guardrails and wrapped in transactions. Reads (SSE replay, queries, hydration)
  fan out freely, unguarded.
- **Configurable, not hardcoded.** The orchestrator knows nothing about "coders" or
  "reviewers" — it reads a `decision` enum and follows a matching edge. Pipelines are data.
- **Surrogate keys for joins, natural keys for addressing.** e.g. `build.id` (surrogate PK,
  single-column FKs) + `build_number` (human `WHERE build_number=4`). Same philosophy as
  `run.guid`.
- **An entity earns a table only if it owns attributes/relationships no existing entity
  has.** (This is why `build` exists and why it almost didn't — see §4.)
- **No manure.** Don't add speculative structure. Extensibility means *swap an impl*, not
  *rebuild half of it* — build the seam, not the future feature.

---

## 3. High-level shape

```
 [Telegram]    [Web UI]                 producers/consumers of bus rows
      \          /  \
       \        /    \ SSE (live stream)     REST (commands)
   ┌────────────────────────────────────────────────────┐
   │  ORCHESTRATOR + MESSAGE BUS  (the graded spine)      │
   │   · messages table = queue + persisted trail        │
   │   · lease-based guaranteed delivery, atomic ack     │
   │   · workflow = nodes + edges (data)                 │
   │   · stack (trace) + dict (payload), guid-keyed      │
   │   · Publisher iface → Postgres LISTEN/NOTIFY        │
   └────────────────────────────────────────────────────┘
              │  one `goose run` per agent-turn (disposable)
   ┌────────────────────────────────────────────────────┐
   │  Goose runtime — per-agent recipe + MCP extensions  │
   │   built-in Developer ext (shell/file/tests)         │
   │   custom MCP: create_agent, memory                  │
   └────────────────────────────────────────────────────┘
              │
   Gemini / Hugging Face (per-agent, BYO-key)
              │
   workspaces/<build_id>/  = clone of project repo @ build branch
```

---

## 4. Data model

Hierarchy: **project → build → ticket → run(guid) → messages / stack / dict.**

- **project** = the PRD / intent ("Notre Dame in code"). Durable, **shared** across attempts.
- **build** = *one attempt* at a project. 172 attempts = 172 build rows, same `project_id`,
  fully isolated (own tickets/runs/artifacts/branch). This is what segregates re-runs —
  you do **not** mint new project IDs to rebuild. `build` earns its table because it owns
  *attempt-level isolation over a shared PRD*, which nothing else does.
- **ticket** = a unit of work **within a build**. Build-relative — ticket #1/#2 can recur in
  every build without collision because `(project, build, ticket)` is unique. The
  **build is the idempotency unit**; a clean redo is a new build, no ticket reconciliation.
- **run** = a workflow execution; `run.guid` is the spine everything hangs off.

```sql
project(id, name, prd, repo_url)

build(id PK, project_id → project, build_number, branch_name, pr_number,
      label, status, artifact_ref, created_at,
      UNIQUE(project_id, build_number))          -- surrogate PK, human build_number

ticket(id, build_id → build, title, status)      -- FK on surrogate, single column

run(guid PK, ticket_id → ticket, workflow_id → workflow,
    status, max_iterations)                       -- guid = spine

agent(id)
agent_roles(agent_id → agent, role)               -- role via join; mutable, non-unique
provider_key(agent_id → agent, provider, enc_key) -- encrypted at rest
secret_github(project_id → project, enc_pat)      -- fine-grained PAT, encrypted

workflow(id, name, is_template)
node(workflow_id → workflow, node_key, agent_id → agent, is_entry)  -- bind by agent:id
edge(workflow_id → workflow, from_node, to_node, on_decision)       -- reject-loop = one row

message(id, run_id → run, seq, from_ref, to_ref, content,
        decision, summary, status, lease_until,
        tokens, cost, created_at)                 -- the bus + trail; seq = replay cursor

stack(run_id → run, seq, summary, ticket_id → ticket)  -- ordered trace, walk backward
dict(guid PK, full_detail)                             -- heavy payload, hydrate on demand

schedule(id, agent_id → agent, cron_expr, next_run_at, payload, enabled)
```

**Addressing.** `from_ref` / `to_ref` are unified: `agent:<id>`, `human:telegram:<chatid>`,
`system:schedule`. Role is never in the payload — it's resolved through `agent_roles` (one
write, no matter how many round trips; survives role drift because identity ≠ role).

**Memory is not a separate subsystem** — it's whatever lives under a `guid`. Continuity is
a spawn-time choice (see §8).

---

## 5. The bus — messages table as queue *and* trail

One table is the queue and the persisted history. The spec demands the full conversation
trail be persisted; making the queue and the trail the same rows gives it for free.

**Guaranteed delivery — no optimistic pop:**

1. Worker claims a `queued` row → `processing`, sets `lease_until`.
2. Runs the turn.
3. In **one transaction**: write the output message(s), push `(summary, ticket_id)` to
   `stack`, upsert `dict`, ack the input → `done`.
4. Crash mid-turn → lease expires → row reverts to `queued` → retried.

At-least-once delivery. Nothing slips through the cracks because the ack and the result
commit atomically.

---

## 6. A "turn" — disposable `goose run`

- Orchestrator dequeues a message → assembles context (recipe system prompt + memory under
  the guid + relevant prior messages) → spawns `goose run` **once**, headless
  (`GOOSE_MODE=auto`, `MAX_TURNS`) → captures output → enqueues result for the next node.
- The agent process is **disposable** — it does not persist between turns. All state lives
  in Postgres and is fed in at turn start. **The DB is the single source of truth**, not
  Goose's session store. Each turn is isolated and reproducible (actor model).

---

## 7. Routing — decision enum + stack/dict

Every agent returns a structured verdict: `decision (approve|reject|complete)` +
`summary` (short string, e.g. "resolved ticket 182, created foo.go").

- Orchestrator reads **only** `decision` and follows the matching `edge`. It knows nothing
  about roles. The reject-loop is `reviewer → coder on: reject` — one row, not an `if`.
- **stack** = ordered `(summary, ticket_id)` breadcrumbs — the deterministic trace, walked
  backward to debug/replay. Lives in-memory (hot path) **and** in the DB tagged with the
  `run guid`, so past runs are queryable/replayable.
- **dict** = `guid → full_detail`, the heavy payload, hydrated only on lookup.

So a bounced message never arrives as a context-free "no" — it arrives as "ticket #42, prior
code, exact feedback, history."

---

## 8. Memory — the guid *is* the handle

No separate working-memory concept. Everything is already stored under guids.

- `GET /agents` returns agents + their tickets + guids (filterable:
  `/agents?projectid=…&ticketid=902384`).
- **Spawn with no guid** → auto-assigned fresh guid → blank slate.
- **Spawn with a guid** → reads everything under it (stack/dict/tickets) → no amnesia.
- **Failure recovery** = respawn a fresh agent with the dead one's guid; it resumes.
- Exposed as an **MCP memory tool** (`get`/`set`/`search`) available to every agent by
  default, scoped to the current guid.

`create_agent` (§10) takes an optional `guid` param for exactly this.

---

## 9. Tools — MCP extensions

Every tool an agent can use is a Goose MCP extension. Per-agent "tool access" = which
extensions its recipe enables (a real switch, not a fake dropdown).

- **Built-in:** Coder/Reviewer/Deployer ride Goose's **Developer extension** — shell, file
  read/write, run tests. Real execution, zero code from us.
- **Custom (we write, small MCP servers):** `create_agent`, `memory`.

Build **bare-bones** for the demo; the README + live walkthrough sell the extensibility
ceiling and prior work.

---

## 10. Factory agent — the flex

One agent whose recipe enables a single custom MCP server:
`create_agent(name, provider, model, key, tools, prompt, guid?)`.

When called → hits the backend → writes the new agent's **recipe file** (its "code") +
DB row + **encrypted key** → live on the bus immediately.

**One endpoint, two callers:** the UI "new agent" form and the Factory agent's tool hit the
*same* backend endpoint. No self-modifying-code magic, no separate path. The tool is a
validated function (whitelisted fields, known agents dir) — never arbitrary shell.

---

## 11. Interaction rules, approval, loop safety — one mechanic

Each agent has a mode:

- **`auto`** — acts and moves on.
- **`approval`** — output parks as `pending_approval`; the run halts.

A human clears it via one endpoint (UI *or* Telegram reply) → state flips → worker resumes.

The **loop guard** is the same pattern: hit `run.max_iterations` → park as `needs_human`
instead of spinning. Approval and runaway-loop are literally the same halt → wait → resume
machinery.

---

## 12. GitHub integration — real PRs (PAT)

**project → repo, build → branch.** One repo per project; each build is a branch
(`build/<build_number>`). `workspaces/<build_id>/` is a **clone checked out to that
branch**. Goose's Developer extension commits and pushes there. 172 Notre Dames = 172
branches in one repo, diffable against each other.

The routing enum becomes the PR lifecycle:

- Coder commits + pushes to the build branch.
- reject-loop = new commits + Reviewer feedback threaded on the PR.
- approve → **Deployer opens/merges the ticket's PR**.

**PR is always created** — `auto` merges it immediately with the approve recorded;
`reviewed` waits. Either way there's a durable PR trail to scroll back through. (This is the
`auto` vs `approval` knob from §11 expressed at the GitHub layer — configurable, not a
global decision. Demo runs *reviewed* for the money shot; YOLO auto-merge is the graceful
degrade if time runs short.)

**Token hygiene (graded):** fine-grained PAT limited to the one repo, decrypted only at
git-op time, injected via env/credential helper, never written to the clone, never logged.

---

## 13. Telegram — a transport on the bus

Required by spec (four times; the demo must show it). Nearly free given unified addressing:
a thin adapter (reused Go code) where inbound → write a row
`from_ref: human:telegram:<chatid>`, outbound → post back to that chat. Human is a peer on
the bus, not a special case. A Telegram DM and an agent-to-agent message are the same kind
of row.

---

## 14. Live monitor — SSE, three layers

1. **Durable log** = `messages` table with a monotonic `seq` (reuses the table we already
   have — no separate event store).
2. **Live tail** = Postgres `LISTEN/NOTIFY`, emitted in the **same transaction** as the
   message insert (announce can't diverge from the durable row). Works across multiple
   backend instances from day one; satisfies fully-local. Send the id, hydrate from the row
   (NOTIFY has an ~8KB payload cap).
3. **SSE replay** = on connect the browser sends `Last-Event-ID`; handler replays
   `seq > last` from the table, then attaches to the tail. Reconnect-proof, gap-free.

**Extensibility seam:** worker + SSE talk to a one-method `Publisher` interface; NOTIFY is
the default impl. Outgrow it → drop in Redis/NATS as a new impl, worker/SSE code unchanged.
One file swap, not a rebuild.

SSE for the stream, REST for commands.

---

## 15. Guardrails — gate the costly writes

Checks on the write/spawn path, right before the expensive thing:

- **Cost:** accumulate tokens+cost per run/build → before the next spawn, check
  `agent.max_cost` (+ optional build-level cap). Over → park `needs_human`.
- **Rate:** token-bucket per agent (turns/min or max concurrent). Over → delay or park.
- **Blocked actions:** coarse = which MCP extensions are enabled (free). Fine-grained
  (allow shell, block `rm -rf` / push to `main`) = a policy check inside the custom MCP
  server. Build coarse; fine-grained is the extensible seam.

Every breach funnels to the same halt → notify → human raises limit or kills → resume.

Config on the agent: `max_cost`, `rate_limit`, `blocked_tools[]`.

---

## 16. Schedules — a third message source

Cron isn't special machinery; it's a producer.

- `schedule(agent_id, cron_expr|interval, next_run_at, payload, enabled)`.
- One ticker goroutine polls `next_run_at <= now` (cheap read) → enqueues a wake message
  `from_ref: system:schedule` → recomputes `next_run_at`.
- Normal machinery takes over; **guardrails still gate the spawn**. A cron fire is
  indistinguishable downstream from a Telegram DM — both are just rows on the bus.

(Reused from existing Telegram-bot scheduler code.)

---

## 17. Sequence flows

**Task run (happy path):** enqueue task → entry node (Coder) `goose run` → commit/push →
`decision: complete` → edge → Reviewer → `decision: approve` → edge → Deployer → open/merge
PR → run terminal.

**Reject loop:** Reviewer `decision: reject` + feedback → edge `reviewer → coder on: reject`
→ Coder re-runs with ticket context (prior code + feedback from dict) → new commit → back to
Reviewer. Bounded by `max_iterations` → park `needs_human` if exceeded.

**Telegram in:** DM → adapter writes `from_ref: human:telegram:…` row → routed to bound
agent → `goose run` (personality + memory under guid) → reply row → adapter posts to chat.
Appears in the same live trail as agent-to-agent messages.

**Factory spawn:** you message the Factory agent with a key + description → its `create_agent`
MCP tool → backend writes recipe + row + encrypted key → new agent live on the bus,
visible in the UI immediately.

---

## 18. Build order (tomorrow)

Sequenced so you're demoable at every checkpoint.

1. **Compose scaffold** + backend spawns `goose run` headless against Gemini/HF and returns
   output. *De-risk the scary integration first — prove this before anything else.*
   (Verify HF via its OpenAI-compatible endpoint here.)
2. **Bus + persistence** — messages table, lease claim, atomic ack, worker loop.
3. **Orchestrator** — workflow nodes/edges as data; route on `decision`; stack/dict.
4. **2-agent linear workflow completing a real task in logs.** ← spine alive (the 40%).
5. **SSE live monitor** — build the observability window; watch everything after through it.
6. **Guardrails** — wrap the runner before loops run on paid APIs.
7. **GitHub** — project→repo, build→branch, commit/push, PR per ticket, PAT hygiene.
8. **Factory agent** — `create_agent` MCP server (the flex).
9. **Telegram** — reused adapter onto the bus + human-in-the-loop.
10. **Reject loop + 2 templates + memory + model routing.**
11. **Schedules** — additive, bolt on last.
12. **README** (arch diagram + setup + runtime justification + "how to add a template/channel")
    + record demo.

Scope cut to protect the timeline: **no drag-drop node editor.** "Configurable not
hardcoded" is graded on the engine reading edges as data — a form/JSON editor + a read-only
rendered DAG gets ~90% of the credit for a fraction of the effort.

---

## 19. Rubric mapping

| Criterion | Weight | Where it's earned |
|---|---|---|
| Working end-to-end demo | 40% | §5–7, §12, §17 — real Goose execution, real PRs, real Telegram chat |
| Architecture & code quality | 30% | §2–4 — every config knob lands on a real primitive; layered separation; token hygiene |
| UI/UX & configurability | 20% | §8, §10, §14 — Factory agent, live monitor, per-agent model/tools/guardrails |
| Documentation | 10% | this file → README with arch diagram, runtime justification, extension guides |

**Key impact metrics:** the Factory agent directly minimizes "time from zero to a working
multi-agent workflow"; the persisted trail proves "agent-to-agent message reliability."
