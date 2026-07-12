// Package store owns the SQLite database: opening, pragmas, and migrations.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DefaultDBPath returns ~/.swarmery/swarmery.db.
func DefaultDBPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".swarmery", "swarmery.db"), nil
}

// Open opens (creating if needed) the SQLite database at path, enables WAL and
// foreign keys, and applies pending migrations.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc.org/sqlite is not safe for concurrent writers on one connection pool
	// with WAL from multiple conns in-process; a single conn keeps the skeleton simple.
	db.SetMaxOpenConns(1)
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
