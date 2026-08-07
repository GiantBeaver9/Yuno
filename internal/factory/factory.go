// Package factory is the one backend function behind agent creation (PRD
// §10, ADR-18): CreateAgent is called by both the UI "new agent" form and the
// Factory MCP tool, so there is exactly one validated code path — never
// arbitrary shell — for turning a Spec into a running agent. See docs/factory.md.
package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
func ValidateSpec(s Spec, agentsDir string) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("factory: name is required")
	}

	allowed := false
	for _, p := range AllowedProviders {
		if s.Provider == p {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("factory: provider %q is not whitelisted (allowed: %v)", s.Provider, AllowedProviders)
	}

	if _, err := RecipePath(agentsDir, s.Name); err != nil {
		return err
	}

	return nil
}

// recipeExt is the file extension for a written agent recipe.
const recipeExt = ".yaml"

// RecipePath returns the sanitized recipe file path for name under agentsDir,
// erroring if the cleaned absolute path escapes agentsDir (traversal guard).
func RecipePath(agentsDir, name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("factory: name is required")
	}
	// No path separators allowed in a name-derived filename at all — this
	// alone blocks "../evil", "../../etc/passwd", "a/b", and absolute paths
	// (which necessarily contain a separator on this platform).
	if strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("factory: invalid name %q: must not contain path separators", name)
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("factory: invalid name %q", name)
	}
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("factory: invalid name %q: must not be an absolute path", name)
	}

	filename := name + recipeExt
	full := filepath.Join(agentsDir, filename)

	// Defense in depth: confirm the resolved path still lives under
	// agentsDir even after filepath.Join's cleaning.
	absDir, err := filepath.Abs(agentsDir)
	if err != nil {
		return "", fmt.Errorf("factory: resolve agents dir: %w", err)
	}
	absFull, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("factory: resolve recipe path: %w", err)
	}
	rel, err := filepath.Rel(absDir, absFull)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("factory: name %q escapes agents dir", name)
	}

	return full, nil
}

type Factory struct {
	agents    *agents.Store
	agentsDir string
	box       *secretbox.Box
}

func New(ag *agents.Store, agentsDir string, box *secretbox.Box) *Factory {
	return &Factory{agents: ag, agentsDir: agentsDir, box: box}
}

// CreateAgent is the single backend function. It: ValidateSpec → write the
// recipe file under agentsDir → agents.Create(row) → agents.SetProviderKey
// (encrypted). Any validation failure short-circuits BEFORE any write.
func (f *Factory) CreateAgent(ctx context.Context, s Spec) (agents.Agent, error) {
	// Validate first — nothing below this point runs on a bad spec.
	if err := ValidateSpec(s, f.agentsDir); err != nil {
		return agents.Agent{}, err
	}

	recipePath, err := RecipePath(f.agentsDir, s.Name)
	if err != nil {
		return agents.Agent{}, err
	}

	// Refuse to silently overwrite an existing recipe (name collision).
	if _, statErr := os.Stat(recipePath); statErr == nil {
		return agents.Agent{}, fmt.Errorf("factory: recipe already exists for name %q", s.Name)
	} else if !os.IsNotExist(statErr) {
		return agents.Agent{}, fmt.Errorf("factory: stat recipe path: %w", statErr)
	}

	if err := os.WriteFile(recipePath, buildRecipe(s), 0o644); err != nil {
		return agents.Agent{}, fmt.Errorf("factory: write recipe file: %w", err)
	}

	created, err := f.agents.Create(ctx, agents.CreateParams{
		Name:         s.Name,
		Provider:     s.Provider,
		Model:        s.Model,
		RecipePath:   recipePath,
		Prompt:       s.Prompt,
		Tools:        s.Tools,
		Mode:         s.Mode,
		MaxCost:      s.MaxCost,
		RateLimit:    s.RateLimit,
		BlockedTools: s.BlockedTools,
		Guid:         s.Guid,
		Roles:        s.Roles,
	})
	if err != nil {
		// Best-effort cleanup so a failed DB write doesn't leave an orphan
		// recipe file blocking a retry under the same name.
		_ = os.Remove(recipePath)
		return agents.Agent{}, fmt.Errorf("factory: create agent row: %w", err)
	}

	if err := f.agents.SetProviderKey(ctx, created.ID, s.Provider, s.Key); err != nil {
		return agents.Agent{}, fmt.Errorf("factory: set provider key: %w", err)
	}

	return created, nil
}

// buildRecipe renders the agent's recipe file contents — the agent's "code":
// its prompt, provider/model, tools, and guardrails, in a simple readable
// key: value form.
func buildRecipe(s Spec) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\n", s.Name)
	fmt.Fprintf(&b, "provider: %s\n", s.Provider)
	fmt.Fprintf(&b, "model: %s\n", s.Model)
	fmt.Fprintf(&b, "mode: %s\n", s.Mode)
	fmt.Fprintf(&b, "prompt: %q\n", s.Prompt)
	fmt.Fprintf(&b, "tools: %v\n", s.Tools)
	fmt.Fprintf(&b, "blocked_tools: %v\n", s.BlockedTools)
	fmt.Fprintf(&b, "roles: %v\n", s.Roles)
	fmt.Fprintf(&b, "guid: %s\n", s.Guid)
	fmt.Fprintf(&b, "max_cost: %v\n", s.MaxCost)
	fmt.Fprintf(&b, "rate_limit: %v\n", s.RateLimit)
	return []byte(b.String())
}
