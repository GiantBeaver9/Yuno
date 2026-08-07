// Package agents is CRUD over the `agent`, `agent_roles`, and `provider_key`
// tables: agent config knobs, roles-via-join, and per-agent provider keys
// encrypted at rest with secretbox. See docs/agents.md.
package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/jackc/pgx/v5"
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

type Store struct {
	q   store.Querier
	box *secretbox.Box
}

func New(q store.Querier, box *secretbox.Box) *Store {
	return &Store{q: q, box: box}
}

// selectAgentJoinRoles is shared by Get and List: it aggregates agent_roles
// into a sorted array per agent via a LEFT JOIN, so an agent with no roles
// still comes back with roles = '{}' rather than being dropped.
const selectAgentJoinRoles = `
SELECT a.id, a.name, a.provider, a.model, a.recipe_path, a.prompt, a.tools,
       a.mode, a.max_cost, a.rate_limit, a.blocked_tools, a.guid,
       COALESCE(array_agg(r.role ORDER BY r.role) FILTER (WHERE r.role IS NOT NULL), '{}')
FROM agent a
LEFT JOIN agent_roles r ON r.agent_id = a.id
`

// scanAgent reads one joined agent row produced by selectAgentJoinRoles.
func scanAgent(sc interface{ Scan(...any) error }) (Agent, error) {
	var a Agent
	err := sc.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.RecipePath, &a.Prompt,
		&a.Tools, &a.Mode, &a.MaxCost, &a.RateLimit, &a.BlockedTools, &a.Guid, &a.Roles)
	return a, err
}

// dedupRoles preserves nothing about input order; it returns the sorted set
// of distinct, non-empty roles.
func dedupRoles(roles []string) []string {
	seen := make(map[string]bool, len(roles))
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

// Create inserts the agent row (RETURNING id) and its agent_roles rows.
func (s *Store) Create(ctx context.Context, p CreateParams) (Agent, error) {
	mode := p.Mode
	if mode == "" {
		mode = "auto"
	}
	tools := p.Tools
	if tools == nil {
		tools = []string{}
	}
	blockedTools := p.BlockedTools
	if blockedTools == nil {
		blockedTools = []string{}
	}

	var id int64
	err := s.q.QueryRow(ctx,
		`INSERT INTO agent (name, provider, model, recipe_path, prompt, tools, mode, max_cost, rate_limit, blocked_tools, guid)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 RETURNING id`,
		p.Name, p.Provider, p.Model, p.RecipePath, p.Prompt, tools, mode, p.MaxCost, p.RateLimit, blockedTools, p.Guid,
	).Scan(&id)
	if err != nil {
		return Agent{}, fmt.Errorf("agents: create agent %q: %w", p.Name, err)
	}

	roles := dedupRoles(p.Roles)
	if err := s.insertRoles(ctx, id, roles); err != nil {
		return Agent{}, fmt.Errorf("agents: create agent %q: %w", p.Name, err)
	}

	return s.Get(ctx, id)
}

// insertRoles inserts one agent_roles row per (already deduped) role.
func (s *Store) insertRoles(ctx context.Context, agentID int64, roles []string) error {
	if len(roles) == 0 {
		return nil
	}
	_, err := s.q.Exec(ctx,
		`INSERT INTO agent_roles (agent_id, role) SELECT $1, x FROM unnest($2::text[]) AS x`,
		agentID, roles,
	)
	return err
}

// Get returns the agent with roles populated. Missing id → error (pgx.ErrNoRows).
func (s *Store) Get(ctx context.Context, id int64) (Agent, error) {
	row := s.q.QueryRow(ctx, selectAgentJoinRoles+" WHERE a.id = $1 GROUP BY a.id", id)
	a, err := scanAgent(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Agent{}, fmt.Errorf("agents: get agent %d: %w", id, pgx.ErrNoRows)
		}
		return Agent{}, fmt.Errorf("agents: get agent %d: %w", id, err)
	}
	return a, nil
}

// List returns all agents with roles — the `GET /agents` data shape (agents +
// roles + guid). No provider keys.
func (s *Store) List(ctx context.Context) ([]Agent, error) {
	rows, err := s.q.Query(ctx, selectAgentJoinRoles+" GROUP BY a.id ORDER BY a.id")
	if err != nil {
		return nil, fmt.Errorf("agents: list agents: %w", err)
	}
	defer rows.Close()

	out := []Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("agents: scan agent: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agents: list agents: %w", err)
	}
	return out, nil
}

// SetRoles replaces the agent's roles (delete-then-insert; dedup).
func (s *Store) SetRoles(ctx context.Context, agentID int64, roles []string) error {
	if _, err := s.q.Exec(ctx, `DELETE FROM agent_roles WHERE agent_id = $1`, agentID); err != nil {
		return fmt.Errorf("agents: set roles for agent %d: %w", agentID, err)
	}
	if err := s.insertRoles(ctx, agentID, dedupRoles(roles)); err != nil {
		return fmt.Errorf("agents: set roles for agent %d: %w", agentID, err)
	}
	return nil
}

func (s *Store) GetRoles(ctx context.Context, agentID int64) ([]string, error) {
	rows, err := s.q.Query(ctx, `SELECT role FROM agent_roles WHERE agent_id = $1 ORDER BY role`, agentID)
	if err != nil {
		return nil, fmt.Errorf("agents: get roles for agent %d: %w", agentID, err)
	}
	defer rows.Close()

	roles := []string{}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, fmt.Errorf("agents: scan role: %w", err)
		}
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agents: get roles for agent %d: %w", agentID, err)
	}
	return roles, nil
}

// SetProviderKey encrypts key via secretbox and upserts provider_key
// (PK agent_id, provider), storing enc_key BYTEA.
func (s *Store) SetProviderKey(ctx context.Context, agentID int64, provider, key string) error {
	encKey, err := s.box.Encrypt([]byte(key))
	if err != nil {
		return fmt.Errorf("agents: encrypt provider key for agent %d/%s: %w", agentID, provider, err)
	}
	_, err = s.q.Exec(ctx,
		`INSERT INTO provider_key (agent_id, provider, enc_key) VALUES ($1, $2, $3)
		 ON CONFLICT (agent_id, provider) DO UPDATE SET enc_key = excluded.enc_key`,
		agentID, provider, encKey,
	)
	if err != nil {
		return fmt.Errorf("agents: set provider key for agent %d/%s: %w", agentID, provider, err)
	}
	return nil
}

// GetProviderKey loads enc_key and decrypts it. No row → error.
func (s *Store) GetProviderKey(ctx context.Context, agentID int64, provider string) (string, error) {
	var encKey []byte
	err := s.q.QueryRow(ctx,
		`SELECT enc_key FROM provider_key WHERE agent_id = $1 AND provider = $2`,
		agentID, provider,
	).Scan(&encKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", fmt.Errorf("agents: get provider key for agent %d/%s: %w", agentID, provider, pgx.ErrNoRows)
		}
		return "", fmt.Errorf("agents: get provider key for agent %d/%s: %w", agentID, provider, err)
	}

	plaintext, err := s.box.Decrypt(encKey)
	if err != nil {
		return "", fmt.Errorf("agents: decrypt provider key for agent %d/%s: %w", agentID, provider, err)
	}
	return string(plaintext), nil
}
