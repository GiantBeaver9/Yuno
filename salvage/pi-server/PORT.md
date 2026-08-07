# Pi-home-server salvage — port map

Source: `github.com/GiantBeaver9/Pi-home-server-page` @ `e8a12aa` (default branch).
Curated frozen copies in `reference/`. Backend is Go 1.25, **SQLite** (`modernc.org/sqlite`,
CGO-free), stdlib `net/http`, Vue frontend. ~4,600 LOC; ~3,500 staged here.

## TL;DR

Strong salvage for the **plumbing**; two things I'd hoped for are **not here**:

- ✅ **Telegram transport** (raw Bot API in Go, no lib) — the best lift in the repo.
- ✅ **HTTP scaffold, single-binary embed, graceful shutdown, config helpers, Docker/Makefile/systemd** — clean, dependency-light, lift-worthy.
- ✅ **Hand-rolled-SQL patterns + a pure-Go recurrence engine** — pattern donors (SQLite, so port the shape not the SQL).
- ❌ **No real MCP wiring.** `internal/llm` is an OpenAI function-calling loop + an LM-Studio-proprietary HTTP client. No MCP Go SDK, no JSON-RPC, no stdio/SSE transport. Yuno's `create_agent`/`memory` MCP servers + Goose stdio are **net-new**.
- ❌ **No SSE / WebSocket / pub-sub anywhere.** `api/events.go` is *calendar* CRUD, not an event stream — the name is a false friend. Yuno's entire live-monitor stack is **net-new**.

## Net-new — build fresh, zero precedent in this repo

1. **Real MCP servers** (`create_agent`, `memory`) + talking to Goose over MCP — needs a Go MCP SDK (`github.com/modelcontextprotocol/go-sdk` or `mark3labs/mcp-go`).
2. **SSE live-monitor** (§14): `Publisher` interface, `seq` cursor, Postgres `LISTEN/NOTIFY` tail, `Last-Event-ID` replay, per-client fan-out.
3. **Encrypted secrets** (§10/§12): per-agent provider keys + GitHub PAT. This repo reads all tokens as **plaintext env** — no encryption to copy.
4. **Lease-based message queue** (§5): claim → `lease_until` → atomic ack. Closest analog here is a one-shot `reminded` boolean; the real queue (`FOR UPDATE SKIP LOCKED`) is new.
5. **DB transactions / atomic ack** (§5): this repo uses **no** `Begin/Commit` anywhere. The "write output + push stack + upsert dict + ack input in one tx" is net-new (pattern below).
6. **True cron**: the recurrence engine supports only 4 fixed intervals — too weak for cron. Use `robfig/cron` (cron) or `teambition/rrule-go` (RRULE) if Yuno needs more.

---

## 1. Telegram — `reference/telegram/` (best salvage)

Raw Bot API over `net/http` (no library), long-poll, offset-ack, token-redacting error path.
`reminders.go` is already ~Yuno's ticker→bus model.

| item | file | verdict | Yuno destination |
|---|---|---|---|
| Raw Bot API client (`call`, wire types) | bot.go:66-83,242-270 | **LIFT** | §13 transport base |
| Long-poll loop + offset ack + backoff | bot.go:86-128 | **LIFT** | §13 inbound → write bus row `human:telegram:<chatid>` |
| chat_id extraction | bot.go:71-77,133 | **LIFT** | maps to the peer address directly |
| Token redaction in errors | bot.go:242-278 | **LIFT** | keep — stops the token leaking via `*url.Error` |
| Typing indicator | bot.go:178 | **LIFT** | fire before enqueuing a Goose wake |
| Allowlist + discovery mode | bot.go:49-62,136-147 | **PORT** | §13 auth; source IDs from Yuno config |
| `splitMessage` chunking | bot.go:220-299 | **PORT** | §13 outbound — **counts runes, not UTF-16; fix first** (gotcha ①) |
| `reminders.go` ticker (60s + immediate first pass, fire-once) | reminders.go:14-63 | **PORT** | §16 scheduler — swap `Broadcast`→enqueue wake on bus; swap `MarkReminded`→a fire-once guard |
| `nextDailyTime` daily-at-HH:MM helper | digest.go:29-50 | **PORT** | §16, fixed-daily case, zero deps |
| In-memory chat history + trim | bot.go:24,185-212 | **SUPERSEDED** | bus/DB trail keyed by guid (§8) |
| `/ask` via `mcp.Ask`, digest LLM narrate | commands.go, digest_llm.go | **SUPERSEDED** | Goose owns LLM |
| App commands + digest assembly | commands.go, digest.go:54-327 | **SKIP** | Pi-home domain |

