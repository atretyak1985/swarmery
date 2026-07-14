package ingest

import (
	"database/sql"
	"fmt"
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

// New projects get a clean display name derived from the cwd path base
// ("/Users/user/work/example-app" → "example-app"), never the slug.
func TestProjectNameDerivedFromPath(t *testing.T) {
	db := testDB(t)
	if _, err := File(db, filepath.Join(fixtures, "simple-session.jsonl")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM projects`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "example-app" {
		t.Errorf("project name = %q, want %q (base of cwd path)", name, "example-app")
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
	// Events: user_prompt + 6 tool_calls (Read, Edit, Write, Bash, Edit, Bash)
	// + 2 test_run (both `npm test` Bash calls emit one) → 9.
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != 9 {
		t.Errorf("events = %d, want 9", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='tool_call'`); got != 6 {
		t.Errorf("tool_call events = %d, want 6", got)
	}
	// The two `npm test` Bash calls each emit a test_run event (the Quality
	// aggregate source): one failing (status='error'), one passing.
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='test_run'`); got != 2 {
		t.Errorf("test_run events = %d, want 2", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='test_run' AND status='error'`); got != 1 {
		t.Errorf("errored test_run = %d, want 1", got)
	}
	// The passing run's "N passed"-style summary parses into counts.
	if got := count(t, db, `SELECT COUNT(*) FROM events WHERE type='test_run'
		AND json_extract(payload,'$.parsed')=1 AND json_extract(payload,'$.passed')=4`); got != 1 {
		t.Errorf("parsed test_run with 4 passed = %d, want 1", got)
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
	// Turns: main = user + asst 0001 (3 lines) + asst 0002 + asst 0003 (2 lines)
	// + asst 0004 (2 lines) → 5; sidechain assistant turns 0101–0104 are now
	// recorded too (phase 2), tagged with the subagent → 9 total.
	if got := count(t, db, `SELECT COUNT(*) FROM turns`); got != 9 {
		t.Errorf("turns = %d, want 9", got)
	}
	// Phase 2 attribution: the 4 sidechain turns carry agent_name from the
	// parent subagent_start's subagent_type ("Explore"); the 5 main turns are
	// left NULL (orchestrator).
	if got := count(t, db, `SELECT COUNT(*) FROM turns WHERE agent_name = 'Explore'`); got != 4 {
		t.Errorf("Explore-tagged turns = %d, want 4", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM turns WHERE agent_name IS NULL`); got != 5 {
		t.Errorf("untagged (main) turns = %d, want 5", got)
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
	if got := count(t, db, `SELECT COUNT(*) FROM turns`); got != 9 {
		t.Errorf("turns after re-ingest = %d, want 9", got)
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

// ── turns.text (Chat tab, migration 0005) ────────────────────────────────────

func textOf(t *testing.T, db *sql.DB, where string, args ...any) sql.NullString {
	t.Helper()
	var s sql.NullString
	if err := db.QueryRow(`SELECT text FROM turns WHERE `+where, args...).Scan(&s); err != nil {
		t.Fatalf("turn text (%s): %v", where, err)
	}
	return s
}

func TestTurnTextFromFixtures(t *testing.T) {
	db := testDB(t)
	if _, err := File(db, filepath.Join(fixtures, "simple-session.jsonl")); err != nil {
		t.Fatalf("ingest simple: %v", err)
	}

	// User turns carry the full prompt text.
	if got := textOf(t, db, `role='user' AND seq=0`); got.String != "What does scripts/deploy.sh do?" {
		t.Errorf("user turn text = %q", got.String)
	}
	// Single-text assistant message.
	want := "It builds the app (`npm run build`) and rsyncs `dist/` to the staging host. No rollback step — deploys are one-way."
	if got := textOf(t, db, `message_id='msg_01AAAAAAAAAAAAAAAAAAAA0302'`); got.String != want {
		t.Errorf("assistant text = %q, want %q", got.String, want)
	}
	// thinking + tool_use only message → no prose → NULL.
	if got := textOf(t, db, `message_id='msg_01AAAAAAAAAAAAAAAAAAAA0301'`); got.Valid {
		t.Errorf("thinking/tool_use-only turn text = %q, want NULL", got.String)
	}
}

func TestTurnTextSplitAssistantMessage(t *testing.T) {
	// subagent-session msg 0001 is split across 3 lines (thinking / text /
	// tool_use): text must be exactly the text block — thinking and tool_use
	// excluded.
	db := testDB(t)
	path := filepath.Join(fixtures, "subagent-session.jsonl")
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if got := textOf(t, db, `message_id='msg_01AAAAAAAAAAAAAAAAAAAA0001'`); got.String != "I'll start by listing the agent definitions." {
		t.Errorf("split-message text = %q", got.String)
	}

	// Re-ingest must not duplicate or extend the text (full re-read → the
	// batch text equals the stored text).
	if _, err := File(db, path); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if got := textOf(t, db, `message_id='msg_01AAAAAAAAAAAAAAAAAAAA0001'`); got.String != "I'll start by listing the agent definitions." {
		t.Errorf("text after re-ingest = %q (duplicated?)", got.String)
	}
}

const turnTextEnvelope = `,"timestamp":"%s","sessionId":"33333333-0000-4000-8000-000000000003","cwd":"/tmp/textproj","version":"2.1.170","gitBranch":"main"}` + "\n"

func turnTextFixture() (head, tail string) {
	asst := func(uuid, ts, blocks string) string {
		return `{"type":"assistant","uuid":"` + uuid +
			`","message":{"model":"claude-fable-5","id":"msg_TEXT01","role":"assistant","content":[` + blocks +
			`],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}` +
			fmt.Sprintf(turnTextEnvelope, ts)
	}
	head = `{"type":"user","uuid":"tx-u1","promptSource":"typed","message":{"role":"user","content":"Summarize the module"}` +
		fmt.Sprintf(turnTextEnvelope, "2026-07-10T12:00:00.000Z") +
		asst("tx-a1", "2026-07-10T12:00:01.000Z", `{"type":"thinking","thinking":"planning"}`) +
		asst("tx-a2", "2026-07-10T12:00:02.000Z", `{"type":"text","text":"First paragraph."}`)
	tail = asst("tx-a3", "2026-07-10T12:00:03.000Z", `{"type":"tool_use","id":"toolu_TX1","name":"Bash","input":{"command":"ls"}}`) +
		asst("tx-a4", "2026-07-10T12:00:04.000Z", `{"type":"text","text":"Second paragraph."}`)
	return head, tail
}

func TestTurnTextConcatAcrossSplitLines(t *testing.T) {
	db := testDB(t)
	head, tail := turnTextFixture()
	path := filepath.Join(t.TempDir(), "text-session.jsonl")
	if err := os.WriteFile(path, []byte(head+tail), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	const want = "First paragraph.\n\nSecond paragraph."
	if got := textOf(t, db, `message_id='msg_TEXT01'`); got.String != want {
		t.Errorf("concatenated text = %q, want %q", got.String, want)
	}
	// Idempotent full re-read.
	if _, err := File(db, path); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if got := textOf(t, db, `message_id='msg_TEXT01'`); got.String != want {
		t.Errorf("text after re-ingest = %q, want %q", got.String, want)
	}
}

func TestTurnTextTailExtendsIncrementally(t *testing.T) {
	// Live tail: later lines of the same turn arrive in a later batch — the
	// upsert must EXTEND the stored text, and a subsequent full re-read must
	// converge to the same value.
	db := testDB(t)
	head, tail := turnTextFixture()
	path := filepath.Join(t.TempDir(), "text-session.jsonl")
	if err := os.WriteFile(path, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := TailFile(db, path, Thresholds{}); err != nil {
		t.Fatalf("tail 1: %v", err)
	}
	if got := textOf(t, db, `message_id='msg_TEXT01'`); got.String != "First paragraph." {
		t.Errorf("after batch 1: text = %q", got.String)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(tail); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := TailFile(db, path, Thresholds{}); err != nil {
		t.Fatalf("tail 2: %v", err)
	}
	const want = "First paragraph.\n\nSecond paragraph."
	if got := textOf(t, db, `message_id='msg_TEXT01'`); got.String != want {
		t.Errorf("after batch 2: text = %q, want %q", got.String, want)
	}
	// A later full re-read (File) over the tailed state converges, no dup.
	if _, err := File(db, path); err != nil {
		t.Fatalf("full re-read: %v", err)
	}
	if got := textOf(t, db, `message_id='msg_TEXT01'`); got.String != want {
		t.Errorf("after full re-read: text = %q, want %q", got.String, want)
	}
}

func TestRebuildTextBackfillsExistingTurns(t *testing.T) {
	// Pre-0005 rows have NULL text and their lines are already consumed, so a
	// plain re-ingest of new bytes can't fill them — `backfill --rebuild-text`
	// re-reads from byte 0. It must fill text without duplicating events.
	db := testDB(t)
	root := t.TempDir()
	projDir := filepath.Join(root, "-tmp-textproj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(fixtures, "simple-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, "session.jsonl")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := File(db, path); err != nil {
		t.Fatalf("initial ingest: %v", err)
	}
	eventsBefore := count(t, db, `SELECT COUNT(*) FROM events`)
	// Simulate pre-migration rows.
	if _, err := db.Exec(`UPDATE turns SET text = NULL`); err != nil {
		t.Fatal(err)
	}

	stats := RebuildText(t.Context(), db, root)
	if stats.Files != 1 || stats.Errors != 0 {
		t.Errorf("rebuild stats = %+v, want 1 file / 0 errors", stats)
	}
	if got := textOf(t, db, `role='user' AND seq=0`); got.String != "What does scripts/deploy.sh do?" {
		t.Errorf("rebuilt user text = %q", got.String)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM turns WHERE role='assistant' AND text IS NOT NULL`); got != 2 {
		t.Errorf("assistant turns with text = %d, want 2", got)
	}
	if got := count(t, db, `SELECT COUNT(*) FROM events`); got != eventsBefore {
		t.Errorf("events after rebuild = %d, want %d (dedup must absorb the replay)", got, eventsBefore)
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
