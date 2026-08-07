# agents — CRUD over agent / agent_roles / provider_key (encrypted)

## Unit
agents

## Package / Owned files
`internal/agents/*.go`

## Deps
secretbox

## Tier
standard

## Interfaces
```go
package agents

import (
	"context"

	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/store"
)

// Agent mirrors the `agent` row plus its joined roles. The provider key is
// NEVER a field here — it is fetched separately and decrypted on demand.
type Agent struct {
	ID           int64
	Name         string
	Provider     string
	Model        string
	RecipePath   string   // agent.recipe_path
	Prompt       string
	Tools        []string // agent.tools  TEXT[]
	Mode         string   // auto | approval
	MaxCost      float64  // agent.max_cost
	RateLimit    int      // agent.rate_limit
	BlockedTools []string // agent.blocked_tools TEXT[]
	Guid         string
	Roles        []string // from agent_roles
}

type CreateParams struct {
	Name, Provider, Model, RecipePath, Prompt string
	Tools        []string
	Mode         string // defaults "auto" if empty
	MaxCost      float64
	RateLimit    int
	BlockedTools []string
	Guid         string
	Roles        []string
}

type Store struct{ /* q store.Querier; box *secretbox.Box */ }

func New(q store.Querier, box *secretbox.Box) *Store

// Create inserts the agent row (RETURNING id) and its agent_roles rows.
func (s *Store) Create(ctx context.Context, p CreateParams) (Agent, error)

// Get returns the agent with roles populated. Missing id → error (pgx.ErrNoRows).
func (s *Store) Get(ctx context.Context, id int64) (Agent, error)

// List returns all agents with roles — the `GET /agents` data shape (agents +
// roles + guid). No provider keys.
func (s *Store) List(ctx context.Context) ([]Agent, error)

// SetRoles replaces the agent's roles (delete-then-insert; dedup).
func (s *Store) SetRoles(ctx context.Context, agentID int64, roles []string) error
func (s *Store) GetRoles(ctx context.Context, agentID int64) ([]string, error)

// SetProviderKey encrypts key via secretbox and upserts provider_key
// (PK agent_id, provider), storing enc_key BYTEA.
func (s *Store) SetProviderKey(ctx context.Context, agentID int64, provider, key string) error

// GetProviderKey loads enc_key and decrypts it. No row → error.
func (s *Store) GetProviderKey(ctx context.Context, agentID int64, provider string) (string, error)
```

## Accept
- `Create` persists the `agent` row (all config knobs from schema.sql: provider, model, recipe_path, prompt, tools[], mode, max_cost, rate_limit, blocked_tools[], guid) and one `agent_roles` row per role; returns the row with `ID` set.
- `List`/`Get` populate `Roles` via join and never expose the provider key.
- `SetProviderKey` writes `enc_key` that is NOT byte-equal to the plaintext (encrypted at rest); `GetProviderKey` round-trips to the original.
- `SetRoles` is a replace (idempotent): calling it twice with the same set leaves one row per role.

## Test cases
All DB tests use `testutil.NewDB`.
- **[positive]** `Create` an agent with `Roles:["coder"]` and config knobs set → `Get` returns them intact with `Roles==["coder"]`; `SetProviderKey(id,"gemini","sk-x")` then `GetProviderKey(id,"gemini")` == `"sk-x"`, and a raw `SELECT enc_key` differs from `"sk-x"` bytes (proves encryption).
- **[negative]** `GetProviderKey` for an agent that has no key row → error; `Get` on a nonexistent id → error.
- **[edge]** `Create` with empty `Roles` → no `agent_roles` rows, yet `List` still returns the agent; `SetRoles` called twice with `["coder","coder"]` → exactly one `coder` role row (dedup / replace, no PK violation).

## Salvage
secretbox for enc/dec. LIFT the shared-scanner idiom (events.go:35-57) and the upsert `ON CONFLICT DO UPDATE` (prefs.go:17) for the provider_key write; PORT the CRUD scan/readback (store.go:33-182) with `?`→`$n`, `LastInsertId`→`RETURNING`. Postgres `TEXT[]` maps directly to `[]string` via pgx.
