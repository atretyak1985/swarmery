package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// titleOnlyLine is the whole content of the transcripts that produced the
// phantom sessions: an ai-title record with no envelope — no uuid, no cwd, no
// timestamp. Claude Code writes these files; 50 of them sat in one projects
// root of the reporting machine.
const titleOnlyLine = `{"type":"ai-title","aiTitle":"Review AI coding agent execution trajectory","sessionId":"7dfbdf21-8196-4460-9f92-a94a9416c311"}` + "\n"

// assertNoPhantom fails if the batch minted anything: a timestamp-less
// session row, or the '(unknown)' project such a row hangs off.
func assertNoPhantom(t *testing.T, db *sql.DB) {
	t.Helper()
	if got := count(t, db, `SELECT COUNT(*) FROM sessions`); got != 0 {
		t.Errorf("sessions = %d, want 0 — a title-only transcript is not a session", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM projects WHERE path = ?`, UnknownProjectPath); got != 0 {
		t.Errorf("'(unknown)' projects = %d, want 0", got)
	}
}

// TestTitleOnlyTranscriptMintsNoSession pins the root cause: a transcript
// carrying no timestamp anywhere is not evidence that a session ran, so
// neither ingest path may mint a session row. Before the fix each such file
// produced a session with started_at "" that the status ticker could never
// move — the dashboard reported dozens of "active" agents with no process
// behind them.
func TestTitleOnlyTranscriptMintsNoSession(t *testing.T) {
	t.Run("File", func(t *testing.T) {
		db := testDB(t)
		path := filepath.Join(t.TempDir(), "title-only.jsonl")
		if err := os.WriteFile(path, []byte(titleOnlyLine), 0o644); err != nil {
			t.Fatal(err)
		}
		stats, err := File(db, path)
		if err != nil {
			t.Fatalf("ingest: %v", err)
		}
		if stats.Sessions != 0 {
			t.Errorf("stats.Sessions = %d, want 0", stats.Sessions)
		}
		assertNoPhantom(t, db)
	})

	t.Run("TailFile", func(t *testing.T) {
		db := testDB(t)
		path := filepath.Join(t.TempDir(), "title-only.jsonl")
		if err := os.WriteFile(path, []byte(titleOnlyLine), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := TailFile(db, path, "", DefaultThresholds())
		if err != nil {
			t.Fatalf("tail: %v", err)
		}
		if res.NextOffset != 0 {
			t.Errorf("NextOffset = %d, want 0 — the batch must stay re-readable", res.NextOffset)
		}
		assertNoPhantom(t, db)
	})
}

// TestTitleOnlyTranscriptHealsWhenRecordsArrive is the other half of the
// contract: skipping must not LOSE anything. The title-only batch leaves the
// offset untouched, so once the transcript grows real records the session is
// created from the full file — with the title that arrived first.
func TestTitleOnlyTranscriptHealsWhenRecordsArrive(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(t.TempDir(), "grows.jsonl")
	if err := os.WriteFile(path, []byte(titleOnlyLine), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TailFile(db, path, "", DefaultThresholds()); err != nil {
		t.Fatalf("tail 1: %v", err)
	}
	assertNoPhantom(t, db)

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	real := `{"type":"user","uuid":"u-1","sessionId":"7dfbdf21-8196-4460-9f92-a94a9416c311",` +
		`"cwd":"/work/repo","gitBranch":"main","timestamp":"` + ts + `",` +
		`"message":{"role":"user","content":"hello"}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(real); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if _, err := TailFile(db, path, "", DefaultThresholds()); err != nil {
		t.Fatalf("tail 2: %v", err)
	}

	var startedAt, title, cwd string
	if err := db.QueryRow(
		`SELECT started_at, COALESCE(title,''), COALESCE(cwd,'') FROM sessions`,
	).Scan(&startedAt, &title, &cwd); err != nil {
		t.Fatalf("session after growth: %v", err)
	}
	if startedAt != ts {
		t.Errorf("started_at = %q, want %q", startedAt, ts)
	}
	if title != "Review AI coding agent execution trajectory" {
		t.Errorf("title = %q — the skipped ai-title line must not be lost", title)
	}
	if cwd != "/work/repo" {
		t.Errorf("cwd = %q, want /work/repo", cwd)
	}
}

// TestStatusRecomputeClosesTimestamplessSession is the backstop: whatever
// channel minted a row with no parseable timestamp on either end, the status
// ticker must close it instead of skipping it. Skipping is what froze 40 rows
// in 'active' for the life of the database. The liveness override still wins:
// a row procwatch believes is alive caps at 'idle', never 'completed'.
func TestStatusRecomputeClosesTimestamplessSession(t *testing.T) {
	db := testDB(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, ?, ?, '')`,
		UnknownProjectPath, UnknownProjectPath); err != nil {
		t.Fatal(err)
	}
	insert := func(uuid, status string, procState any) int64 {
		r, err := db.Exec(
			`INSERT INTO sessions (project_id, session_uuid, status, started_at, ended_at, proc_state)
			 VALUES (1, ?, ?, '', NULL, ?)`, uuid, status, procState)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	stuck := insert("phantom-active", "active", nil)
	alive := insert("phantom-alive", "active", "running")

	changes, err := RecomputeStatuses(db, Thresholds{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(changes))
	}

	want := map[int64]string{stuck: "completed", alive: "idle"}
	for id, w := range want {
		var got string
		if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != w {
			t.Errorf("session %d status = %q, want %q", id, got, w)
		}
	}
}
