// Package memory implements the persistent memory storage layer for pigo.
//
// It is backed by a pure-Go SQLite database (modernc.org/sqlite, no CGO) with
// an FTS5 full-text index over Markdown memory files. This file provides the
// Store type together with database opening and idempotent schema migration.
// Later nodes extend the package with path resolution, reconcile (lazy
// indexing/pruning) and BM25 search.
package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers the "sqlite" driver
)

// Store is the handle to the memory database. Fields are unexported; later
// nodes access the underlying *sql.DB via DB() and the configured roots via the
// package-internal fields.
type Store struct {
	db     *sql.DB
	root   string // memory root directory (magic layout: global/projects/sessions)
	ccBase string // optional Claude Code base dir for the "cc" scope; "" disables
}

// Open opens (creating if necessary) the SQLite database at dbPath and runs the
// idempotent schema migration. The parent directory of dbPath is created with
// os.MkdirAll before opening. root is the memory root directory and ccBase is
// the optional Claude Code base directory; both are retained for use by later
// nodes and are not required to exist here.
//
// Calling Open twice on the same file is safe: the migration uses
// CREATE ... IF NOT EXISTS throughout.
func Open(dbPath, root, ccBase string) (*Store, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("memory: empty dbPath")
	}

	// modernc.org/sqlite understands the ":memory:" DSN; only create a parent
	// directory for real file paths.
	if dbPath != ":memory:" {
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("memory: create db dir %q: %w", dir, err)
			}
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("memory: open db %q: %w", dbPath, err)
	}

	// A single global DB with a single connection: writes are serialized, which
	// matches the low write frequency and avoids SQLITE_BUSY contention.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: ping db %q: %w", dbPath, err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}

	return &Store{db: db, root: root, ccBase: ccBase}, nil
}

// migrate applies the idempotent schema DDL.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaDDL); err != nil {
		return err
	}
	return nil
}

// DB returns the underlying *sql.DB for use by later nodes and tests. It may be
// nil if the store was not successfully opened.
func (s *Store) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
