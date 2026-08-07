// Package memory implements the guid-scoped memory API (get/set/search) over
// the dict and stack tables (§8: memory = whatever lives under a guid). Keys
// and values live inside dict.full_detail (JSONB); stack summaries are
// searchable breadcrumbs tagged with the same guid via stack.run_id.
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/giantbeaver9/yuno/internal/store"
	"github.com/jackc/pgx/v5"
)

// ErrEmptyGuid is returned by Set when called with an empty guid.
var ErrEmptyGuid = errors.New("memory: guid must not be empty")

// Store is the guid handle over dict + stack.
type Store struct {
	q store.Querier
}

// New builds a Store over the given Querier (a *pgxpool.Pool or pgx.Tx).
func New(q store.Querier) *Store {
	return &Store{q: q}
}

// Set upserts key->value inside dict.full_detail for guid. Empty guid is an
// error.
func (s *Store) Set(ctx context.Context, guid, key, value string) error {
	if guid == "" {
		return ErrEmptyGuid
	}
	const q = `
		INSERT INTO dict (guid, full_detail)
		VALUES ($1, jsonb_build_object($2::text, $3::text))
		ON CONFLICT (guid) DO UPDATE
		SET full_detail = dict.full_detail || jsonb_build_object($2::text, $3::text)
	`
	if _, err := s.q.Exec(ctx, q, guid, key, value); err != nil {
		return fmt.Errorf("memory: set %s/%s: %w", guid, key, err)
	}
	return nil
}

// Get reads dict.full_detail->>key. Missing key -> ("", false, nil); absence
// is not an error.
func (s *Store) Get(ctx context.Context, guid, key string) (value string, ok bool, err error) {
	const q = `SELECT full_detail ->> $2 FROM dict WHERE guid = $1`
	var v *string
	if err := s.q.QueryRow(ctx, q, guid, key).Scan(&v); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("memory: get %s/%s: %w", guid, key, err)
	}
	if v == nil {
		// Key absent from full_detail: ->> yields SQL NULL, not an error.
		return "", false, nil
	}
	return *v, true, nil
}

// Search returns dict key/value pairs under guid whose key or value contains
// the query substring, plus matching stack summaries (stack.run_id = guid).
func (s *Store) Search(ctx context.Context, guid, query string) ([]Hit, error) {
	hits := make([]Hit, 0)

	var raw []byte
	err := s.q.QueryRow(ctx, `SELECT full_detail FROM dict WHERE guid = $1`, guid).Scan(&raw)
	switch {
	case err == nil:
		var detail map[string]string
		if err := json.Unmarshal(raw, &detail); err != nil {
			return nil, fmt.Errorf("memory: decode dict for %s: %w", guid, err)
		}
		for k, v := range detail {
			if strings.Contains(k, query) || strings.Contains(v, query) {
				hits = append(hits, Hit{Key: k, Value: v})
			}
		}
	case errors.Is(err, pgx.ErrNoRows):
		// No dict row for guid yet: nothing to search there.
	default:
		return nil, fmt.Errorf("memory: search dict for %s: %w", guid, err)
	}

	rows, err := s.q.Query(ctx, `SELECT summary FROM stack WHERE run_id = $1 ORDER BY seq`, guid)
	if err != nil {
		return nil, fmt.Errorf("memory: search stack for %s: %w", guid, err)
	}
	defer rows.Close()
	for rows.Next() {
		var summary string
		if err := rows.Scan(&summary); err != nil {
			return nil, fmt.Errorf("memory: scan stack for %s: %w", guid, err)
		}
		if strings.Contains(summary, query) {
			hits = append(hits, Hit{Key: "stack", Value: summary})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: iterate stack for %s: %w", guid, err)
	}

	return hits, nil
}

// Hit is one Search result: a dict key/value pair, or a stack breadcrumb
// (Key == "stack").
type Hit struct {
	Key   string
	Value string
}
