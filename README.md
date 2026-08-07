# Yuno

A platform for creating AI agents, configuring them, and wiring them into
collaborative multi-agent workflows. Agents run on a **real runtime (Goose)**,
execute **real tools (MCP)**, communicate over a **persisted message bus**, and
at least one is reachable by a human through **Telegram**. A React UI manages and
monitors everything live.

**Headline feature — the Factory agent:** hand it a key and a description and it
writes a new agent's definition and brings it online live. Agents spawning
working agents, on screen.

> Status: under active construction from [`PRD.md`](./PRD.md) (the single source
> of truth) via a ticket-driven, test-first build. See
> [`docs/tickets/`](./docs/tickets) for the unit breakdown and
> [`build/units.json`](./build) for the dependency DAG. Design rationale for
> every load-bearing decision is in [`DECISIONS.md`](./DECISIONS.md).

---

## Architecture

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

**Core principle:** *everything is a message on the bus.* A human on Telegram, an
agent talking to another agent, a cron wake, and the Factory are all just
*producers of rows* in one `messages` table that is simultaneously the work queue
and the durable conversation trail. There is no special-case code path for any of
them.

### Data model

`project → build → ticket → run(guid) → messages / stack / dict`

- **project** — the PRD/intent; durable, shared across attempts.
- **build** — *one attempt* at a project, fully isolated (own tickets, runs,
  branch). Re-running the same PRD mints a new `build`, never a new project.
- **run** — a workflow execution; `run.guid` is the spine everything hangs off.
- **message** — the bus + trail. `id` is a global monotonic cursor (drives SSE
  `Last-Event-ID` replay); `seq` is the per-run replay cursor.
- **stack / dict** — a lightweight ordered `(summary, ticket_id)` trace plus a
  `guid → full_detail` payload store, so a bounced message arrives with full
  context, never a context-free "no".

Full DDL: [`internal/store/schema.sql`](./internal/store/schema.sql).

---

## Run it locally (one command)

```bash
cp .env.example .env         # optionally set SECRET_KEY, TELEGRAM_BOT_TOKEN, ...
docker compose up --build
```

This stands up Postgres and the Yuno backend (with the React SPA embedded) on
<http://localhost:8080>. "Local" means the *platform* runs on your box — the
model stays a remote API you key per agent.

### Develop without Docker

```bash
# 1. a Postgres reachable at DATABASE_URL (see .env.example)
# 2. build the UI into the Go embed dir, then run the backend:
make frontend
make run          # go run ./cmd/yuno   (applies the schema on boot)

# tests (needs a Postgres; point TEST_DATABASE_URL at it):
TEST_DATABASE_URL=postgres://yuno@127.0.0.1:5432/yuno?sslmode=disable make test
```

The single binary embeds the built SPA, so `go build ./cmd/yuno` produces a
self-contained server.

### Running real agents (Goose)

Agent turns shell out to **Goose** (`goose run`, headless). The Docker image
installs the Goose CLI and builds the MCP servers, so `docker compose up` is
goose-ready. To point it at a model:

1. **Set a provider key.** Either give each agent a provider + model + BYO key in
   the Factory form (encrypted at rest, decrypted only at turn time and injected
   as the right env var), or set a platform-wide default in `.env`:
   - Gemini → `GOOSE_PROVIDER=google`, `GOOSE_MODEL=gemini-2.0-flash`, `GOOGLE_API_KEY=…`
   - Hugging Face → `GOOSE_PROVIDER=huggingface`, `GOOSE_MODEL=…`, `HF_TOKEN=…`
2. **Recipes are generated for you.** When the Factory creates an agent it writes
   a valid Goose recipe (`AGENTS_DIR`): the agent's prompt + the verdict contract
   as instructions, `settings.goose_provider/goose_model`, the built-in
   `developer` extension (real shell/file/tests), and — for any agent whose tools
   include `memory` or `create_agent` — the matching custom MCP server (from
   `MCP_BIN_DIR`) wired in as a stdio extension. The per-turn input arrives as the
   recipe's `task` parameter.

Running without a key still exercises the whole path — REST → bus → orchestrator →
`goose run` (recipe + MCP extensions load) — and stops at the provider auth
boundary; a turn that can't reach the model is retried under its lease, not hot-
spun. The verdict contract every agent must emit:

```
DECISION: <approve|reject|complete>
SUMMARY: <short text>
```

The orchestration itself (routing, atomic ack, loop guard, the 2-agent hand-off)
is proven by the test suite via a fake runner and demoable with a stub that emits
the two lines above. Goose diagnostics (provider/tool errors) are surfaced in the
server log on a failed turn.

---

## Using it (REST API)

Everything is a message on the bus; the REST API and SSE stream are the write and
read sides of the same `message` rows.

