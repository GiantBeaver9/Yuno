// Command mcp-memory is a thin stdio MCP server exposing the guid-scoped
// memory API (internal/memory) as three tools: memory_get, memory_set, and
// memory_search. Goose (the MCP host) launches this binary as a subprocess
// and speaks MCP over stdin/stdout, so stdout is reserved for the protocol
// transport — all diagnostics go to stderr via the standard log package.
//
// All business logic lives in internal/memory and is tested there; this file
// is a smoke-level wrapper with no tests of its own.
package main

import (
	"context"
	"log"
	"os"

	"github.com/giantbeaver9/yuno/internal/memory"
	"github.com/giantbeaver9/yuno/internal/store"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, databaseURL)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	m := memory.New(st.Pool)

	s := server.NewMCPServer("yuno-memory", "1.0.0")
	registerTools(s, m)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("serve stdio: %v", err)
	}
}

func registerTools(s *server.MCPServer, m *memory.Store) {
	getTool := mcp.NewTool("memory_get",
		mcp.WithDescription("Read a value previously stored under a memory key"),
		mcp.WithString("key", mcp.Required(), mcp.Description("Memory key")),
		mcp.WithString("guid", mcp.Description("Run guid; omit to use current")),
	)
	s.AddTool(getTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key, err := req.RequireString("key")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		guid := req.GetString("guid", "")

		value, ok, err := m.Get(ctx, guid, key)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if !ok {
			return mcp.NewToolResultText(""), nil
		}
		return mcp.NewToolResultText(value), nil
	})

	setTool := mcp.NewTool("memory_set",
		mcp.WithDescription("Store a memory under the current run's guid"),
		mcp.WithString("key", mcp.Required(), mcp.Description("Memory key")),
		mcp.WithString("value", mcp.Required(), mcp.Description("Value to store")),
		mcp.WithString("guid", mcp.Description("Run guid; omit to use current")),
	)
	s.AddTool(setTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		key, err := req.RequireString("key")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		value, err := req.RequireString("value")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		guid := req.GetString("guid", "")

		if err := m.Set(ctx, guid, key, value); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	})

	searchTool := mcp.NewTool("memory_search",
		mcp.WithDescription("Search memory keys/values and stack breadcrumbs for a substring"),
		mcp.WithString("query", mcp.Required(), mcp.Description("Substring to search for")),
		mcp.WithString("guid", mcp.Description("Run guid; omit to use current")),
	)
	s.AddTool(searchTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		guid := req.GetString("guid", "")

		hits, err := m.Search(ctx, guid, query)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var out string
		for _, h := range hits {
			out += h.Key + ": " + h.Value + "\n"
		}
		return mcp.NewToolResultText(out), nil
	})
}
