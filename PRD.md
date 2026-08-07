# Yuno — PRD & Build Handoff

**Single source of truth.** This one document is the spec *and* the build directive. If
you are the agent picking this up: read this top to bottom, then build in the order in §18.
The `salvage/*/PORT.md` files are deep-dives you consult per step; this PRD is the map.

---

## ▶ KICKOFF — builder's standing order (read first, then GO)

You are the builder. Your job is to turn this PRD into **compiled, runnable code**. Start now.

- **Just start.** Begin at §18 step 0 (scaffold) and work the sequence in order. Do **not** ask
  for approval on scaffolding, boilerplate, dependencies, or ordinary edits — you are
  pre-authorized for all of it. Commit per step; push to your branch.
- **Reuse first.** Before writing a subsystem, open the matching `salvage/*/PORT.md` and
  LIFT/PORT what's marked reusable. Only the items flagged NET-NEW (§20) are written from
  scratch.
- **Compiles or it didn't happen.** After every step, `go build ./...` must pass. Green build
  is the floor, not the goal. Write tests for the critical paths (agent creation, workflow
  execution, message delivery) and keep them passing.
- **Done = the demo runs.** `docker compose up` stands the platform up locally; a 2-agent
  workflow executes a real task end-to-end (tools called, messages exchanged, conclusion
  reached); a human can chat an agent via Telegram. That's the 40%. Build toward it; if time
  is short, degrade gracefully (§20) but keep the end-to-end path real.
- **Decisions are frozen (§3).** If reality contradicts the PRD, pick the most reasonable
  option consistent with the house rules, leave a `// NOTE:` and keep moving. Only stop for a
  human if you are truly blocked — do not stall on judgment calls.
- **Mind the credit card.** Real model APIs cost money and a human is watching the meter.
  Build and respect the guardrails (§15); never let a loop run unbounded; default to the
  cheapest model that works and only escalate where the demo needs it.

Everything below is the detail behind this order.

---

## 0. How to use this doc (builder, read this first)

- **Mission:** ship the platform in §1, demoable end-to-end, in ~2 days.
- **What exists:** this PRD, plus `salvage/` — reference code mined from the owner's prior
  repos, each with a `PORT.md` marking every piece **LIFT** (use ~as-is) / **PORT** (adapt) /
  **SUPERSEDED** / **SKIP**. There is **no source scaffold yet** — building it is step 0.
- **Decisions are frozen.** §2–§17 are settled. Do **not** re-litigate them; implement them.
  If reality forces a change, note it and keep moving — don't stall.
- **House rules are in §3.** Follow them; they're load-bearing, not decoration.
- **Definition of done is §20.** Build toward the demo (40% of the grade), not feature count.

---

## 1. What we're building

A platform where users create AI agents, configure them, and wire them into collaborative
multi-agent workflows. Agents run on a **real runtime (Goose)**, execute **real tools (MCP)**,
communicate over a **persisted message bus**, and at least one is reachable by a human through
**Telegram**. A web UI manages and monitors everything live.

**Headline feature (the flex):** a **Factory agent** — hand it a key and a description and it
writes a new agent's definition and brings it online live. Agents spawning working agents,
on screen.

---

## 2. Stack (locked)

| Layer | Choice | Why |
|---|---|---|
| Runtime | **Goose** (headless `goose run`) | Provider-agnostic, MCP tools, real execution — not our own loop. |
| Backend | **Go** | Reuse prior Go; goroutine+channel bus; single static binary = one-command local. |
| DB | **Postgres, hand-rolled SQL (pgx), NO ORM** | Boilerplate is cheap to generate; ORM is an abstraction + perf tax we don't want. |
| Frontend | **React** | SSE for live stream, REST for commands. |
| Providers | **Gemini (native) + Hugging Face (OpenAI-compatible)** | BYO-key per agent, entered in UI, encrypted at rest. |
| Channel | **Telegram** | Simplest Bot API of the three; required by spec. |

Runs fully local with **one command** (`docker compose up`); model is a remote API the user
keys. "Local" = the platform runs on your box, not the model.

---

## 3. House rules (principles)

- **Everything is a message on the bus.** Human (Telegram), agent-to-agent, cron wake, and the
  Factory are all just *producers* of rows. No special code paths.
- **Reads are free, writes are costly.** Costly writes (spawns, spend, git pushes) are gated by
  guardrails and wrapped in transactions. Reads fan out freely, unguarded.
- **Configurable, not hardcoded.** The orchestrator knows nothing about "coders" or
  "reviewers" — it reads a `decision` enum and follows a matching edge. Pipelines are data.
