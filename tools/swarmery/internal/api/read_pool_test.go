package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// The dashboard's hot GETs must answer while the single writer connection is
// held — the shape of a labeler pass, an ingest rescan or a prune, which used
// to send /api/sessions to 30 s timeouts. Every endpoint moved to the read pool
// is listed; one that still touches the writer anywhere on its path blocks
// here until the client gives up.
func TestHotGetsServeWhileWriterHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := ingest.File(db, filepath.Join("..", "..", "testdata", "fixtures", "subagent-session.jsonl")); err != nil {
		t.Fatalf("ingest fixture: %v", err)
	}
	ro, err := store.OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { ro.Close() })
	AttachReader(ro)
	t.Cleanup(func() { AttachReader(nil) })
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var sessionID, projectID int64
	if err := db.QueryRow(`SELECT id, project_id FROM sessions LIMIT 1`).Scan(&sessionID, &projectID); err != nil {
		t.Fatalf("fixture session: %v", err)
	}

	// Hold the writer: its only connection, inside an open write transaction.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.ExecContext(ctx, `ROLLBACK`)
		conn.Close()
	})

	client := &http.Client{Timeout: 2 * time.Second}
	for _, p := range []string{
		"/api/sessions",
		fmt.Sprintf("/api/sessions/%d", sessionID),
		"/api/approvals",
		"/api/usage",
		"/api/stats/today",
		"/api/stats/overview",
		"/api/health",
		"/api/projects",
		fmt.Sprintf("/api/projects/%d", projectID),
	} {
		start := time.Now()
		resp, err := client.Get(srv.URL + p)
		if err != nil {
			t.Errorf("GET %s while the writer is held: %v — it still reads through the writer", p, err)
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d", p, resp.StatusCode)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("GET %s took %v with the writer held, want < 1s", p, d)
		}
	}
}
