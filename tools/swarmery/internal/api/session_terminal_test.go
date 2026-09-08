package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// fakeClaudeProcInfo swaps sessionStartProcInfo for the duration of the test
// so hookSessionStart's identity check passes without a real OS process
// literally named "claude".
func fakeClaudeProcInfo(t *testing.T, tty string) {
	t.Helper()
	prev := sessionStartProcInfo
	sessionStartProcInfo = func(pid int) (*procwatch.ProcInfo, error) {
		return &procwatch.ProcInfo{PID: pid, StartTime: "Mon Jan  2 15:04:05 2006", Command: "claude", TTY: tty}, nil
	}
	t.Cleanup(func() { sessionStartProcInfo = prev })
}

func openTerminalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func postSessionStart(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/hooks/session-start", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.hookSessionStart(w, req)
	return w
}

func readTerminalCols(t *testing.T, db *sql.DB, sessionUUID string) (program, focusURL, bundleID, tty sql.NullString) {
	t.Helper()
	if err := db.QueryRow(
		`SELECT term_program, term_focus_url, term_bundle_id, term_tty FROM sessions WHERE session_uuid = ?`,
		sessionUUID,
	).Scan(&program, &focusURL, &bundleID, &tty); err != nil {
		t.Fatalf("read terminal columns for %s: %v", sessionUUID, err)
	}
	return
}

// ingest-before-hook: the sessions row already exists (as if the JSONL tail
// ingested it first), so hookSessionStart's UPDATE writes the four columns
// directly.
func TestHookSessionStartWritesTerminalColumnsWhenSessionAlreadyExists(t *testing.T) {
	db := openTerminalTestDB(t)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/proj', 'proj', '2026-09-07T00:00:00Z')`)
	execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'sid-already-ingested', 'active', '2026-09-07T00:00:00Z', 'jsonl')`)
	fakeClaudeProcInfo(t, "ttys004")

	h := &Handler{DB: db}
	body := `{"session_id":"sid-already-ingested","pid":4242,"cwd":"/tmp/proj",
		"terminal":{"program":"iTerm.app","bundleId":"com.googlecode.iterm2","sessionId":"w0t0p0:abc"}}`
	w := postSessionStart(t, h, body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (no drift seeded)", w.Code)
	}

	program, focusURL, bundleID, tty := readTerminalCols(t, db, "sid-already-ingested")
	if program.String != "iTerm.app" {
		t.Errorf("term_program = %q, want iTerm.app", program.String)
	}
	if bundleID.String != "com.googlecode.iterm2" {
		t.Errorf("term_bundle_id = %q, want com.googlecode.iterm2", bundleID.String)
	}
	if tty.String != "ttys004" {
		t.Errorf("term_tty = %q, want the PID-derived ttys004", tty.String)
	}
	if focusURL.Valid {
		t.Errorf("term_focus_url = %q, want NULL — absent from the payload", focusURL.String)
	}
}

// hook-before-ingest: the sessions row does not exist yet when the hook
// fires, so the terminal identity must be parked and then applied the moment
// the JSONL tail mints the row (ingest's session_started path).
func TestHookSessionStartParksTerminalUntilIngestCreatesTheRow(t *testing.T) {
	db := openTerminalTestDB(t)
	fakeClaudeProcInfo(t, "ttys009")

	const uuid = "sid-not-yet-ingested"
	h := &Handler{DB: db}
	body := `{"session_id":"` + uuid + `","pid":4343,"cwd":"/tmp/proj2",
		"terminal":{"program":"WarpTerminal","focusUrl":"warp://focus/xyz","bundleId":"dev.warp.Warp-Stable"}}`
	w := postSessionStart(t, h, body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 — fire-and-forget even though the row doesn't exist yet", w.Code)
	}

	var sessionCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_uuid = ?`, uuid).Scan(&sessionCount); err != nil {
		t.Fatal(err)
	}
	if sessionCount != 0 {
		t.Fatalf("sessions row already exists before ingest — test setup invalid")
	}

	// The JSONL tail now mints the row — this is the ingest session_started
	// path that must apply the parked terminal identity in the same tx as the
	// INSERT.
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"type":"user","parentUuid":null,"isSidechain":false,"promptId":"p-1","promptSource":"typed",` +
		`"message":{"role":"user","content":"hello"},"uuid":"ev-1","timestamp":"2026-09-07T10:00:00.000Z",` +
		`"cwd":"/tmp/proj2","sessionId":"` + uuid + `","version":"2.1.170","gitBranch":"main"}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ingest.TailFile(db, transcript, "", ingest.DefaultThresholds())
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if !res.SessionCreated {
		t.Fatal("want the tail to mint a new session row")
	}

	program, focusURL, bundleID, tty := readTerminalCols(t, db, uuid)
	if program.String != "WarpTerminal" {
		t.Errorf("term_program = %q, want the parked WarpTerminal", program.String)
	}
	if focusURL.String != "warp://focus/xyz" {
		t.Errorf("term_focus_url = %q, want the parked warp://focus/xyz", focusURL.String)
	}
	if bundleID.String != "dev.warp.Warp-Stable" {
		t.Errorf("term_bundle_id = %q, want the parked dev.warp.Warp-Stable", bundleID.String)
	}
	if tty.String != "ttys009" {
		t.Errorf("term_tty = %q, want the parked ttys009", tty.String)
	}
}