- **Surrogate keys for joins, natural keys for addressing.** `build.id` (surrogate PK) +
  `build_number` (human). Same as `run.guid`.
- **An entity earns a table only if it owns attributes/relationships no existing entity has.**
- **No manure.** No speculative structure. Extensibility = swap an impl, not rebuild half of
  it. Build the seam, not the future feature.
- **Don't re-litigate settled decisions.** This doc is the record.

---

## 4. Architecture

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

## 5. Data model

Hierarchy: **project → build → ticket → run(guid) → messages / stack / dict.**

- **project** = the PRD/intent. Durable, **shared** across attempts.
- **build** = *one attempt* at a project. 172 attempts = 172 build rows, same `project_id`,
  fully isolated (own tickets/runs/artifacts/branch). You do **not** mint new project IDs to
  rebuild. Earns its table: attempt-level isolation over a shared PRD.
- **ticket** = a unit of work **within a build**. Build-relative; `(project, build, ticket)` is
  unique. The **build is the idempotency unit** — clean redo = new build.
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

**Addressing.** `from_ref`/`to_ref` unified: `agent:<id>`, `human:telegram:<chatid>`,
`system:schedule`. Role is never in the payload — resolved through `agent_roles`.

**Memory is not a separate subsystem** — it's whatever lives under a `guid` (§8).

---

## 6. The bus — messages table as queue *and* trail

One table is queue and persisted history. **Guaranteed delivery — no optimistic pop:**

1. Worker claims a `queued` row → `processing`, sets `lease_until`.
2. Runs the turn.
3. In **one transaction**: write output message(s), push `(summary, ticket_id)` to `stack`,
   upsert `dict`, ack input → `done`.
4. Crash mid-turn → lease expires → row reverts to `queued` → retried.

At-least-once; ack and result commit atomically.

---

## 7. A "turn" — disposable `goose run`

Dequeue → assemble context (recipe system prompt + memory under the guid + relevant prior
messages) → `goose run` once, headless (`GOOSE_MODE=auto`, `MAX_TURNS`) → capture output →
enqueue result for the next node. Process is **disposable**; **DB is the single source of
truth**, not Goose's session store. Each turn isolated and reproducible (actor model).

---

## 8. Routing + memory

**Verdict:** every agent returns `decision (approve|reject|complete)` + `summary` (short
string). Orchestrator reads **only** `decision`, follows the matching `edge`. Reject-loop =
`reviewer → coder on: reject`, one row.

- **stack** = ordered `(summary, ticket_id)` breadcrumbs — the deterministic trace, walked
  backward to debug/replay. In-memory (hot) **and** DB, tagged with `run.guid`.
- **dict** = `guid → full_detail`, hydrated on lookup. A bounced message arrives as
  "ticket #42, prior code, exact feedback, history" — never a context-free "no".

**Memory = the guid handle.** No separate working-memory concept.
- `GET /agents` returns agents + tickets + guids (filter: `/agents?projectid=…&ticketid=…`).
- Spawn **no guid** → fresh guid → blank slate. Spawn **with guid** → reads everything under
  it → no amnesia. Failure recovery = respawn with the dead agent's guid.
- Exposed as an **MCP memory tool** (`get`/`set`/`search`), guid-scoped, on every agent by
  default. `create_agent` takes an optional `guid`.

---

## 9. Tools — MCP extensions

Every tool = a Goose MCP extension. Per-agent "tool access" = which extensions the recipe
enables (a real switch).

- **Built-in:** Coder/Reviewer/Deployer ride Goose's **Developer extension** (shell, file,
  tests). Real execution, zero code.
- **Custom (we write):** `create_agent`, `memory` — small stdio MCP servers. Goose plays the
  host role LM Studio played in prior work: it spawns each server as a subprocess over stdio.
  Use **`github.com/mark3labs/mcp-go`** + `server.ServeStdio`. Complete distilled skeleton
  (real API, cheat sheets, launch config, gotchas) in **`salvage/mcp-skeleton/MCP-SKELETON.md`**,
  copied from an MCP server the owner already wrote. Fill-in-the-blanks, not new territory.

Bare-bones for the demo; README + walkthrough sell the ceiling.

---

## 10. Factory agent — the flex

One agent whose recipe enables a custom MCP server:
`create_agent(name, provider, model, key, tools, prompt, guid?)` → hits the backend → writes
the new agent's **recipe file** (its "code") + DB row + **encrypted key** → live on the bus.

