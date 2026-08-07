# mcp-create-agent — thin stdio MCP server → factory.CreateAgent

## Unit
mcp-create-agent

## Package / Owned files
`cmd/mcp-create-agent/main.go`

## Deps
factory

## Tier
simple

## Interfaces
```go
package main // cmd/mcp-create-agent

// main wires: config → store.Open → secretbox.NewFromHex(SECRET_KEY) →
// agents.New → factory.New(agentsDir), registers the create_agent tool on a
// mark3labs/mcp-go server, and server.ServeStdio(s). Diagnostics to stderr only.
//
// Tool schema (MCP-SKELETON.md builders):
//   create_agent(name, provider, model, key, tools[]?, prompt?, guid?, roles[]?)
//   name, provider, model, key = mcp.Required(); the rest optional.
//
// specFromArgs is the tested, side-effect-free surface: it maps a
// mcp.CallToolRequest's arguments onto a factory.Spec, applying Get*/Require*
// with the SKELETON's empty-default sentinels (guid="" means "make a new one").
func specFromArgs(req mcpCallToolRequest) (factory.Spec, error)
```
(`mcpCallToolRequest` denotes `mcp.CallToolRequest`; extract `specFromArgs` so it
can be unit-tested without the stdio loop.)

## Accept
- The `cmd/mcp-create-agent` binary builds and serves `create_agent` over stdio, writing nothing to stdout except MCP frames.
- The handler decodes args → `factory.Spec` → `factory.CreateAgent` → returns the created agent as JSON text (`mcp.NewToolResultText`); a validation/creation failure returns `mcp.NewToolResultError(...)` with a `nil` Go error (business error → result, per SKELETON gotcha ②).
- `specFromArgs` maps required strings via `RequireString` and optionals via `Get*` with sentinels.

## Test cases
- **[positive]** `specFromArgs` on args `{name:"coder",provider:"gemini",model:"gemini-1.5",key:"sk-x"}` → `factory.Spec` with those fields and empty optionals.
- **[negative]** `specFromArgs` on args missing the required `name` → error (surfaces as `NewToolResultError`, not a Go error / panic).
- **[edge]** Optional `guid` absent → `Spec.Guid == ""` (the "make a new one" sentinel); absent `tools` → `Spec.Tools` is empty, not nil-panicking.

## Salvage
LIFT the stdio skeleton from `salvage/mcp-skeleton/MCP-SKELETON.md` — this is "the same skeleton with a wider schema (name, provider, model, key, tools[], prompt, guid?)" whose handler calls the one `create_agent` backend (factory). Smoke-level; the real logic lives in factory.
