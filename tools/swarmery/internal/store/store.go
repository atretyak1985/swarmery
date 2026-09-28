// Package store owns the SQLite database: opening, pragmas, and migrations.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// OpenNoMigrate opens the database at path with exactly Open's DSN (WAL,
// foreign keys, busy timeout) and single-connection pool, but applies NO
// migration. It is the CLI's handle: `swarmery account switch|move-session`
// read (and move-session writes one column) while the daemon may be running,
// and a terminal command must never migrate the schema behind the daemon's back
// — the side effect store.Open (and therefore `swarmery backup`) carries.
//
// Not a `mode=ro` URI on purpose: SQLite cannot create the -shm a WAL database
// needs on a read-only connection, so mode=ro fails outright on a cleanly-closed
// store (see the note beside `swarmery backup` in cmd/swarmery/main.go).
//
// The file must already exist: a missing database is reported as an error
// rather than created empty, so a caller can degrade to "unknown" instead of
// leaving a blank database behind. A table a pending migration would create
// is simply absent — callers must treat "no such table" as unknown.
func OpenNoMigrate(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	return db, nil
}

// Backup writes a consistent, defragmented snapshot of the database at srcPath
// to destPath using SQLite's `VACUUM INTO`. It is safe to run against a live
// WAL database (the daemon may keep serving) — VACUUM takes only a brief read
// lock and the snapshot is a single self-contained file with no -wal/-shm
// sidecars. destPath must not already exist; its parent dir is created as
// needed. Returns the snapshot's size in bytes.
func Backup(srcPath, destPath string) (int64, error) {
	if _, err := os.Stat(destPath); err == nil {
		return 0, fmt.Errorf("backup target already exists: %s", destPath)
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("stat backup target: %w", err)
	}
	if dir := filepath.Dir(destPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return 0, fmt.Errorf("create backup dir: %w", err)
		}
	}
	db, err := Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	// VACUUM INTO takes a single-quoted string literal; escape any embedded
	// single quote in the path per SQLite literal rules ('' == one quote).
	escaped := strings.ReplaceAll(destPath, "'", "''")
	if _, err := db.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
		return 0, fmt.Errorf("vacuum into %s: %w", destPath, err)
	}
	fi, err := os.Stat(destPath)
	if err != nil {
		return 0, fmt.Errorf("stat snapshot: %w", err)
	}
	return fi.Size(), nil
}
