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
//
// The store holds transcripts, so its files are private to the user: a new
// directory is created 0700 (and the default ~/.swarmery is tightened to 0700
// when it already exists), a new database file is created 0600 before SQLite
// opens it — SQLite gives the -wal and -shm sidecars the database file's mode —
// and after the migration the database and any sidecars are set to 0600.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
		if isDefaultDir(dir) {
			if err := os.Chmod(dir, 0o700); err != nil {
				return nil, fmt.Errorf("chmod db dir: %w", err)
			}
		}
	}
	if err := createPrivate(path); err != nil {
		return nil, fmt.Errorf("create db file: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// The WRITER stays a single connection, deliberately: SQLite serializes
	// writers anyway, and code across the daemon relies on one connection (the
	// "collect, then write" cursors in wsingest, the bounded prune transaction).
	// The daemon's reads that must not queue behind it go through OpenReader.
	db.SetMaxOpenConns(1)
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := privateDBFiles(path); err != nil {
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

// ReaderConns bounds the read pool. WAL lets these read concurrently with the
// single writer from Open; four covers the dashboard's parallel page loads
// without multiplying page caches (each connection keeps its own).
const ReaderConns = 4

// OpenReader opens a read-only pool on an existing database: Open's DSN plus
// query_only, so a write through it fails with SQLITE_READONLY instead of
// competing with the writer. Under WAL a reader sees every committed write and
// never waits on the writer, which is the point: the API's hot reads used to
// queue behind whatever held the single connection (a labeler pass, an ingest
// rescan, a prune) and time out.
//
// Not a mode=ro URI, for the -shm reason on OpenNoMigrate. It applies no
// migration; the daemon opens it after Open has migrated. Like OpenNoMigrate it
// refuses a missing file rather than creating an empty database.
func OpenReader(path string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	db.SetMaxOpenConns(ReaderConns)
	db.SetMaxIdleConns(ReaderConns)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	return db, nil
}

// Backup writes a consistent, defragmented snapshot of the database at srcPath
// to destPath using SQLite's `VACUUM INTO`. It is safe to run against a live
// WAL database (the daemon may keep serving) — VACUUM takes only a brief read
// lock and the snapshot is a single self-contained file with no -wal/-shm
// sidecars. destPath must not already exist; its parent dir is created as
// needed (0700), and the snapshot is set to 0600. Returns the snapshot's size
// in bytes.
func Backup(srcPath, destPath string) (int64, error) {
	if _, err := os.Stat(destPath); err == nil {
		return 0, fmt.Errorf("backup target already exists: %s", destPath)
	} else if !os.IsNotExist(err) {
		return 0, fmt.Errorf("stat backup target: %w", err)
	}
	if dir := filepath.Dir(destPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
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
	// A snapshot is the whole store: private like the database itself.
	if err := os.Chmod(destPath, 0o600); err != nil {
		return 0, fmt.Errorf("chmod snapshot: %w", err)
	}
	fi, err := os.Stat(destPath)
	if err != nil {
		return 0, fmt.Errorf("stat snapshot: %w", err)
	}
	return fi.Size(), nil
}
