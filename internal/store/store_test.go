package store_test

import (
	"context"
	"testing"

	"github.com/giantbeaver9/yuno/internal/testutil"
)

// TestMigrateCreatesTables verifies the embedded schema applies and the core
// tables exist in the isolated schema.
func TestMigrateCreatesTables(t *testing.T) {
	st := testutil.NewDB(t)
	ctx := context.Background()

	want := []string{"project", "build", "ticket", "agent", "workflow",
		"node", "edge", "run", "message", "stack", "dict", "schedule"}
	for _, tbl := range want {
		var reg *string
		err := st.Pool.QueryRow(ctx, "SELECT to_regclass($1)", tbl).Scan(&reg)
		if err != nil {
			t.Fatalf("to_regclass(%s): %v", tbl, err)
		}
		if reg == nil {
			t.Errorf("table %q was not created by migration", tbl)
		}
	}
}

// TestMigrateIdempotent verifies re-running the migration is a no-op (IF NOT
// EXISTS DDL), so boot-time migration is safe on an existing database.
func TestMigrateIdempotent(t *testing.T) {
	st := testutil.NewDB(t)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate should be idempotent: %v", err)
	}
}
