# factory — CreateAgent: the one backend fn (UI form + Factory MCP tool)

## Unit
factory

## Package / Owned files
`internal/factory/*.go`

## Deps
agents, secretbox

## Tier
standard

## Interfaces
```go
package factory

import (
	"context"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/secretbox"
)

// Spec is the validated input both callers supply (§10 — one endpoint, two
// callers: the UI "new agent" form and the create_agent MCP tool).
type Spec struct {
	Name         string
	Provider     string // whitelisted: gemini | huggingface
	Model        string
	Key          string // provider API key (plaintext in; stored encrypted)
	Prompt       string
	Tools        []string
	Guid         string // optional (§8 — omit = blank slate)
	Roles        []string
	Mode         string
	MaxCost      float64
	RateLimit    int
	BlockedTools []string
}

// AllowedProviders is the provider whitelist.
var AllowedProviders = []string{"gemini", "huggingface"}

// ValidateSpec enforces: non-empty Name, Provider ∈ AllowedProviders, and that
// the derived recipe path stays under agentsDir (no path traversal). Returns a
// descriptive error on any violation.
func ValidateSpec(s Spec, agentsDir string) error

// RecipePath returns the sanitized recipe file path for name under agentsDir,
// erroring if the cleaned absolute path escapes agentsDir (traversal guard).
func RecipePath(agentsDir, name string) (string, error)

type Factory struct {
	/* agents *agents.Store; agentsDir string; box *secretbox.Box */
}

func New(ag *agents.Store, agentsDir string, box *secretbox.Box) *Factory

// CreateAgent is the single backend function. It: ValidateSpec → write the
// recipe file under agentsDir → agents.Create(row) → agents.SetProviderKey
// (encrypted). Any validation failure short-circuits BEFORE any write.
func (f *Factory) CreateAgent(ctx context.Context, s Spec) (agents.Agent, error)
```

## Accept
- `CreateAgent` on a valid spec writes a recipe file under `agentsDir`, inserts the `agent` row (with `recipe_path` set to that file), and stores the provider key encrypted — all wired through the agents unit.
- Validation runs first: a bad provider or a traversal-y name is rejected with NO file written and NO DB row created.
- `RecipePath` never returns a path outside `agentsDir`; names are sanitized.
- The same function serves both the UI form and the Factory MCP tool (no second code path).

## Test cases
DB tests use `testutil.NewDB`; use a `t.TempDir()` as `agentsDir`.
- **[positive]** `CreateAgent{Name:"coder", Provider:"gemini", Key:"sk-x"}` → a recipe file exists under `agentsDir`, `agents.Get(returned.ID)` shows `recipe_path` pointing at it, and `agents.GetProviderKey` round-trips `"sk-x"`.
- **[negative]** `Spec{Name:"../../etc/passwd", Provider:"gemini"}` → rejected (traversal), no file created and no agent row inserted; `Spec{Name:"x", Provider:"openai"}` (not whitelisted) → rejected.
- **[edge]** `Spec` with empty `Tools` → valid; recipe written enabling no custom extensions. Creating a second agent whose sanitized name collides with an existing recipe file → rejected (no silent overwrite).

## Salvage
agents + secretbox do the persistence/encryption. PORT.md §4: LIFT the pointer partial-update idea (tools.go:414-446) and PORT the `dispatch` decode→validate→backend shape (tools.go:133-412) as the validation/whitelist template. Recipe-file writing is net-new.
