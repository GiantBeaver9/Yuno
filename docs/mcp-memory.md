# mcp-memory — guid-scoped memory over stdio

The `internal/memory` package implements Yuno's memory API: whatever lives
under a guid (§8). A "memory" is not a single object — it's the pairing of a
`dict` row (arbitrary key/value detail, stored as JSONB) and any `stack` rows
tagged with the same guid (ordered breadcrumbs from a run). `cmd/mcp-memory`
exposes that API as a stdio MCP server so Goose (or any MCP host) can read and
write it as tool calls.

## The guid handle

Every memory operation is scoped by `guid`, a free-form string that is also
`run.guid` when the memory belongs to a run. `dict.guid` is that table's
primary key — one row per guid, holding a `full_detail` JSONB blob. `stack`
rows reference the same guid via `stack.run_id` (a FK to `run.guid`), so
searching a guid pulls both the dict detail and that run's breadcrumb trail.
Keys under one guid are invisible under another; there is no cross-guid
lookup.

## API (`internal/memory.Store`)

```go
s := memory.New(pool) // pool is any store.Querier: *pgxpool.Pool or pgx.Tx

err := s.Set(ctx, guid, "lang", "go")
value, ok, err := s.Get(ctx, guid, "lang")       // ("go", true, nil)
hits, err := s.Search(ctx, guid, "go")            // []Hit
```

- **`Set(ctx, guid, key, value)`** upserts `key -> value` inside
  `dict.full_detail` via a JSONB merge (`||`), so setting one key never
  clobbers sibling keys already stored under the same guid. An empty guid is
  a caller error (`ErrEmptyGuid`) — there's no such thing as a memory with no
  handle.
- **`Get(ctx, guid, key)`** reads `full_detail ->> key`. A key that was never
  set, or a guid with no dict row at all, both come back as `("", false,
  nil)` — absence is a normal outcome, not an error.
- **`Search(ctx, guid, query)`** returns every dict key/value pair under
  `guid` whose key or value contains `query` as a substring, plus every
  `stack` row under `stack.run_id = guid` whose summary contains it. Stack
  hits use `Key == "stack"`; dict hits use the dict key. No matches is an
  empty slice, not an error.

## The stdio server (`cmd/mcp-memory`)

`cmd/mcp-memory/main.go` is a thin wrapper: it opens a `store.Store` from
`DATABASE_URL`, builds a `memory.Store` over the pool, and registers three
`mark3labs/mcp-go` tools that each call straight into it:

| Tool | Args | Behavior |
|---|---|---|
| `memory_get` | `key` (required), `guid` (optional) | Returns the stored value, or `""` if absent |
| `memory_set` | `key`, `value` (required), `guid` (optional) | Upserts the key and returns `"ok"` |
| `memory_search` | `query` (required), `guid` (optional) | Returns matching `key: value` lines |

Business errors (e.g. an empty guid) come back as `NewToolResultError(...)`
with a `nil` Go error, per the MCP convention — a non-nil Go error is
reserved for protocol/transport failure, which would otherwise corrupt the
stdio stream.

### How Goose launches it

Goose is the MCP host: it spawns `mcp-memory` as a subprocess and speaks MCP
over the subprocess's stdin/stdout. That means **stdout is the transport** —
the binary never writes anything but MCP frames there. All diagnostics go to
stderr via the standard `log` package. Goose's config points at the built
binary directly, with `DATABASE_URL` injected as an environment variable:

```
command: "/path/to/mcp-memory"
env:     { DATABASE_URL: "postgres://..." }
```

No subcommand or `args` is needed — the binary speaks stdio MCP by default,
since that's the only transport Goose ever wants.
