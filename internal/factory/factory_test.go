package factory_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/giantbeaver9/yuno/internal/agents"
	"github.com/giantbeaver9/yuno/internal/factory"
	"github.com/giantbeaver9/yuno/internal/secretbox"
	"github.com/giantbeaver9/yuno/internal/testutil"
)

// testBox returns a *secretbox.Box built from an all-zero test key. Fine for
// tests; production keys come from config.
func testBox(t *testing.T) *secretbox.Box {
	t.Helper()
	box, err := secretbox.NewFromHex(strings.Repeat("0", 64))
	if err != nil {
		t.Fatalf("secretbox.NewFromHex: %v", err)
	}
	return box
}

// countFiles returns the number of directory entries under dir.
func countFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	return len(entries)
}

// [positive] CreateAgent with a valid spec returns the new agent id, writes a
// recipe file under agentsDir, inserts the agent row (recipe_path pointing at
// it), and stores the provider key encrypted (round-trips via GetProviderKey).
func TestCreateAgent_Valid(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	dir := t.TempDir()
	f := factory.New(ag, dir, testBox(t))

	spec := factory.Spec{
		Name:     "coder",
		Provider: "gemini",
		Key:      "sk-x",
	}

	created, err := f.CreateAgent(ctx, spec)
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if created.ID == 0 {
		t.Fatalf("CreateAgent: expected non-zero ID")
	}

	// Recipe file exists under agentsDir.
	if created.RecipePath == "" {
		t.Fatalf("CreateAgent: expected non-empty RecipePath")
	}
	if !strings.HasPrefix(filepath.Clean(created.RecipePath), filepath.Clean(dir)) {
		t.Fatalf("CreateAgent: recipe path %q not under agentsDir %q", created.RecipePath, dir)
	}
	if _, err := os.Stat(created.RecipePath); err != nil {
		t.Fatalf("CreateAgent: recipe file not found at %q: %v", created.RecipePath, err)
	}

	// Agent row present via agents.Get, with recipe_path matching.
	got, err := ag.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("agents.Get: %v", err)
	}
	if got.RecipePath != created.RecipePath {
		t.Fatalf("agents.Get: RecipePath = %q, want %q", got.RecipePath, created.RecipePath)
	}
	if got.Name != "coder" || got.Provider != "gemini" {
		t.Fatalf("agents.Get: unexpected row %+v", got)
	}

	// Provider key stored encrypted; round-trips via GetProviderKey.
	key, err := ag.GetProviderKey(ctx, created.ID, "gemini")
	if err != nil {
		t.Fatalf("agents.GetProviderKey: %v", err)
	}
	if key != "sk-x" {
		t.Fatalf("agents.GetProviderKey: got %q, want %q", key, "sk-x")
	}
}

// [negative] A path-traversal name is rejected: no file created, no agent row
// inserted.
func TestCreateAgent_PathTraversalName(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	dir := t.TempDir()
	f := factory.New(ag, dir, testBox(t))

	cases := []string{"../../etc/passwd", "../evil", "a/b"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.CreateAgent(ctx, factory.Spec{Name: name, Provider: "gemini"})
			if err == nil {
				t.Fatalf("CreateAgent(%q): expected error, got nil", name)
			}

			// Nothing written under agentsDir.
			if n := countFiles(t, dir); n != 0 {
				t.Fatalf("CreateAgent(%q): expected 0 files under agentsDir, got %d", name, n)
			}

			// No agent row inserted anywhere (list should stay empty since
			// this is the only creator in this schema).
			all, err := ag.List(ctx)
			if err != nil {
				t.Fatalf("agents.List: %v", err)
			}
			if len(all) != 0 {
				t.Fatalf("CreateAgent(%q): expected no agent rows, got %d", name, len(all))
			}
		})
	}
}

// [negative] A non-whitelisted provider is rejected: no file, no row.
func TestCreateAgent_BadProvider(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	dir := t.TempDir()
	f := factory.New(ag, dir, testBox(t))

	_, err := f.CreateAgent(ctx, factory.Spec{Name: "x", Provider: "openai"})
	if err == nil {
		t.Fatalf("CreateAgent: expected error for unwhitelisted provider, got nil")
	}

	if n := countFiles(t, dir); n != 0 {
		t.Fatalf("CreateAgent: expected 0 files under agentsDir, got %d", n)
	}
	all, err := ag.List(ctx)
	if err != nil {
		t.Fatalf("agents.List: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("CreateAgent: expected no agent rows, got %d", len(all))
	}
}

// [edge] Empty Tools list is allowed.
func TestCreateAgent_EmptyTools(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	dir := t.TempDir()
	f := factory.New(ag, dir, testBox(t))

	created, err := f.CreateAgent(ctx, factory.Spec{
		Name:     "no-tools",
		Provider: "huggingface",
		Tools:    []string{},
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	got, err := ag.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("agents.Get: %v", err)
	}
	if len(got.Tools) != 0 {
		t.Fatalf("agents.Get: expected empty tools, got %v", got.Tools)
	}
}

// [edge] Two agents with names differing only by case/spacing get distinct
// safe filenames (no clobber).
func TestCreateAgent_CaseSpacingDistinctFilenames(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	dir := t.TempDir()
	f := factory.New(ag, dir, testBox(t))

	a1, err := f.CreateAgent(ctx, factory.Spec{Name: "Coder", Provider: "gemini"})
	if err != nil {
		t.Fatalf("CreateAgent(Coder): %v", err)
	}
	a2, err := f.CreateAgent(ctx, factory.Spec{Name: "coder", Provider: "gemini"})
	if err != nil {
		t.Fatalf("CreateAgent(coder): %v", err)
	}

	if a1.RecipePath == a2.RecipePath {
		t.Fatalf("expected distinct recipe paths, both got %q", a1.RecipePath)
	}
	if _, err := os.Stat(a1.RecipePath); err != nil {
		t.Fatalf("recipe file missing for a1: %v", err)
	}
	if _, err := os.Stat(a2.RecipePath); err != nil {
		t.Fatalf("recipe file missing for a2: %v", err)
	}
}

// [edge] Creating a second agent whose sanitized name collides with an
// existing recipe file is rejected (no silent overwrite), and nothing extra
// is written or inserted for the rejected attempt.
func TestCreateAgent_NameCollisionRejected(t *testing.T) {
	ctx := context.Background()
	st := testutil.NewDB(t)
	ag := agents.New(st.Pool, testBox(t))
	dir := t.TempDir()
	f := factory.New(ag, dir, testBox(t))

	first, err := f.CreateAgent(ctx, factory.Spec{Name: "dup", Provider: "gemini"})
	if err != nil {
		t.Fatalf("CreateAgent(dup) #1: %v", err)
	}

	before, err := ag.List(ctx)
	if err != nil {
		t.Fatalf("agents.List: %v", err)
	}

	_, err = f.CreateAgent(ctx, factory.Spec{Name: "dup", Provider: "gemini"})
	if err == nil {
		t.Fatalf("CreateAgent(dup) #2: expected error on collision, got nil")
	}

	after, err := ag.List(ctx)
	if err != nil {
		t.Fatalf("agents.List: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("CreateAgent(dup) #2: expected no new agent row, before=%d after=%d", len(before), len(after))
	}

	// Original recipe file untouched / still present.
	if _, err := os.Stat(first.RecipePath); err != nil {
		t.Fatalf("original recipe file missing: %v", err)
	}
}