// A PID that isn't a claude process must not park anything — the identity
// gate returns before the terminal object is ever looked at.
func TestHookSessionStartNonClaudePIDParksNothing(t *testing.T) {
	db := openTerminalTestDB(t)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/proj', 'proj', '2026-09-07T00:00:00Z')`)

	h := &Handler{DB: db}
	// PID 1 is launchd/init in the real OsProvider path; sessionStartProcInfo
	// is left at its production default here on purpose.
	body := `{"session_id":"sid-non-claude","pid":1,"cwd":"/tmp/proj","terminal":{"program":"iTerm.app"}}`
	w := postSessionStart(t, h, body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"type":"user","parentUuid":null,"isSidechain":false,"promptId":"p-1","promptSource":"typed",` +
		`"message":{"role":"user","content":"hello"},"uuid":"ev-1","timestamp":"2026-09-07T10:00:00.000Z",` +
		`"cwd":"/tmp/proj","sessionId":"sid-non-claude","version":"2.1.170","gitBranch":"main"}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.TailFile(db, transcript, "", ingest.DefaultThresholds()); err != nil {
		t.Fatalf("tail: %v", err)
	}

	program, focusURL, bundleID, tty := readTerminalCols(t, db, "sid-non-claude")
	if program.Valid || focusURL.Valid || bundleID.Valid || tty.Valid {
		t.Errorf("terminal columns = %q/%q/%q/%q, want all NULL — the PID never verified as claude",
			program.String, focusURL.String, bundleID.String, tty.String)
	}
}

// ── DTO shape: null vs populated (SessionTerminal) ───────────────────────────

func TestSessionTerminalDTONullWhenAllColumnsEmpty(t *testing.T) {
	var s sessionTerminalScan
	if got := s.dto(); got != nil {
		t.Errorf("dto() = %+v, want nil for all-NULL columns", got)
	}
}

func TestSessionTerminalDTOPresentWhenAnyColumnSet(t *testing.T) {
	s := sessionTerminalScan{
		program: sql.NullString{String: "iTerm.app", Valid: true},
		tty:     sql.NullString{String: "ttys003", Valid: true},
	}
	got := s.dto()
	if got == nil {
		t.Fatal("dto() = nil, want a populated DTO")
	}
	if got.Program != "iTerm.app" || got.TTY != "ttys003" {
		t.Errorf("dto() = %+v, want Program=iTerm.app TTY=ttys003", got)
	}
	if got.FocusURL != "" || got.BundleID != "" {
		t.Errorf("dto() = %+v, want the unset fields empty", got)
	}
}

// GetSession end-to-end: a session with no terminal columns at all serializes
// "terminal": null explicitly, never an omitted key — that null is the
// widget's signal to offer "open in dashboard" instead of "focus the terminal".
func TestGetSessionTerminalIsExplicitNullWhenUnset(t *testing.T) {
	db := openTerminalTestDB(t)
	execSQL(t, db, `INSERT INTO projects (id, path, slug, first_seen) VALUES (1, '/tmp/p', 'p', '2026-09-07T00:00:00Z')`)
	execSQL(t, db, `INSERT INTO sessions (id, project_id, session_uuid, status, started_at, source)
		VALUES (1, 1, 'sid-plain', 'active', '2026-09-07T00:00:00Z', 'jsonl')`)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var raw map[string]json.RawMessage
	getJSON(t, srv.URL+"/api/sessions/1", &raw)
	if got := string(raw["terminal"]); got != "null" {
		t.Errorf(`terminal = %s, want the literal "null"`, got)
	}
}