## 2. HTTP + infra — `reference/{api,config,web,main.go}` + build files

stdlib `net/http` (Go 1.22 method-pattern `ServeMux`). Clean, lift-worthy scaffold.

| item | file | verdict | Yuno destination |
|---|---|---|---|
| ServeMux router + method-pattern routes | api.go:30-70 | **LIFT** | REST command API |
| `writeJSON/readJSON/parseID/badRequest/serverError` | api.go:104-139 | **LIFT** | shared HTTP helpers |
| `Logging` middleware | api.go:92-98 | **LIFT** | swap for structured logger |
| `//go:embed all:dist` + SPA fallback + `/api/` guard | web/embed.go | **LIFT** | embed the **React** build (repoint dir; framework-agnostic) |
| Bootstrap + ctx-cancel + `srv.Shutdown(5s)` | main.go:21-107 | **LIFT** | graceful-shutdown template; add pool close + SSE hub stop |
| env helpers (`getenv/Int/Bool/Float/CSV`) | config.go:99-194 | **LIFT** | config package |
| `godotenv.Load` + typed Config | config.go:54-87 | **PORT** | add `DATABASE_URL`, secret signing key |
| HMAC signed-cookie session | auth.go:24-83 | **PORT** | React dashboard session; use a real signing key (not `SHA256(password)`), carry an identity claim |
| single-shared-password login | auth.go:85-125 | **SUPERSEDED** | multi-agent needs real identities |
| `withAuth` per-route decorator | api.go:76-89 | **PORT** | consider default-deny wrapper so new routes aren't accidentally public |
| `api/events.go` | (not staged) | **SKIP** | calendar CRUD, **not SSE** |
| 3-stage Dockerfile (node build → go build → alpine) | Dockerfile | **PORT** | add a `docker-compose.yml` + `postgres` service (none here — SQLite) |
| Makefile (build/cross/run) | Makefile | **LIFT** | adapt targets |
| systemd unit (`EnvironmentFile`, `StateDirectory`) | deploy/pi-dashboard.service | **PORT** | add `After=postgres` |

## 3. DB — `reference/db/` (pattern donor, SQLite → port the shape)

**SQLite via `database/sql`, not pgx.** Lift patterns, not code. Three pervasive porting gotchas:
1. **Placeholders** `?` → `$1,$2,…`
2. **Autoincrement + `LastInsertId()`** → Postgres `GENERATED AS IDENTITY`/`BIGSERIAL` + `INSERT … RETURNING id` via `QueryRow`
3. **Bool-as-INTEGER + `b2i`/`!=0`** → native `boolean` (delete the dance)
   (also: `datetime('now')`/TEXT timestamps → `timestamptz`+`now()`; `COLLATE NOCASE` → `ILIKE`/`citext`; `pragma_table_info` migrator is SQLite-only.)

