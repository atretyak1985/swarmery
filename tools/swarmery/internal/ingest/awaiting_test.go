package ingest

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/procwatch"
)

// awaitFixture is one session shaped for the awaiting_reply detector: a user
// turn followed by a main-thread assistant turn, then the knobs the detector
// reads.
type awaitFixture struct {
	status     string        // starting sessions.status
	entrypoint string        // sessions.entrypoint
	procState  string        // "" = NULL (no liveness signal)
	quiet      time.Duration // now - ended_at
	stopReason string        // last assistant turn's stop_reason
	openTool   bool          // an unclosed tool_call on that turn
	pendingPR  bool          // a pending permission_request for the session
}

func seedAwaitSession(t *testing.T, db *sql.DB, uuid string, f awaitFixture, now time.Time) int64 {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO projects (path, slug, first_seen) VALUES ('/tmp/await-proj', '-tmp-await-proj', ?)
		 ON CONFLICT(path) DO NOTHING`, now.Add(-24*time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	var projID int64
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = '/tmp/await-proj'`).Scan(&projID); err != nil {
		t.Fatal(err)
	}
	var ps any
	if f.procState != "" {
		ps = f.procState
	}
	last := now.Add(-f.quiet).UTC()
	res, err := db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, status, started_at, ended_at, proc_state, entrypoint, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'jsonl')`,
		projID, uuid, f.status, last.Add(-time.Hour).Format(time.RFC3339),
		last.Format("2006-01-02T15:04:05.000Z"), ps, f.entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := res.LastInsertId()
	ts := last.Format(time.RFC3339)
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, started_at) VALUES (?, 0, 'user', ?)`,
		sid, ts); err != nil {
		t.Fatal(err)
	}
	res, err = db.Exec(
		`INSERT INTO turns (session_id, seq, role, message_id, started_at, stop_reason) VALUES (?, 1, 'assistant', ?, ?, ?)`,
		sid, "msg-"+uuid, ts, f.stopReason)
	if err != nil {
		t.Fatal(err)
	}
	turnID, _ := res.LastInsertId()
	if f.openTool {
		if _, err := db.Exec(
			`INSERT INTO events (session_id, turn_id, ts, type, tool_name) VALUES (?, ?, ?, 'tool_call', 'Bash')`,
			sid, turnID, ts); err != nil {
			t.Fatal(err)
		}
	}
	if f.pendingPR {
		if _, err := db.Exec(
			`INSERT INTO permission_requests (session_id, tool_name, request_json, status, requested_at)
			 VALUES (?, 'Bash', '{}', 'pending', ?)`, sid, ts); err != nil {
			t.Fatal(err)
		}
	}
	return sid
}

// TestAwaitingReplyDetector covers the ticker side of the detector: cases
// (a)–(g), (i), (j) from the phase plan, plus the pre-migration row (empty
// entrypoint) that is eligible through the liveness rule. Case (h) — clearing on
// the next batch — needs the real ingest path: TestAwaitingReplyClearsOnNextBatch.
func TestAwaitingReplyDetector(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	cli := awaitFixture{status: "idle", entrypoint: "cli", procState: procwatch.StateRunning,
		quiet: 4 * time.Minute, stopReason: "end_turn"}
	with := func(mod func(*awaitFixture)) awaitFixture { f := cli; mod(&f); return f }

	for _, tc := range []struct {
		name string
		f    awaitFixture
		want string
	}{
		{"a cli end_turn quiet 4m running → awaiting", cli, StatusAwaitingReply},
		{"b sdk-cli → idle", with(func(f *awaitFixture) { f.entrypoint = "sdk-cli" }), "idle"},
		{"c last turn tool_use → idle", with(func(f *awaitFixture) { f.stopReason = "tool_use" }), "idle"},
		{"d open tool_call → idle", with(func(f *awaitFixture) { f.openTool = true }), "idle"},
		{"e pending permission_request → idle", with(func(f *awaitFixture) { f.pendingPR = true }), "idle"},
		{"f quiet 1m → active", with(func(f *awaitFixture) { f.quiet = time.Minute; f.status = "active" }), "active"},
		{"g proc dead → idle", with(func(f *awaitFixture) { f.procState = procwatch.StateDead }), "idle"},
		{"i awaiting, proc NULL, quiet 45m → completed", with(func(f *awaitFixture) {
			f.status, f.procState, f.quiet = StatusAwaitingReply, "", 45*time.Minute
		}), "completed"},
		{"j awaiting, proc running, quiet 3h → stays awaiting", with(func(f *awaitFixture) {
			f.status, f.quiet = StatusAwaitingReply, 3*time.Hour
		}), StatusAwaitingReply},
		{"pre-migration entrypoint '' + proc NULL, quiet 4m → idle", with(func(f *awaitFixture) {
			f.entrypoint, f.procState = "", ""
		}), "idle"},
		{"pre-migration entrypoint '' + proc alive, quiet 4m → awaiting", with(func(f *awaitFixture) {
			f.entrypoint, f.procState = "", procwatch.StateRunning
		}), StatusAwaitingReply},
		{"stop_sequence is not awaiting", with(func(f *awaitFixture) { f.stopReason = "stop_sequence" }), "idle"},
		{"awaiting, open tool_call appears → back to idle", with(func(f *awaitFixture) {
			f.status, f.openTool = StatusAwaitingReply, true
		}), "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			id := seedAwaitSession(t, db, "s-await", tc.f, now)
			changes, err := RecomputeStatuses(db, Thresholds{}, now)
			if err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
			// A transition is reported exactly when the status moved, so the
			// pipeline publishes session_updated for it.
			moved := tc.f.status != tc.want
			if reported := len(changes) == 1 && changes[0].ID == id && changes[0].Status == tc.want; reported != moved {
				t.Errorf("changes = %+v, want a reported transition: %v", changes, moved)
			}
		})
	}
}

