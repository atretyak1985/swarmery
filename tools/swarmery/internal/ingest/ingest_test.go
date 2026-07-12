package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const fixtures = "../../testdata/fixtures"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func TestIngestSimpleSession(t *testing.T) {
	db := testDB(t)
	stats, err := File(db, filepath.Join(fixtures, "simple-session.jsonl"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := count(t, db, `SELECT COUNT(*) FROM projects`); got != 1 {
		t.Errorf("projects = %d, want 1", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM sessions`); got != 1 {
		t.Errorf("sessions = %d, want 1", got)
	}
	// Turns: user, assistant msg 0301 (2 split lines), assistant msg 0302,
	// user, assistant msg 0303 → 5.
	if got := count(t, db, `SELECT COUNT(*) FROM turns`); got != 5 {
		t.Errorf("turns = %d, want 5", got)
	}
	// Events: user_prompt, tool_call(Read), user_prompt → 3.
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 3 {
		t.Errorf("events = %d, want 3", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM file_changes`); got != 0 {
		t.Errorf("file_changes = %d, want 0", got)
	}
	if stats.Sessions != 1 || stats.Turns != 5 || stats.Events != 3 {
		t.Errorf("stats = %+v, want 1 session / 5 turns / 3 events", stats)
	}

	var status, title, model string
	if err := db.QueryRow(`SELECT status, title, model FROM sessions`).Scan(&status, &title, &model); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Errorf("status = %q, want completed", status)
	}
	if title != "Explain deploy script" {
		t.Errorf("title = %q, want ai-title value", title)
	}
	if model != "claude-fable-5" {
		t.Errorf("model = %q", model)
	}

	// C1: usage duplicated on both split lines of msg …0301 must be counted once.
	var tokensOut int
	if err := db.QueryRow(
		`SELECT tokens_out FROM turns WHERE message_id = 'msg_01AAAAAAAAAAAAAAAAAAAA0301'`,
	).Scan(&tokensOut); err != nil {
		t.Fatal(err)
	}
	if tokensOut != 150 {
		t.Errorf("tokens_out = %d, want 150 (deduplicated by message.id)", tokensOut)
	}
	// User turns carry no usage.
	if got := count(t, db, `SELECT COUNT(*) FROM turns WHERE role='user' AND tokens_out IS NOT NULL`); got != 0 {
		t.Errorf("user turns with tokens = %d, want 0", got)
	}
	// Turn seq is contiguous from 0 in file order.
	var minSeq, maxSeq int
	if err := db.QueryRow(`SELECT MIN(seq), MAX(seq) FROM turns`).Scan(&minSeq, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if minSeq != 0 || maxSeq != 4 {
		t.Errorf("seq range = [%d,%d], want [0,4]", minSeq, maxSeq)
	}
}

func TestIngestToolHeavySession(t *testing.T) {
	db := testDB(t)
	if _, err := File(db, filepath.Join(fixtures, "tool-heavy-session.jsonl")); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := count(t, db, `SELECT COUNT(*) FROM sessions`); got != 1 {
		t.Errorf("sessions = %d, want 1", got)
	}
	// Turns: user + 7 assistant API messages (0201…0207) → 8.
	if got := count(t, db, `SELECT COUNT(*) FROM turns`); got != 8 {
		t.Errorf("turns = %d, want 8", got)
	}
	// Events: user_prompt + 6 tool_calls (Read, Edit, Write, Bash, Edit, Bash) → 7.
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 7 {
		t.Errorf("events = %d, want 7", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='tool_call'`); got != 6 {
		t.Errorf("tool_call events = %d, want 6", got)
	}
	// The failing npm test Bash call carries is_error → status='error'.
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='tool_call' AND tool_name='Bash' AND status='error'`); got != 1 {
		t.Errorf("error Bash events = %d, want 1", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='tool_call' AND status='ok'`); got != 5 {
		t.Errorf("ok tool_calls = %d, want 5", got)
	}

	// file_changes: 2 Edits + 1 Write(create) → 3.
	if got := count(t, db, `SELECT COUNT(*) FROM file_changes`); got != 3 {
		t.Errorf("file_changes = %d, want 3", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM file_changes WHERE change_type='create'`); got != 1 {
		t.Errorf("create changes = %d, want 1", got)
	}
	var adds, dels int
	if err := db.QueryRow(
		`SELECT additions, deletions FROM file_changes WHERE change_type='edit' ORDER BY id LIMIT 1`,
	).Scan(&adds, &dels); err != nil {
		t.Fatal(err)
	}
	if adds != 1 || dels != 0 {
		t.Errorf("first edit +%d/-%d, want +1/-0 (from structuredPatch lines)", adds, dels)
	}
}

func TestIngestSubagentSession(t *testing.T) {
	db := testDB(t)
	stats, err := File(db, filepath.Join(fixtures, "subagent-session.jsonl"))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	if got := count(t, db, `SELECT COUNT(*) FROM sessions`); got != 1 {
		t.Errorf("sessions = %d, want 1", got)
	}
	// Turns (main file only; sidechain lines do not open turns):
	// user, asst 0001 (3 lines), asst 0002, asst 0003 (2 lines), asst 0004 (2 lines) → 5.
	if got := count(t, db, `SELECT COUNT(*) FROM turns`); got != 5 {
		t.Errorf("turns = %d, want 5", got)
	}
	// Events: main = user_prompt, tool_call(Bash), skill_use, subagent_start,
	// subagent_stop; sidechain = tool_call(Bash), tool_call(Read) → 7.
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 7 {
		t.Errorf("events = %d, want 7", got)
	}
	if stats.Events != 7 {
		t.Errorf("stats.Events = %d, want 7", stats.Events)
	}

	// Subagent chain: Agent tool_use → subagent_start, its tool_result → subagent_stop.
	var startID int
	if err := db.QueryRow(
		`SELECT id FROM events WHERE type='subagent_start' AND tool_name='Agent'`,
	).Scan(&startID); err != nil {
		t.Fatalf("subagent_start event: %v", err)
	}
	var stopParent sql.NullInt64
	var stopStatus sql.NullString
	var stopDuration sql.NullInt64
	if err := db.QueryRow(
		`SELECT parent_event_id, status, duration_ms FROM events WHERE type='subagent_stop'`,
	).Scan(&stopParent, &stopStatus, &stopDuration); err != nil {
		t.Fatalf("subagent_stop event: %v", err)
	}
	if !stopParent.Valid || int(stopParent.Int64) != startID {
		t.Errorf("subagent_stop.parent_event_id = %v, want %d", stopParent, startID)
	}
	if stopStatus.String != "ok" {
		t.Errorf("subagent_stop.status = %q, want ok", stopStatus.String)
	}
	if stopDuration.Int64 != 135715 {
		t.Errorf("subagent_stop.duration_ms = %d, want 135715 (totalDurationMs)", stopDuration.Int64)
	}

	// Sidechain tool calls attach to the subagent_start event.
	if got := count(t, db,
		`SELECT COUNT(*) FROM events WHERE type='tool_call' AND parent_event_id=?`, startID); got != 2 {
		t.Errorf("sidechain tool_calls parented to subagent_start = %d, want 2", got)
	}

	// Skill invocation → skill_use event.
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='skill_use'`); got != 1 {
		t.Errorf("skill_use events = %d, want 1", got)
	}
}

// TestIngestBackgroundAgentSession: run_in_background Agent calls get an
// immediate "async_launched" tool_result (no totalDurationMs) while the
// sidechain keeps running — duration must come from the sidechain span
// (subagent_start.ts → last sidechain record ts) and the launch must not be
// recorded as an error.
func TestIngestBackgroundAgentSession(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(fixtures, "background-agent-session.jsonl")
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Fixture span: Agent tool_use 12:00:05.000 → last sidechain record
	// 12:20:05.000 = exactly 20 minutes.
	const wantDuration = 20 * 60 * 1000

	var startID, startDuration int64
	var startStatus string
	if err := db.QueryRow(
		`SELECT id, status, duration_ms FROM events WHERE type='subagent_start'`,
	).Scan(&startID, &startStatus, &startDuration); err != nil {
		t.Fatalf("subagent_start: %v", err)
	}
	var stopStatus string
	var stopDuration int64
	if err := db.QueryRow(
		`SELECT status, duration_ms FROM events WHERE type='subagent_stop'`,
	).Scan(&stopStatus, &stopDuration); err != nil {
		t.Fatalf("subagent_stop: %v", err)
	}
	if startStatus != "ok" || stopStatus != "ok" {
		t.Errorf("statuses = start %q / stop %q, want ok/ok (async launch is not an error)",
			startStatus, stopStatus)
	}
	if startDuration != wantDuration || stopDuration != wantDuration {
		t.Errorf("duration_ms = start %d / stop %d, want %d (sidechain span, not the launch roundtrip)",
			startDuration, stopDuration, wantDuration)
	}
	// Sidechain tool call is parented to the start event — no orphans.
	if got := count(t, db,
		`SELECT COUNT(*) FROM events WHERE type='tool_call' AND parent_event_id=?`, startID); got != 1 {
		t.Errorf("sidechain tool_calls parented = %d, want 1", got)
	}
	if got := count(t, db,
		`SELECT COUNT(*) FROM events WHERE turn_id IS NULL AND parent_event_id IS NULL`); got != 0 {
		t.Errorf("orphan events = %d, want 0", got)
	}

	// Re-ingest converges to the same values (idempotent update).
	stats2, err := File(db, path)
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if stats2.Events != 0 {
		t.Errorf("re-ingest created %d events, want 0", stats2.Events)
	}
	if err := db.QueryRow(
		`SELECT status, duration_ms FROM events WHERE type='subagent_stop'`,
	).Scan(&stopStatus, &stopDuration); err != nil {
		t.Fatal(err)
	}
	if stopStatus != "ok" || stopDuration != wantDuration {
		t.Errorf("after re-ingest: stop = %q/%d, want ok/%d", stopStatus, stopDuration, wantDuration)
	}
}

func TestIngestIsIdempotent(t *testing.T) {
	db := testDB(t)
	path := filepath.Join(fixtures, "subagent-session.jsonl")
	if _, err := File(db, path); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	stats2, err := File(db, path)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if stats2.Events != 0 || stats2.Sessions != 0 || stats2.Turns != 0 || stats2.FileChanges != 0 {
		t.Errorf("second ingest created rows: %+v, want all zero", stats2)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 7 {
		t.Errorf("events after re-ingest = %d, want 7", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM turns`); got != 5 {
		t.Errorf("turns after re-ingest = %d, want 5", got)
	}
}

func TestIngestUnknownAndMalformedLines(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "weird-session.jsonl")
	content := `{"type": "user", "parentUuid": null, "isSidechain": false, "promptId": "p1", "promptSource": "typed", "message": {"role": "user", "content": "hello"}, "uuid": "11111111-0000-4000-8000-000000000001", "timestamp": "2026-07-10T12:00:00.000Z", "cwd": "/tmp/proj", "sessionId": "22222222-0000-4000-8000-000000000002", "version": "2.1.170", "gitBranch": "main"}
{"type": "flurble", "uuid": "11111111-0000-4000-8000-000000000003", "timestamp": "2026-07-10T12:00:01.000Z", "sessionId": "22222222-0000-4000-8000-000000000002", "cwd": "/tmp/proj", "payloadStuff": 42}
this is not json at all {{{
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, err := File(db, path)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// Malformed line → warned and skipped, not fatal.
	if stats.SkippedLines != 1 {
		t.Errorf("SkippedLines = %d, want 1", stats.SkippedLines)
	}
	// Unknown record type → events row with type='unknown' and raw JSON payload.
	var payload string
	if err := db.QueryRow(`SELECT payload FROM events WHERE type='unknown'`).Scan(&payload); err != nil {
		t.Fatalf("unknown event: %v", err)
	}
	if payload == "" || payload[0] != '{' {
		t.Errorf("unknown event payload should be raw JSON, got %q", payload)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 2 {
		t.Errorf("events = %d, want 2 (user_prompt + unknown)", got)
	}
}

func TestSessionStatusVocabulary(t *testing.T) {
	// MVP must only ever emit active | idle | completed (C5).
	db := testDB(t)
	for _, f := range []string{"simple-session.jsonl", "tool-heavy-session.jsonl", "subagent-session.jsonl"} {
		if _, err := File(db, filepath.Join(fixtures, f)); err != nil {
			t.Fatalf("ingest %s: %v", f, err)
		}
	}
	if got := count(t, db,
		`SELECT COUNT(*) FROM sessions WHERE status NOT IN ('active','idle','completed')`); got != 0 {
		t.Errorf("sessions with forbidden status = %d, want 0", got)
	}
}
