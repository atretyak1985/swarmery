package link

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const fixtures = "../../testdata/fixtures"

// simpleSessionUUID is the sessionId of fixtures/simple-session.jsonl.
const simpleSessionUUID = "0f1e2d3c-4b5a-4968-8776-655443322110"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

type recorder struct{ notes []ingest.Notification }

func (r *recorder) publish(n ingest.Notification) { r.notes = append(r.notes, n) }

func mustIngest(t *testing.T, db *sql.DB, pub func(ingest.Notification), batch []Event) Result {
	t.Helper()
	res, err := Ingest(context.Background(), db, pub, batch)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	return res
}

type linkRow struct {
	source            string
	projectPath       string
	status            string
	linkStatus        sql.NullString
	seenAt            sql.NullString
	contextPct, cost  sql.NullFloat64
	rateLimits        sql.NullString
	startedAt, cwdCol string
}

func readRow(t *testing.T, db *sql.DB, uuid string) linkRow {
	t.Helper()
	var r linkRow
	if err := db.QueryRow(
		`SELECT s.source, p.path, s.status, s.link_status, s.link_seen_at, s.link_context_pct,
		        s.link_cost_usd, s.link_rate_limits, s.started_at, COALESCE(s.cwd, '')
		   FROM sessions s JOIN projects p ON p.id = s.project_id WHERE s.session_uuid = ?`, uuid).
		Scan(&r.source, &r.projectPath, &r.status, &r.linkStatus, &r.seenAt, &r.contextPct,
			&r.cost, &r.rateLimits, &r.startedAt, &r.cwdCol); err != nil {
		t.Fatalf("read session %s: %v", uuid, err)
	}
	return r
}