// TestAwaitingReplyIgnoresSubagentTurns: the detector reads the newest
// MAIN-thread turn — a later subagent turn (agent_name set) does not hide an
// end_turn on the main thread.
func TestAwaitingReplyIgnoresSubagentTurns(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	db := testDB(t)
	id := seedAwaitSession(t, db, "s-sub", awaitFixture{status: "idle", entrypoint: "cli",
		procState: procwatch.StateRunning, quiet: 5 * time.Minute, stopReason: "end_turn"}, now)
	if _, err := db.Exec(
		`INSERT INTO turns (session_id, seq, role, message_id, started_at, stop_reason, agent_name)
		 VALUES (?, 2, 'assistant', 'msg-sub', ?, 'tool_use', 'researcher')`,
		id, now.Add(-5*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := RecomputeStatuses(db, Thresholds{}, now); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != StatusAwaitingReply {
		t.Errorf("status = %q, want %q", got, StatusAwaitingReply)
	}
}

// TestAwaitingReplyAwaitAfterKnob: AwaitAfter is honoured — a 4-minute quiet
// session is not awaiting under a 10-minute threshold.
func TestAwaitingReplyAwaitAfterKnob(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	db := testDB(t)
	id := seedAwaitSession(t, db, "s-knob", awaitFixture{status: "idle", entrypoint: "cli",
		procState: procwatch.StateRunning, quiet: 4 * time.Minute, stopReason: "end_turn"}, now)
	if _, err := RecomputeStatuses(db, Thresholds{AwaitAfter: 10 * time.Minute}, now); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE id = ?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "idle" {
		t.Errorf("status = %q, want idle under AwaitAfter=10m", got)
	}
	if d := DefaultThresholds().AwaitAfter; d != 3*time.Minute {
		t.Errorf("default AwaitAfter = %v, want 3m", d)
	}
}

/* ---------- through the real ingest path ---------- */

func epUserLine(sessionUUID, uuid, ts, entrypoint, text string) string {
	return fmt.Sprintf(`{"type":"user","parentUuid":null,"isSidechain":false,"promptSource":"typed","message":{"role":"user","content":%q},"uuid":%q,"timestamp":%q,"cwd":"/home/dev/await","sessionId":%q,"version":"2.1.170","gitBranch":"main","entrypoint":%q}`+"\n",
		text, uuid, ts, sessionUUID, entrypoint)
}

func epAssistantLine(sessionUUID, uuid, ts, entrypoint, msgID, stopReason, text string) string {
	return fmt.Sprintf(`{"type":"assistant","parentUuid":null,"isSidechain":false,"message":{"model":"claude-opus-5-5","id":%q,"role":"assistant","content":[{"type":"text","text":%q}],"stop_reason":%q,"usage":{"input_tokens":1,"output_tokens":1}},"uuid":%q,"timestamp":%q,"cwd":"/home/dev/await","sessionId":%q,"version":"2.1.170","gitBranch":"main","entrypoint":%q}`+"\n",
		msgID, text, stopReason, uuid, ts, sessionUUID, entrypoint)
}

func sessionField(t *testing.T, db *sql.DB, col, uuid string) string {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT `+col+` FROM sessions WHERE session_uuid = ?`, uuid).Scan(&v); err != nil {
		t.Fatalf("read %s of %s: %v", col, uuid, err)
	}
	return v
}

// TestAwaitingReplyClearsOnNextBatch is case (h): a session the ticker moved
// to awaiting_reply leaves it as soon as the next transcript batch (the
// operator's reply) is ingested — the ingest upsert does not protect it.
func TestAwaitingReplyClearsOnNextBatch(t *testing.T) {
	db := testDB(t)
	const sid = "cccccccc-0000-4000-8000-0000000000c1"
	path := filepath.Join(t.TempDir(), "-home-dev-await", sid+".jsonl")
	t0 := time.Now().UTC().Add(-10 * time.Minute)
	mustWrite(t, path,
		epUserLine(sid, "u-1", t0.Format(time.RFC3339), "cli", "refactor the parser")+
			epAssistantLine(sid, "a-1", t0.Add(time.Second).Format(time.RFC3339), "cli", "msg_await_1", "end_turn",
				"Done. Should I also update the tests?"))
	if _, err := TailFile(db, path, "", DefaultThresholds()); err != nil {
		t.Fatalf("tail: %v", err)
	}

	if _, err := RecomputeStatuses(db, Thresholds{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := sessionField(t, db, "status", sid); got != StatusAwaitingReply {
		t.Fatalf("after the ticker: status = %q, want %q", got, StatusAwaitingReply)
	}

	mustAppend(t, path, epUserLine(sid, "u-2", time.Now().UTC().Format(time.RFC3339), "cli", "yes, please"))
	if _, err := TailFile(db, path, "", DefaultThresholds()); err != nil {
		t.Fatalf("re-tail: %v", err)
	}
	if got := sessionField(t, db, "status", sid); got != "active" {
		t.Errorf("after the reply batch: status = %q, want active", got)
	}
	// And the ticker does not put it straight back: the newest main-thread
	// turn is now the user's reply.
	if _, err := RecomputeStatuses(db, Thresholds{}, time.Now().Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := sessionField(t, db, "status", sid); got == StatusAwaitingReply {
		t.Errorf("ticker re-entered awaiting_reply on a user turn")
	}
}

// TestAwaitingReplyEntrypointFirstWins: ingest stamps sessions.entrypoint from
// the first batch that carries one; a later batch with another value never
// rewrites it, and a row created without one is filled by the first that has it.
func TestAwaitingReplyEntrypointFirstWins(t *testing.T) {
	db := testDB(t)
	dir := t.TempDir()
	ts := func(m int) string {
		return time.Date(2026, 10, 6, 10, m, 0, 0, time.UTC).Format(time.RFC3339)
	}

	// INSERT path: sdk-cli stored; a later cli batch does not overwrite it.
	const headless = "dddddddd-0000-4000-8000-0000000000d1"
	hp := filepath.Join(dir, "-home-dev-await", headless+".jsonl")
	mustWrite(t, hp, epUserLine(headless, "h-1", ts(0), "sdk-cli", "run the plan"))
	if _, err := TailFile(db, hp, "", DefaultThresholds()); err != nil {
		t.Fatalf("tail: %v", err)
	}
	if got := sessionField(t, db, "entrypoint", headless); got != "sdk-cli" {
		t.Fatalf("entrypoint after first batch = %q, want sdk-cli", got)
	}
	mustAppend(t, hp, epUserLine(headless, "h-2", ts(1), "cli", "more"))
	if _, err := TailFile(db, hp, "", DefaultThresholds()); err != nil {
		t.Fatalf("re-tail: %v", err)
	}
	if got := sessionField(t, db, "entrypoint", headless); got != "sdk-cli" {
		t.Errorf("entrypoint after a later cli batch = %q, want sdk-cli (first wins)", got)
	}

	// UPDATE path: a row first ingested with no entrypoint is filled later.
	const legacy = "eeeeeeee-0000-4000-8000-0000000000e1"
	lp := filepath.Join(dir, "-home-dev-await", legacy+".jsonl")
	mustWrite(t, lp, epUserLine(legacy, "l-1", ts(0), "", "old transcript line"))
	if _, err := TailFile(db, lp, "", DefaultThresholds()); err != nil {
		t.Fatalf("tail legacy: %v", err)
	}
	if got := sessionField(t, db, "entrypoint", legacy); got != "" {
		t.Fatalf("entrypoint without an envelope value = %q, want ''", got)
	}
	mustAppend(t, lp, epUserLine(legacy, "l-2", ts(1), "cli", "new line"))
	if _, err := TailFile(db, lp, "", DefaultThresholds()); err != nil {
		t.Fatalf("re-tail legacy: %v", err)
	}
	if got := sessionField(t, db, "entrypoint", legacy); got != "cli" {
		t.Errorf("entrypoint after a cli batch on an unknown row = %q, want cli", got)
	}
}
