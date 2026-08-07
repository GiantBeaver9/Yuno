// Package testutil provides shared test helpers. NewDB gives each test an
// isolated Postgres schema with the full Yuno schema migrated into it, so the
// whole suite can run in parallel against one database without collisions.
//
// It reads TEST_DATABASE_URL (falling back to a local dev default). If no
// database is reachable the test is skipped rather than failed, so `go test
// ./...` stays green on machines without Postgres; CI and the build gates set
// TEST_DATABASE_URL so the tests actually run.
package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultURL is the local dev Postgres used when TEST_DATABASE_URL is unset.
const DefaultURL = "postgres://yuno@127.0.0.1:5433/yuno?sslmode=disable"

// TestURL returns the configured test database URL.
func TestURL() string {
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		return v
	}
	return DefaultURL
}

var schemaSeq int64

// NewDB returns a *store.Store bound to a fresh, isolated schema. The schema is
// dropped and the pool closed automatically via t.Cleanup. Skips the test if
// Postgres is unreachable.
func NewDB(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()

	// Unique schema name per test invocation.
	schemaSeq++
	name := sanitize(t.Name())
	schema := fmt.Sprintf("t_%s_%d_%d", name, time.Now().UnixNano(), schemaSeq)
	if len(schema) > 60 {
		schema = schema[:60]
	}

	cfg, err := pgxpool.ParseConfig(TestURL())
	if err != nil {
		t.Fatalf("parse test db url: %v", err)
	}
	// Every connection in the pool defaults to our private schema.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("postgres unavailable (%v); set TEST_DATABASE_URL to run db tests", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unreachable (%v); set TEST_DATABASE_URL to run db tests", err)
	}

	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		pool.Close()
		t.Fatalf("create schema: %v", err)
	}

	st := &store.Store{Pool: pool}
	if err := st.Migrate(ctx); err != nil {
		_, _ = pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}

	t.Cleanup(func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = pool.Exec(dctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		pool.Close()
	})

	return st
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