| item | file | verdict | Yuno destination |
|---|---|---|---|
| Shared-scanner idiom (`const cols` + one `scan(sc interface{Scan})` for Row & Rows) | events.go:35-57 | **LIFT** | reuse for messages/runs/stack rows |
| Upsert `ON CONFLICT DO UPDATE SET x=EXCLUDED.x` | prefs.go:17 | **LIFT** | the `dict` upsert (identical on PG) |
| `ErrNoRows`→default idiom | prefs.go:23-30 | **LIFT** | `pgx.ErrNoRows` equivalent |
| CRUD scan/readback pattern | store.go:33-182 | **PORT** | `?`→`$n`, `LastInsertId`→`RETURNING` |
| owner-scoped queries (`WHERE owner_id=?`) | tracker.go | **PORT** | project/build/ticket scoping discipline |
| **recurrence engine** (`addRecurrence/startIndex/expandInWindow/NextOccurrenceAfter`) | events.go:204-367 | **PORT** | §16 — pure Go, tested, DB-agnostic; `NextOccurrenceAfter` is the scheduler primitive. Only 4 fixed intervals — swap for a cron lib if you need more |
| recurrence tests | events_recur_test.go | **LIFT** | port with the engine |
| count-guarded seed | seed.go:8-53 | **PORT** | workflow-template seeding |
| `Open` + WAL/single-writer, PRAGMA migrator | db.go | **SUPERSEDED** | `pgxpool.New`; use versioned migrations (tern/goose) |
| schema.sql DDL | schema.sql | **SUPERSEDED** | re-author for PG; keep "one idempotent file, indexed FKs" style |

**Atomic ack (net-new) — the tx pattern Yuno needs; salvaged CRUD methods should take a
`Querier` (`*pgxpool.Pool` or `pgx.Tx`) so they compose inside it:**
```go
tx, _ := pool.Begin(ctx); defer tx.Rollback(ctx)
tx.Exec(ctx, insertOutputSQL, ...)
tx.Exec(ctx, pushStackSQL, ...)
tx.Exec(ctx, upsertDictSQL, ...)
tx.Exec(ctx, ackInputSQL, msgID)   // UPDATE ... WHERE lease...
tx.Commit(ctx)
```

## 4. LLM / MCP — `reference/llm/` (mostly superseded; salvage the tool patterns)

No MCP protocol here (see TL;DR). Goose supersedes the agent loop. What ports is the
**tool-schema + dispatch** shape, useful when you write Yuno's MCP servers:

| item | file | verdict | note |
|---|---|---|---|
| schema-builder closures (`obj/str/intp/num`) | tools.go:19-128 | **PORT** | JSON Schema = MCP `inputSchema` shape. **But** rewrite `fn()` — it emits OpenAI's nested `{"type":"function","function":{…}}`; MCP tool descriptors are flat (`{name, description, inputSchema}`) |
| `dispatch(name,args,owner)` switch | tools.go:133-412 | **PORT** | template for an MCP `tools/call` handler: decode→validate→backend→JSON result |
| pointer partial-update (`*string`/`*int64` + applyOptional) | tools.go:414-446 | **LIFT** | patch-style `create_agent`/update fields |
| error-as-`{"error":…}` tool result | agent.go:141-150 | **PORT** | MCP tool errors as structured content, don't abort turn |
| time-stamped system prompt | agent.go:203-216 | **PORT (idea)** | only if Yuno builds prompts (Goose usually does) |
| `MCPClient.Ask`, `Agent.Run`, OpenAI wire types | mcp.go, agent.go | **SUPERSEDED** | Goose owns the loop + LLM |

## Gotchas worth preserving

① **UTF-16, not runes.** Telegram's 4096 limit is UTF-16 code units (astral emoji = 2). The
salvaged `splitMessage` counts runes with a 4000 slack cap — measure `utf16` before lifting.
② **Token redaction.** A failed `http.Do` returns `*url.Error` embedding the token-bearing
URL; keep `redact()`.
③ **Missed-fire catch-up.** `reminders.go` does an immediate first pass before the ticker so a
just-due item isn't skipped on startup — keep that in Yuno's ticker. The daily timer has *no*
catch-up (fires late after sleep) — decide if Yuno needs one.
④ **Vue→React.** Embed is framework-agnostic, but Dockerfile/Makefile `npm run build` + the
vite output path (`../backend/web/dist`) must repoint to the React build.
