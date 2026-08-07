# MCP server skeleton — for Yuno's `create_agent` + `memory`

Source: `github.com/GiantBeaver9/GoSearchAPI` @ `e19ab29`. A real MCP server built on
**`github.com/mark3labs/mcp-go v0.54.1`**, served over **stdio** — exactly the transport
Goose launches. The repo's search/scrape tools are throwaway; this doc is the *scaffold*,
distilled. Real reference in `reference/main.go` (`runMCP()` is the model).

**This closes the biggest net-new gap.** The Pi repo had no real MCP (it called LM Studio).
This repo shows you've already authored a genuine stdio MCP server — so Yuno's two custom
servers are fill-in-the-blanks, not new territory.

## How it maps to Yuno

- **Goose is the host** (the role LM Studio played for you). Goose spawns each MCP server as
  a subprocess and speaks MCP over stdin/stdout.
- Yuno needs two of these servers: **`create_agent`** (the Factory tool, §10) and
  **`memory`** (guid-scoped get/set, §8). Each is a small standalone Go binary.
- `google/jsonschema-go` is an **indirect, unused** dep — the `mcp.With*` builders cover
  everything. You don't need it unless you want raw JSON-Schema for nested objects.

## Minimal server skeleton (real v0.54.1 API)

```go
package main

import (
	"context"
	"encoding/json"
	"log" // stderr ONLY — see gotcha ①

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	db := openDB()          // open state in main…
	defer db.Close()

	s := server.NewMCPServer("yuno-memory", "1.0.0") // (name, version) — positional
	registerTools(s, db)                              // …pass via closure (no DI)

	if err := server.ServeStdio(s); err != nil {      // blocks until Goose closes stdin
		log.Fatal(err)
	}
}

func registerTools(s *server.MCPServer, db *DB) {
	tool := mcp.NewTool("memory_set",
		mcp.WithDescription("Store a memory under the current run's guid"),
		mcp.WithString("key",   mcp.Required(), mcp.Description("Memory key")),
		mcp.WithString("value", mcp.Required(), mcp.Description("Value to store")),
		mcp.WithString("guid",  mcp.Description("Run guid; omit to use current")), // optional
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key, err := req.RequireString("key")
		if err != nil { return mcp.NewToolResultError(err.Error()), nil } // business err → result, Go err = nil
		value, err := req.RequireString("value")
		if err != nil { return mcp.NewToolResultError(err.Error()), nil }
		guid := req.GetString("guid", "")   // "" sentinel = "not provided"

		out, err := db.Set(guid, key, value)
		if err != nil { return mcp.NewToolResultError(err.Error()), nil }
		data, _ := json.Marshal(out)
		return mcp.NewToolResultText(string(data)), nil
	})
}
```

That's the entire shape. `create_agent` is the same skeleton with a wider schema
(`name, provider, model, key, tools[], prompt, guid?`) whose handler calls Yuno's one
`create_agent` backend endpoint (§10 — one endpoint, two callers).

## Schema builders (variadic opts to `mcp.NewTool(name, ...)`)

| Builder | Purpose |
|---|---|
| `mcp.WithDescription(s)` | tool description the model sees |
| `mcp.WithString("k", opts...)` | string param |
| `mcp.WithNumber("k", opts...)` | number (float64 on wire) |
| `mcp.WithBoolean("k", opts...)` | bool |
| `mcp.WithArray("k", ..., mcp.WithStringItems())` | array of strings (Yuno's `tools`) |
| `mcp.WithArray("k", ..., mcp.Items(map[string]any{...}))` | array of objects (raw JSON-Schema fragment) |
| per-param: `mcp.Required()`, `mcp.Description(s)` | mark required / describe |

## Arg extraction (methods on `mcp.CallToolRequest`)

| Call | Behavior |
|---|---|
| `req.RequireString("k") (string, error)` | required; errors if missing/wrong type |
| `req.RequireStringSlice("k") ([]string, error)` | required string array |
| `req.GetString("k", "def") string` | optional + default, no error |
| `req.GetInt("k", 5) int` | optional int (coerces the wire float64) |
| `req.GetBool("k", false) bool` | optional bool |
| `req.GetArguments() map[string]any` | raw args → `json.Marshal`→`Unmarshal` into a struct for nested objects |

## Results

| Call | Use |
|---|---|
| `mcp.NewToolResultText(s)` | success (repo returns JSON-as-text) |
| `mcp.NewToolResultError(msg)` | business error the model should see — **return `nil` as the Go error** |

## Launch (how Goose references it)

Build `go build -o yuno-memory .`, then Goose's MCP config points at it — same shape as the
LM Studio `mcp.json` in `reference/SETUP.md`:
```
command: "/path/yuno-memory"
args:    ["mcp"]          # or make stdio the default mode and drop the arg
env:     { ... }          # per-server config injected here
```
Simplest for Yuno: make each binary MCP-over-stdio by default (no subcommand), since Goose
only ever wants stdio MCP.

## Gotchas (all confirmed in the real code)

① **stdout is the transport.** Never `fmt.Println` — it corrupts MCP frames. All diagnostics
go to **stderr** (`log` defaults there). Non-negotiable.
② **Business errors → `NewToolResultError(...), nil`.** Reserve a non-nil Go error for
protocol/transport failure only. Consistent across all ~15 tools in the source.
③ **Optional/nullable = empty-default sentinel.** No explicit nullable type; declare without
`mcp.Required()`, read with `Get*(key, default)`, treat the default as "absent". Perfect for
Yuno's `guid?` → `req.GetString("guid", "")`, `""` means "make a new one".
④ **Schema default ≠ runtime default.** The default lives only in the handler's
`GetInt("k", 5)`; the schema description is prose. Keep them in sync by hand.
⑤ **`NewMCPServer` here passes no capability options** — `AddTool` implies tool capability.
Fine to omit; add `server.WithToolCapabilities(...)`/hooks only if needed.
⑥ **No graceful shutdown** — `ServeStdio` blocks until Goose kills the subprocess; a
`defer db.Close()` is enough cleanup.
⑦ Cleanest single-tool reference to mirror: `wordDefinitionTool` in `reference/main.go`
(declaration ~L211-220, handler ~L232-245) — one required string + one optional string.