**One endpoint, two callers:** the UI "new agent" form and the Factory tool hit the *same*
endpoint. No self-modifying magic. The tool is a validated function (whitelisted fields,
known agents dir) — never arbitrary shell.

---

## 11. Interaction rules, approval, loop safety — one mechanic

Agent mode: **`auto`** (acts, moves on) or **`approval`** (output parks `pending_approval`,
run halts). Human clears via one endpoint (UI *or* Telegram reply) → state flips → worker
resumes. **Loop guard** is the same: hit `run.max_iterations` → park `needs_human`. Approval
and runaway-loop are the same halt → wait → resume machinery.

---

## 12. GitHub integration — real PRs (PAT)

**project → repo, build → branch** (`build/<build_number>`). `workspaces/<build_id>/` is a
clone checked out to that branch; Goose's Developer extension commits/pushes there. 172
attempts = 172 branches, diffable.

Routing enum = PR lifecycle: Coder commits/pushes; reject-loop = new commits + Reviewer
feedback threaded; approve → **Deployer opens/merges the PR**. **PR always created** — `auto`
merges immediately with approve recorded, `reviewed` waits. Demo runs *reviewed* for the money
shot; YOLO auto-merge is the graceful degrade.

**Token hygiene (graded):** fine-grained PAT scoped to the one repo, decrypted only at git-op
time, injected via env/credential helper, never written to the clone, never logged.

---

## 13. Telegram — a transport on the bus

Required by spec (4×; demo must show it). Thin adapter: inbound → write row
`from_ref: human:telegram:<chatid>`; outbound → post back to that chat. Human is a peer on the
bus. **Lift the Go transport from `salvage/pi-server/reference/telegram/bot.go`** (raw Bot API,
no lib) — see that PORT map. Fix the UTF-16 chunking gotcha before shipping.

---

## 14. Live monitor — SSE, three layers (NET-NEW)

1. **Durable log** = `messages` table + monotonic `seq` (no separate event store).
2. **Live tail** = Postgres `LISTEN/NOTIFY` in the **same transaction** as the message insert.
   Works multi-instance from day one; send the id, hydrate from the row (NOTIFY ~8KB cap).
3. **SSE replay** = on connect the browser sends `Last-Event-ID`; handler replays `seq > last`
   from the table, then attaches to the tail. Reconnect-proof, gap-free.

**Seam:** worker + SSE talk to a one-method `Publisher` interface; NOTIFY is the default impl.
Redis/NATS is a later drop-in, no rewrite. SSE for the stream, REST for commands. *No salvage
exists for this — build fresh.*

---

## 15. Guardrails — gate the costly writes

Checks on the write/spawn path: **cost** (accumulate tokens/cost per run/build → check
`agent.max_cost` + build cap → over → park `needs_human`); **rate** (token-bucket per agent);
**blocked actions** (coarse = which extensions are enabled; fine-grained = policy check inside
the custom MCP server). Every breach → halt → notify → human raises/kills → resume.
Config on the agent: `max_cost`, `rate_limit`, `blocked_tools[]`.

---

## 16. Schedules — a third message source

`schedule(agent_id, cron_expr|interval, next_run_at, payload, enabled)`. One ticker goroutine
polls `next_run_at <= now` (cheap read) → enqueues a wake message `from_ref: system:schedule` →
recomputes `next_run_at`. Normal machinery + guardrails follow. **Lift the ticker+fire-once
pattern from `salvage/pi-server/reference/telegram/reminders.go`** (do the immediate first
pass). For real cron beyond fixed intervals, add `robfig/cron` — the salvaged recurrence engine
only does 4 fixed intervals.

---

## 17. Sequence flows

- **Task run:** enqueue task → Coder `goose run` → commit/push → `complete` → Reviewer →
  `approve` → Deployer → open/merge PR → terminal.
- **Reject loop:** Reviewer `reject` + feedback → edge back to Coder → Coder re-runs with ticket
  context (prior code + feedback from dict) → new commit → Reviewer. Bounded by `max_iterations`.
- **Telegram in:** DM → adapter writes `human:telegram:…` row → bound agent → `goose run` →
  reply row → adapter posts to chat. Same live trail as agent-to-agent.
- **Factory spawn:** message Factory with key + description → `create_agent` MCP tool → backend
  writes recipe + row + encrypted key → new agent live, visible in UI.

---

## 18. Build sequence (do it in this order; salvage mapped per step)

Sequenced so you're demoable at every checkpoint.

