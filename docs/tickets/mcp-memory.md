# mcp-memory — guid-scoped memory get/set/search + stdio MCP server

## Unit
mcp-memory

## Package / Owned files
`cmd/mcp-memory/main.go`, `internal/memory/*.go`

## Deps
store

## Tier
standard

## Interfaces
```go
package memory // internal/memory

import (
	"context"

	"github.com/giantbeaver9/yuno/internal/store"
)

// Memory is the guid handle over dict + stack (§8: memory = whatever lives under
// a guid). Keys/values live inside dict.full_detail (JSONB); stack summaries are
// searchable breadcrumbs under the same guid.
type Store struct{ /* q store.Querier */ }

func New(q store.Querier) *Store

// Set upserts key→value inside dict.full_detail for guid:
//   INSERT INTO dict(guid, full_detail) VALUES ($1, jsonb_build_object($2,$3))
//   ON CONFLICT (guid) DO UPDATE SET full_detail = dict.full_detail || jsonb_build_object($2,$3)
// Empty guid → error.
func (s *Store) Set(ctx context.Context, guid, key, value string) error

// Get reads dict.full_detail->>key. Missing key → ("", false, nil) — absence is
// not an error.
func (s *Store) Get(ctx context.Context, guid, key string) (value string, ok bool, err error)

// Search returns dict key/value pairs under guid whose key or value contains the
// query substring, plus matching stack summaries (stack.run_id = guid).
func (s *Store) Search(ctx context.Context, guid, query string) ([]Hit, error)

type Hit struct {
	Key   string // dict key, or "stack" for a breadcrumb
	Value string
}
```

`cmd/mcp-memory/main.go` — a `mark3labs/mcp-go` stdio server (see MCP-SKELETON.md):
opens the store from `DATABASE_URL`, registers tools `memory_get(key, guid?)`,
`memory_set(key, value, guid?)`, `memory_search(query, guid?)`, each handler
calling the `internal/memory` Store, then `server.ServeStdio(s)`. Diagnostics to
stderr only (stdout is the MCP transport). The stdio loop itself is smoke-level;
the tested surface is `internal/memory`.

## Accept
- `Set` then `Get` on the same `(guid,key)` returns the stored value; a second `Set` on the same key overwrites it while preserving other keys in `full_detail` (JSONB merge, not replace).
- `Get` of a key never set → `("", false, nil)`.
- `Search` matches on key or value substring under the guid and includes stack summaries for `stack.run_id = guid`.
- The `cmd/mcp-memory` binary builds and serves over stdio, writing no diagnostics to stdout.

## Test cases
All DB tests use `testutil.NewDB`. `dict` has no FK, so dict-only tests need no run row; the stack branch of `Search` requires a `run` + `stack` row.
- **[positive]** `Set(g,"lang","go")` then `Get(g,"lang")` → `("go", true, nil)`; `Search(g,"go")` returns a hit with `Value=="go"`.
- **[negative]** `Get(g,"absent")` → `("", false, nil)` (no error); `Set("","k","v")` (empty guid) → error.
- **[edge]** `Set(g,"k","v1")` then `Set(g,"k","v2")` → `Get` returns `"v2"` and a previously-set different key under the same guid is untouched (JSONB merge); `Search(g,"nomatch")` → empty slice.

## Salvage
LIFT the stdio server skeleton from `salvage/mcp-skeleton/MCP-SKELETON.md` (the `memory_set` example is written against this exact unit — `server.NewMCPServer`, `mcp.NewTool`, `req.RequireString`/`GetString`, `NewToolResultText/Error`, `server.ServeStdio`). Gotchas ①–③ apply (stderr-only, business-error→`nil` Go error, `GetString("guid","")` sentinel).
