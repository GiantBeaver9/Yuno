// Command mcp-create-agent is a thin stdio MCP server exposing a single
// create_agent tool over the one factory backend (internal/factory) — the
// same CreateAgent function the UI "new agent" form calls (PRD §10, ADR-18:
// one validated code path, never arbitrary shell). Goose (the MCP host)
// launches this binary as a subprocess and speaks MCP over stdin/stdout, so
// stdout is reserved for the protocol transport — all diagnostics go to
// stderr via the standard log package.
//
// All business logic lives in internal/factory and is tested there;
// specFromArgs is the tested, side-effect-free surface here that maps a
// mcp.CallToolRequest's arguments onto a factory.Spec.
package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/config"
	"github.com/giantbeaver9/yuno/internal/factory"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/store"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func main() {
	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if cfg.SecretKey == "" {
		log.Fatal("SECRET_KEY is required")
	}

	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	box, err := secretbox.NewFromHex(cfg.SecretKey)
	if err != nil {
		log.Fatalf("build secretbox: %v", err)
	}

	ag := agents.New(st.Pool, box)
	f := factory.New(ag, cfg.AgentsDir, box)

	s := server.NewMCPServer("yuno-create-agent", "1.0.0")
	registerTools(s, f)

	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("serve stdio: %v", err)
	}
}

// mcpCallToolRequest denotes mcp.CallToolRequest (see docs/tickets/mcp-create-agent.md).
type mcpCallToolRequest = mcp.CallToolRequest

// specFromArgs maps a create_agent tool call's arguments onto a factory.Spec.
// name, provider, model, key are required (RequireString: missing/wrong-type
// surfaces as an error, never a panic); tools, prompt, guid, and roles are
// optional (Get*/GetStringSlice with empty-string/empty-slice sentinels —
// guid="" means "make a new one", absent tools/roles come back as an empty
// slice, never nil).
func specFromArgs(req mcpCallToolRequest) (factory.Spec, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return factory.Spec{}, err
	}
	provider, err := req.RequireString("provider")
	if err != nil {
		return factory.Spec{}, err
	}
	model, err := req.RequireString("model")
	if err != nil {
		return factory.Spec{}, err
	}
	key, err := req.RequireString("key")
	if err != nil {
		return factory.Spec{}, err
	}

	return factory.Spec{
		Name:     name,
		Provider: provider,
		Model:    model,
		Key:      key,
		Prompt:   req.GetString("prompt", ""),
		Tools:    req.GetStringSlice("tools", []string{}),
		Guid:     req.GetString("guid", ""),
		Roles:    req.GetStringSlice("roles", []string{}),
	}, nil
}

func registerTools(s *server.MCPServer, f *factory.Factory) {
	tool := mcp.NewTool("create_agent",
		mcp.WithDescription("Create a new agent: validates the spec, writes its recipe file, and stores its row and encrypted provider key"),
		mcp.WithString("name", mcp.Required(), mcp.Description("Agent name (used to derive the recipe filename)")),
		mcp.WithString("provider", mcp.Required(), mcp.Description("Provider id; whitelisted (gemini | huggingface)")),
		mcp.WithString("model", mcp.Required(), mcp.Description("Model name for the chosen provider")),
		mcp.WithString("key", mcp.Required(), mcp.Description("Provider API key (plaintext in; stored encrypted)")),
		mcp.WithArray("tools", mcp.WithStringItems(), mcp.Description("Tool names to allow the agent; omit for none")),
		mcp.WithString("prompt", mcp.Description("System prompt for the agent; omit for none")),
		mcp.WithString("guid", mcp.Description("Run guid to scope the agent to; omit to make a new one")),
		mcp.WithArray("roles", mcp.WithStringItems(), mcp.Description("Roles to assign the agent; omit for none")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		spec, err := specFromArgs(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		created, err := f.CreateAgent(ctx, spec)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		data, err := json.Marshal(created)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(data)), nil
	})
}