| # | Step | Salvage / net-new |
|---|---|---|
| **0** | **Scaffold**: `go.mod`, package layout, `docker-compose.yml` (backend + Postgres), Makefile, React app shell, single-binary embed. | LIFT `salvage/pi-server/reference/` — `web/embed.go`, `main.go` (graceful shutdown), `config/config.go`, `api/api.go` (router+helpers), `Dockerfile`/`Makefile`. **Add a `postgres` service** (Pi repo was SQLite, no compose). |
| 1 | **De-risk Goose**: backend spawns `goose run` headless against Gemini/HF, returns output. *Prove before anything else.* Verify HF via its OpenAI-compatible endpoint. | NET-NEW. |
| 2 | **Bus + persistence**: messages table, lease claim, atomic ack (one tx), worker loop. | NET-NEW (lease queue + tx have no salvage). DB *patterns* (scanner idiom, upsert, `RETURNING`) in `salvage/pi-server/PORT.md §3`. **SQLite→Postgres gotchas there.** |
| 3 | **Orchestrator**: workflow nodes/edges as data; route on `decision`; stack/dict. | NET-NEW. |
| 4 | **2-agent linear workflow completing a real task in logs.** ← spine alive (the 40%). | — |
| 5 | **SSE live monitor** (§14). Build the observability window; watch everything after through it. | NET-NEW (no SSE anywhere in salvage). |
| 6 | **Guardrails** (§15) — wrap the runner before loops burn paid APIs. | NET-NEW. |
| 7 | **GitHub** (§12): repo/branch, commit/push, PR per ticket, PAT hygiene. | NET-NEW (no secret encryption in salvage). |
| 8 | **MCP servers**: `create_agent` + `memory` (stdio, `mcp-go`). | LIFT the skeleton — `salvage/mcp-skeleton/MCP-SKELETON.md`. |
| 9 | **Factory agent** (§10) — recipe enabling `create_agent`. | Builds on step 8. |
| 10 | **Telegram** (§13) — transport onto the bus + human-in-the-loop. | PORT `salvage/pi-server/reference/telegram/bot.go`. Fix UTF-16. |
| 11 | **Reject loop + 2 templates + memory + per-agent model routing.** | Templates = seed rows (`salvage/pi-server` `seed.go` pattern). |
| 12 | **Schedules** (§16) — additive. | PORT `reminders.go` ticker. |
| 13 | **README** (arch diagram + setup + runtime justification + "add a template/channel") + record demo. | — |

**Scope cut:** no drag-drop node editor. "Configurable not hardcoded" is graded on the engine
reading edges as data — a form/JSON editor + a read-only rendered DAG gets ~90% of the credit.

---

## 19. Salvage index

| Path | What it is | Use for |
|---|---|---|
| `salvage/mcp-skeleton/` | Real `mcp-go` stdio server (owner's) + distilled `MCP-SKELETON.md` | Step 8 — `create_agent`/`memory`. **The highest-value salvage.** |
| `salvage/pi-server/` | Go: raw Bot API Telegram + ticker scheduler, stdlib HTTP scaffold, single-binary embed, graceful shutdown, config, hand-rolled-SQL patterns, recurrence engine, Docker/systemd. SQLite. | Steps 0, 2, 10, 12. See its `PORT.md` for LIFT/PORT/SUPERSEDED per file + SQLite→Postgres gotchas. |
| `salvage/telegram/` | Python personal bot (superseded by pi-server's Go transport) | UTF-16 splitter + allowlist patterns only. Mostly superseded. |

---

## 20. Definition of done

| Criterion | Weight | Met by |
|---|---|---|
| Working end-to-end demo | 40% | §6–8, §12, §17 — real Goose execution, real PRs, real Telegram chat; 2+ agents pass messages, call tools, reach a conclusion; human chats in via Telegram. |
| Architecture & code quality | 30% | §3–5 — every config knob on a real primitive; layered separation (UI / runtime / persistence); token hygiene; tests on agent creation, workflow execution, message delivery. |
| UI/UX & configurability | 20% | §8, §10, §14 — Factory agent, live monitor, per-agent model/tools/guardrails. |
| Documentation | 10% | README: arch diagram, setup, runtime justification, how to add a template / messaging channel. |

**Net-new inventory (no salvage precedent — build fresh):** MCP *servers* (skeleton exists,
tools don't), SSE live-monitor stack, encrypted secrets, lease-based queue, DB transactions /
atomic ack, cron beyond fixed intervals.

**The demo is 40%.** Build toward it. If time runs short, degrade gracefully (YOLO auto-merge,
single-vote instead of loops) but keep the end-to-end path real.