| Method & path | Purpose |
|---|---|
| `GET /api/health` | liveness |
| `GET /api/agents` | list agents (+ roles, guid) |
| `POST /api/agents` | create an agent — **the same backend the Factory MCP tool calls** (`{name,provider,model,key,prompt,tools,roles,mode,...}`; provider ∈ gemini\|huggingface) |
| `GET /api/workflows` · `GET /api/workflows/{id}` | list templates · one workflow's nodes+edges |
| `POST /api/runs` | start a run on a workflow (`{workflowId,input,maxIterations}`) → enqueues the entry message |
| `GET /api/runs` · `GET /api/runs/{guid}/messages` | run status · the bus trail |
| `POST /api/runs/{guid}/messages` | inject a human message (`{toAgentId,content}`) |
| `POST /api/messages/{id}/approve` · `POST /api/runs/{guid}/resume` | clear an approval halt · lift a loop-guard halt (the one halt→resume mechanic) |
| `GET /api/stream` | SSE live monitor (`Last-Event-ID` replay + live tail) |

Quick demo (server on :8080, a Goose stub or real Goose configured):

```bash
curl -s localhost:8080/api/workflows                    # seeded: build-review, quick-review
curl -s -X POST localhost:8080/api/runs \
  -H 'content-type: application/json' \
  -d '{"workflowId":1,"input":"Build a hello endpoint","maxIterations":6}'
curl -s localhost:8080/api/runs/<guid>/messages         # watch coder -> reviewer -> done
```

---

## Why these choices (short version)

| Layer | Choice | Why |
|---|---|---|
| Runtime | **Goose** (`goose run`, headless) | Provider-agnostic, MCP tools, *real* execution — not a hand-rolled loop. Keeps "the agent logic must actually execute" honest. |
| Backend | **Go** | The backend is orchestration + I/O + subprocess + HTTP (zero ML compute); goroutines/channels fit the bus, and a single static binary makes local one-command. |
| DB | **Postgres, hand-rolled SQL (pgx), no ORM** | Boilerplate is cheap to generate; an ORM is an abstraction + perf tax for a problem we don't have. |
| Frontend | **React** | SSE for the live stream, REST for commands. |
| Providers | **Gemini + Hugging Face, BYO-key per agent, encrypted at rest** | Frontier APIs are far steadier at multi-step tool calls; per-agent keys make configurability vivid and feed the Factory. |
| Channel | **Telegram** | Simplest Bot API; the human is just another peer on the bus. |

The long form — each decision, the alternative it beat, and the reason — is in
[`DECISIONS.md`](./DECISIONS.md).

---

## Extending Yuno

Two seams the design is built around:

### Add a workflow template
Workflows are **data**, not code: rows in `workflow`, `node`, and `edge`. The
orchestrator knows nothing about "coders" or "reviewers" — it reads an agent's
`decision` enum (`approve` / `reject` / `complete`) and follows the matching
`edge`. A reject-loop is a single `reviewer → coder on: reject` row. To add a
template, seed its agents and its node/edge rows (see `internal/seed`); no engine
change, no redeploy. Rewire from the UI's form/JSON editor and the rendered DAG.

### Add a messaging channel
Channels are **transports on the bus**. Telegram is a thin adapter: inbound
messages become rows with `from_ref: human:telegram:<chatid>`; outbound rows get
posted back to that chat. To add Slack/WhatsApp/email, write the same two halves
against the unified addressing scheme (`agent:<id>`, `human:<channel>:<id>`,
`system:schedule`) — the human lands in the same live trail as agent-to-agent
messages, with no new orchestrator code.

The live monitor sits behind a one-method `Publisher` interface (Postgres
`LISTEN/NOTIFY` today); swapping in Redis/NATS is an impl swap, not a rewrite.

---

## Repository layout

```
cmd/yuno/                backend entrypoint (graceful shutdown)
cmd/mcp-memory/          custom MCP server: guid-scoped memory (stdio)
cmd/mcp-create-agent/    custom MCP server: the Factory create_agent tool (stdio)
internal/store/          pgx pool, embedded schema, migration, Querier seam
internal/config/         env-driven config
internal/api/            HTTP scaffold + shared JSON helpers
internal/bus/            the messages-table queue: enqueue, lease-claim, atomic ack
internal/workflow/       nodes/edges as data + decision routing
internal/runner/         Goose runner (shell out) behind a Runner interface (+ fake)
internal/orchestrator/   the worker loop tying bus + workflow + runner together
internal/sse/            live monitor: Publisher, NOTIFY tail, Last-Event-ID replay
internal/agents/         agent CRUD, roles, encrypted provider keys
internal/secretbox/      AES-GCM encryption for secrets at rest
internal/guardrails/     cost / rate / blocked-tool checks on the write path
internal/ghsync/         GitHub: build branches, PRs, PAT hygiene
internal/factory/        create_agent — one backend, two callers (UI form + MCP)
internal/telegram/       raw Bot API transport onto the bus (UTF-16-correct)
internal/schedule/       ticker: due schedules → wake messages on the bus
internal/seed/           count-guarded workflow-template seeding
internal/testutil/       per-test isolated Postgres schema
web/                     embedded React SPA
frontend/                React (Vite) source
salvage/                 read-only donor code + PORT.md maps (nested module)
```
