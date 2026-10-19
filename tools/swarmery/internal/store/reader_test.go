package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The read pool must not be a second writer: query_only turns any write into
// SQLITE_READONLY rather than a contender for the store's write lock.
func TestOpenReaderRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, err = r.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/p', 'p', '2026-10-19T00:00:00Z')`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("insert through the reader: err = %v, want a readonly error", err)
	}
}

// THE property the pool exists for: a read returns while the writer holds an
// open write transaction, sees only committed data, and sees the write once it
// commits.
func TestOpenReaderNotBlockedByWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.db")
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	ctx := context.Background()
	conn, err := w.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO projects (path, slug, first_seen) VALUES ('/p', 'p', '2026-10-19T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	rctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var n int
	if err := r.QueryRowContext(rctx, `SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("read while the writer holds a transaction: %v", err)
	}
	if n != 0 {
		t.Fatalf("reader saw %d uncommitted rows, want 0", n)
	}

	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := r.QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reader saw %d rows after commit, want 1", n)
	}
}

// A missing store is an error, not a new empty database a caller would then
// read "nothing" from.
func TestOpenReaderMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")
	if db, err := OpenReader(path); err == nil {
		db.Close()
		t.Fatal("OpenReader on a missing file succeeded")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("OpenReader left a file behind: %v", err)
	}
}