func countEvents(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM link_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A new Claude session id mints a source='mod' row attributed by cwd; the JSONL
// ingest of the same session later promotes it to 'both' (acceptance #4).
func TestIngestMintsModSessionThenJSONLMakesBoth(t *testing.T) {
	db := testDB(t)
	rec := &recorder{}
	res := mustIngest(t, db, rec.publish, []Event{
		{SessionID: simpleSessionUUID, Cwd: "/tmp/demo", Kind: KindSessionStart, At: "2026-07-10T09:00:01Z"},
		{SessionID: simpleSessionUUID, Cwd: "/tmp/demo", Kind: KindTurnStart, At: "2026-07-10T09:00:02.5+00:00"},
	})
	if res.Accepted != 2 || res.Duplicates != 0 || res.Rejected != 0 {
		t.Fatalf("result = %+v", res)
	}
	r := readRow(t, db, simpleSessionUUID)
	if r.source != "mod" || r.projectPath != "/tmp/demo" || r.cwdCol != "/tmp/demo" {
		t.Errorf("minted row source=%q project=%q cwd=%q, want mod on /tmp/demo", r.source, r.projectPath, r.cwdCol)
	}
	if r.startedAt != "2026-07-10T09:00:01.000Z" {
		t.Errorf("started_at = %q, want the earliest event time", r.startedAt)
	}
	if r.linkStatus.String != StatusWorking || r.seenAt.String != "2026-07-10T09:00:02.500Z" {
		t.Errorf("link_status=%v seen_at=%v", r.linkStatus, r.seenAt)
	}
	if len(rec.notes) != 1 || rec.notes[0].Type != ingest.NoteSessionStarted {
		t.Errorf("notes = %+v, want one session_started", rec.notes)
	}

	if _, err := ingest.File(db, filepath.Join(fixtures, "simple-session.jsonl")); err != nil {
		t.Fatalf("jsonl ingest: %v", err)
	}
	if r := readRow(t, db, simpleSessionUUID); r.source != "both" {
		t.Errorf("source after JSONL ingest = %q, want both", r.source)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE session_uuid = ?`, simpleSessionUUID).Scan(&n); err != nil || n != 1 {
		t.Errorf("sessions rows for the uuid = %d (%v), want 1", n, err)
	}
}

// The other order: transcript first ('jsonl'), then the mod → 'both'.
func TestIngestPromotesJSONLRowToBoth(t *testing.T) {
	db := testDB(t)
	if _, err := ingest.File(db, filepath.Join(fixtures, "simple-session.jsonl")); err != nil {
		t.Fatal(err)
	}
	if r := readRow(t, db, simpleSessionUUID); r.source != "jsonl" {
		t.Fatalf("precondition: source = %q", r.source)
	}
	rec := &recorder{}
	mustIngest(t, db, rec.publish, []Event{
		{SessionID: simpleSessionUUID, Kind: KindTurnComplete, At: "2026-07-10T09:05:00Z"},
	})
	r := readRow(t, db, simpleSessionUUID)
	if r.source != "both" || r.linkStatus.String != StatusWaiting {
		t.Errorf("source=%q link_status=%v, want both/waiting", r.source, r.linkStatus)
	}
	if len(rec.notes) != 1 || rec.notes[0].Type != ingest.NoteSessionUpdated {
		t.Errorf("notes = %+v, want one session_updated", rec.notes)
	}
}

// Resending the same batch stores nothing new: every event is a Duplicate and
// no notification fires.
func TestIngestDedupOnResend(t *testing.T) {
	db := testDB(t)
	batch := []Event{
		{SessionID: "u-dup", Cwd: "/tmp/dup", Kind: KindTurnStart, At: "2026-10-13T20:00:00Z"},
		{SessionID: "u-dup", Cwd: "/tmp/dup", Kind: KindToolCall, At: "2026-10-13T20:00:01Z",
			Payload: json.RawMessage(`{"tool":"Bash","inputHash":"abc"}`)},
		{SessionID: "u-dup", Cwd: "/tmp/dup", Kind: KindTurnComplete, At: "2026-10-13T20:00:02Z"},
	}
	if res := mustIngest(t, db, nil, batch); res.Accepted != 3 {
		t.Fatalf("first send = %+v", res)
	}
	rec := &recorder{}
	res := mustIngest(t, db, rec.publish, batch)
	if res.Duplicates != len(batch) || res.Accepted != 0 {
		t.Fatalf("resend = %+v, want duplicates=%d", res, len(batch))
	}
	if countEvents(t, db) != len(batch) {
		t.Errorf("link_events rows = %d, want %d", countEvents(t, db), len(batch))
	}
	if len(rec.notes) != 0 {
		t.Errorf("a duplicate-only batch published %+v", rec.notes)
	}
	// The same instant in another offset is the same event.
	res = mustIngest(t, db, nil, []Event{
		{SessionID: "u-dup", Kind: KindTurnStart, At: "2026-10-13T22:00:00+02:00"},
	})
	if res.Duplicates != 1 {
		t.Errorf("same instant, other offset = %+v, want a duplicate", res)
	}
	var payload string
	if err := db.QueryRow(`SELECT payload FROM link_events WHERE kind = 'tool.call'`).Scan(&payload); err != nil ||
		payload != `{"tool":"Bash","inputHash":"abc"}` {
		t.Errorf("tool.call payload = %q (%v)", payload, err)
	}
}

// The session.measure fixture (mods-format §M2) fills the link_* columns.
func TestIngestSessionMeasureFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtures, "mods", "session-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		AfterTurn1 map[string]json.RawMessage `json:"afterTurn1"`
		AfterTurn2 map[string]json.RawMessage `json:"afterTurn2"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	turn1 := fx.AfterTurn1["session.measure.e"]
	turn2 := fx.AfterTurn2["session.measure.e"]
	if turn1 == nil || turn2 == nil {
		t.Fatal("fixture shape changed: afterTurnN.session.measure.e missing")
	}

	db := testDB(t)
	// Turn 2's measure arrives BEFORE turn 1's (no fixed order, §M2): the
	// newer one must win.
	res := mustIngest(t, db, nil, []Event{
		{SessionID: "u-measure", Cwd: "/tmp/m", Kind: KindSessionMeasure, At: "2026-10-13T20:02:00Z", Payload: turn2},
		{SessionID: "u-measure", Cwd: "/tmp/m", Kind: KindSessionMeasure, At: "2026-10-13T20:01:00Z", Payload: turn1},
	})
	if res.Accepted != 2 {
		t.Fatalf("result = %+v", res)
	}
	r := readRow(t, db, "u-measure")
	if !r.contextPct.Valid || r.contextPct.Float64 != 4 {
		t.Errorf("link_context_pct = %v, want 4 (turn 2)", r.contextPct)
	}
	if !r.cost.Valid || r.cost.Float64 != 0.00803651 {
		t.Errorf("link_cost_usd = %v, want 0.00803651 (turn 2)", r.cost)
	}
	var limits []struct {
		Kind        string  `json:"kind"`
		PercentUsed float64 `json:"percentUsed"`
		ResetsAt    string  `json:"resetsAt"`
	}
	if err := json.Unmarshal([]byte(r.rateLimits.String), &limits); err != nil || len(limits) != 2 ||
		limits[0].Kind != "five_hour" || limits[1].PercentUsed != 48 {
		t.Errorf("link_rate_limits = %q (%v)", r.rateLimits.String, err)
	}
	if r.seenAt.String != "2026-10-13T20:02:00.000Z" {
		t.Errorf("link_seen_at = %v, want the newest event", r.seenAt)
	}
	if r.linkStatus.Valid {
		t.Errorf("a measure must not set link_status, got %v", r.linkStatus)
	}

	// A fresh process's first measure has no context.percent and cost 0: the
	// absent unit keeps its value, the present one updates.
	mustIngest(t, db, nil, []Event{
		{SessionID: "u-measure", Kind: KindSessionMeasure, At: "2026-10-13T20:03:00Z",
			Payload: json.RawMessage(`{"context":{"window":1000000},"rateLimits":[],"cost":{"usd":0}}`)},
	})
	r = readRow(t, db, "u-measure")
	if r.contextPct.Float64 != 4 || !r.cost.Valid || r.cost.Float64 != 0 || r.rateLimits.String != "[]" {
		t.Errorf("after a bare measure: pct=%v cost=%v limits=%q", r.contextPct, r.cost, r.rateLimits.String)
	}
}

// link_status follows the newest status-bearing event, whatever the arrival
// order; session.end → done.
func TestIngestStatusTransitions(t *testing.T) {
	db := testDB(t)
	mustIngest(t, db, nil, []Event{
		{SessionID: "u-st", Kind: KindTurnStart, At: "2026-10-13T20:00:00Z"},
		{SessionID: "u-st", Kind: KindTurnComplete, At: "2026-10-13T20:00:05Z"},
	})
	if r := readRow(t, db, "u-st"); r.linkStatus.String != StatusWaiting {
		t.Fatalf("link_status = %v, want waiting", r.linkStatus)
	}
	// A late, older turn.start must not roll waiting back to working.
	mustIngest(t, db, nil, []Event{{SessionID: "u-st", Kind: KindTurnStart, At: "2026-10-13T19:59:00Z"}})
	if r := readRow(t, db, "u-st"); r.linkStatus.String != StatusWaiting {
		t.Errorf("late older turn.start set link_status = %v", r.linkStatus)
	}
	mustIngest(t, db, nil, []Event{{SessionID: "u-st", Kind: KindSessionEnd, At: "2026-10-13T20:10:00Z"}})
	r := readRow(t, db, "u-st")
	if r.linkStatus.String != StatusDone || r.seenAt.String != "2026-10-13T20:10:00.000Z" {
		t.Errorf("after session.end: status=%v seen=%v", r.linkStatus, r.seenAt)
	}
	// No cwd → the '(unknown)' placeholder project, as the hooks channel does.
	if r.projectPath != ingest.UnknownProjectPath {
		t.Errorf("cwd-less mint attributed to %q", r.projectPath)
	}
}

// Invalid events are rejected one by one; the valid rest of the batch lands.
func TestIngestRejectsInvalidEvents(t *testing.T) {
	db := testDB(t)
	rec := &recorder{}
	res := mustIngest(t, db, rec.publish, []Event{
		{SessionID: "u-rej", Kind: "turn.bogus", At: "2026-10-13T20:00:00Z"},
		{SessionID: "", Kind: KindTurnStart, At: "2026-10-13T20:00:00Z"},
		{SessionID: "u-rej", Kind: KindTurnStart, At: "yesterday"},
		{SessionID: "u-rej", Kind: KindToolCall, At: "2026-10-13T20:00:00Z", Payload: json.RawMessage(`{broken`)},
		{SessionID: "u-rej", Kind: KindSessionMeasure, At: "2026-10-13T20:00:00Z", Payload: json.RawMessage(`{"cost":0.5}`)},
		{SessionID: "u-rej", Kind: KindTurnStart, At: "2026-10-13T20:00:01Z", Payload: json.RawMessage(`null`)},
	})
	if res.Rejected != 5 || res.Accepted != 1 || len(res.Rejects) != 5 {
		t.Fatalf("result = %+v", res)
	}
	for i, rj := range res.Rejects {
		if rj.Index != i || rj.Reason == "" {
			t.Errorf("reject %d = %+v", i, rj)
		}
	}
	if len(rec.notes) != 1 {
		t.Errorf("notes = %+v, want one for the single touched session", rec.notes)
	}
	var payload sql.NullString
	if err := db.QueryRow(`SELECT payload FROM link_events`).Scan(&payload); err != nil || payload.Valid {
		t.Errorf("a null payload must store as NULL, got %v (%v)", payload, err)
	}

	// An all-invalid batch never opens a transaction or publishes.
	rec.notes = nil
	res = mustIngest(t, db, rec.publish, []Event{{SessionID: "u-none", Kind: "nope", At: "x"}})
	if res.Rejected != 1 || len(rec.notes) != 0 {
		t.Errorf("all-invalid batch = %+v, notes %+v", res, rec.notes)
	}
}

// A database failure fails the whole batch.
func TestIngestDatabaseError(t *testing.T) {
	db := testDB(t)
	db.Close()
	if _, err := Ingest(context.Background(), db, nil, []Event{
		{SessionID: "u-x", Kind: KindTurnStart, At: "2026-10-13T20:00:00Z"},
	}); err == nil {
		t.Fatal("want an error on a closed database")
	}
}
