package memory_test

import (
	"context"
	"testing"

	"github.com/giantbeaver9/yuno/internal/memory"
	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/giantbeaver9/yuno/internal/testutil"
)

// runGUID inserts a minimal run row (raw SQL, out of unit scope) so a stack
// row can satisfy stack.run_id's FK, and returns the guid.
func runGUID(t *testing.T, ctx context.Context, st *store.Store, guid string) string {
	t.Helper()
	if _, err := st.Pool.Exec(ctx, "INSERT INTO run (guid) VALUES ($1)", guid); err != nil {
		t.Fatalf("insert run %q: %v", guid, err)
	}
	return guid
}

// --- Set / Get ---

func TestSetThenGetRoundTrips(t *testing.T) {
	// [positive] Set(g,"lang","go") then Get(g,"lang") -> ("go", true, nil)
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	if err := m.Set(ctx, "guid-1", "lang", "go"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok, err := m.Get(ctx, "guid-1", "lang")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for a key that was set")
	}
	if value != "go" {
		t.Fatalf("expected value %q, got %q", "go", value)
	}
}

func TestGetMissingKeyReturnsFalseNoError(t *testing.T) {
	// [negative] Get(g,"absent") -> ("", false, nil) — no error
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	if err := m.Set(ctx, "guid-1", "lang", "go"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	value, ok, err := m.Get(ctx, "guid-1", "absent")
	if err != nil {
		t.Fatalf("expected no error for missing key, got %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for a key never set")
	}
	if value != "" {
		t.Fatalf("expected empty value for missing key, got %q", value)
	}
}

func TestGetOnUnknownGuidReturnsFalseNoError(t *testing.T) {
	// [negative] Get on a guid with no dict row at all -> ("", false, nil)
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	value, ok, err := m.Get(ctx, "never-set-guid", "lang")
	if err != nil {
		t.Fatalf("expected no error for unknown guid, got %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false for unknown guid")
	}
	if value != "" {
		t.Fatalf("expected empty value for unknown guid, got %q", value)
	}
}

func TestSetWithEmptyGuidErrors(t *testing.T) {
	// [negative] Set("","k","v") (empty guid) -> error
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	err := m.Set(ctx, "", "k", "v")
	if err == nil {
		t.Fatalf("expected error for empty guid, got nil")
	}
}

func TestSetOverwritesKeyPreservingSiblings(t *testing.T) {
	// [edge] Set(g,"k","v1") then Set(g,"k","v2") -> Get returns "v2"; a
	// different key set earlier under the same guid is untouched (JSONB merge).
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	if err := m.Set(ctx, "guid-1", "other", "untouched"); err != nil {
		t.Fatalf("Set other: %v", err)
	}
	if err := m.Set(ctx, "guid-1", "k", "v1"); err != nil {
		t.Fatalf("Set k=v1: %v", err)
	}
	if err := m.Set(ctx, "guid-1", "k", "v2"); err != nil {
		t.Fatalf("Set k=v2: %v", err)
	}

	value, ok, err := m.Get(ctx, "guid-1", "k")
	if err != nil {
		t.Fatalf("Get k: %v", err)
	}
	if !ok || value != "v2" {
		t.Fatalf("expected overwritten value (\"v2\", true), got (%q, %v)", value, ok)
	}

	other, ok, err := m.Get(ctx, "guid-1", "other")
	if err != nil {
		t.Fatalf("Get other: %v", err)
	}
	if !ok || other != "untouched" {
		t.Fatalf("expected sibling key preserved (\"untouched\", true), got (%q, %v)", other, ok)
	}
}

func TestGuidIsolation(t *testing.T) {
	// [edge] a key set under guid A is not visible under guid B.
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	if err := m.Set(ctx, "guid-a", "secret", "a-value"); err != nil {
		t.Fatalf("Set under guid-a: %v", err)
	}

	value, ok, err := m.Get(ctx, "guid-b", "secret")
	if err != nil {
		t.Fatalf("Get under guid-b: %v", err)
	}
	if ok {
		t.Fatalf("expected key set under guid-a to be invisible under guid-b, got value %q", value)
	}
}

// --- Search ---

func TestSearchMatchesDictKeyOrValue(t *testing.T) {
	// [positive] Search(g,"go") returns a hit with Value=="go"
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	if err := m.Set(ctx, "guid-1", "lang", "go"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	hits, err := m.Search(ctx, "guid-1", "go")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.Value == "go" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a hit with Value %q, got %+v", "go", hits)
	}
}

func TestSearchIncludesStackSummaries(t *testing.T) {
	// [positive] Search matches stack.summary rows where stack.run_id = guid.
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)
	guid := runGUID(t, ctx, st, "guid-with-run")

	if _, err := st.Pool.Exec(ctx,
		"INSERT INTO stack (run_id, seq, summary) VALUES ($1, 1, $2)",
		guid, "did some breadcrumb work"); err != nil {
		t.Fatalf("insert stack row: %v", err)
	}

	hits, err := m.Search(ctx, guid, "breadcrumb")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.Key == "stack" && h.Value == "did some breadcrumb work" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a stack hit, got %+v", hits)
	}
}

func TestSearchNoMatchReturnsEmpty(t *testing.T) {
	// [edge] Search(g,"nomatch") -> empty slice
	ctx := context.Background()
	st := testutil.NewDB(t)
	m := memory.New(st.Pool)

	if err := m.Set(ctx, "guid-1", "lang", "go"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	hits, err := m.Search(ctx, "guid-1", "nomatch")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected empty hits, got %+v", hits)
	}
}
