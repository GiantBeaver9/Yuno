package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// Store wraps the SQLite connection pool and exposes typed CRUD helpers.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, applies the
// schema, and seeds first-run example data. The pure-Go modernc driver is used
// so the binary cross-compiles to ARM without CGO.
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single writer avoids "database is locked" churn on a small Pi workload.
	d.SetMaxOpenConns(1)
	if err := d.Ping(); err != nil {
		return nil, err
	}
	if _, err := d.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	s := &Store{db: d}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate columns: %w", err)
	}
	if err := s.seed(); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// migrate adds columns introduced after a database may already exist. SQLite has
// no "ADD COLUMN IF NOT EXISTS", so we diff PRAGMA table_info and add what's
// missing — idempotent, so upgrading an existing Pi database just works.
func (s *Store) migrate() error {
	adds := []struct{ table, column, ddl string }{
		{"events", "recurrence", "ALTER TABLE events ADD COLUMN recurrence TEXT NOT NULL DEFAULT 'none'"},
		{"events", "recurrence_until", "ALTER TABLE events ADD COLUMN recurrence_until TEXT NOT NULL DEFAULT ''"},
		{"events", "countdown", "ALTER TABLE events ADD COLUMN countdown INTEGER NOT NULL DEFAULT 0"},
		{"events", "important", "ALTER TABLE events ADD COLUMN important INTEGER NOT NULL DEFAULT 0"},
	}
	for _, a := range adds {
		has, err := s.hasColumn(a.table, a.column)
		if err != nil {
			return err
		}
		if !has {
			if _, err := s.db.Exec(a.ddl); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
