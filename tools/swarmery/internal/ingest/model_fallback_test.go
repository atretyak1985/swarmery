package ingest

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The safeguard-fallback transcript, ingested end to end.
//
// Everything this phase makes visible is invisible without these two writes, and
// both were silent drops before it: `stop_reason` was decoded by nothing (so a
// refused turn was indistinguishable from a model that finished talking) and
// `system/model_refusal_fallback` fell into the "other subtypes: not ingested"
// branch (so the switch itself left no row at all).
func TestIngestRecordsRefusalAndModelFallback(t *testing.T) {
	db := testDB(t)
	path, err := filepath.Abs("../../testdata/fixtures/model-fallback-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := File(db, path); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	stopOf := func(msgID string) sql.NullString {
		t.Helper()
		var s sql.NullString
		if err := db.QueryRow(`SELECT stop_reason FROM turns WHERE message_id = ?`, msgID).Scan(&s); err != nil {
			t.Fatalf("read stop_reason of %s: %v", msgID, err)
		}
		return s
	}

	if got := stopOf("msg_FB01"); !got.Valid || got.String != "end_turn" {
		t.Errorf("msg_FB01 stop_reason = %v, want end_turn", got)
	}
	if got := stopOf("msg_FB02"); !got.Valid || got.String != "refusal" {
		t.Errorf("msg_FB02 stop_reason = %v, want refusal — this is the whole signal", got)
	}
	if got := stopOf("msg_FB03"); !got.Valid || got.String != "end_turn" {
		t.Errorf("msg_FB03 stop_reason = %v, want end_turn", got)
	}

	// The fallback record itself, as a typed event rather than 'unknown', so the
	// API can find it on idx_events_type.
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'model_fallback'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("model_fallback events = %d, want 1", events)
	}

	// Both models are on the turns, which is the substrate every "which model did
	// this actually run on" surface reads. sessions.model stays the FIRST one —
	// that is the fact it owns, and the bug was reading it as the only one.
	var sessModel sql.NullString
	if err := db.QueryRow(`SELECT model FROM sessions WHERE session_uuid = ?`,
		"fb00fb00-0000-4000-8000-00000000fb01").Scan(&sessModel); err != nil {
		t.Fatal(err)
	}
	if sessModel.String != "claude-opus-5-5" {
		t.Errorf("sessions.model = %q, want the FIRST model claude-opus-5-5", sessModel.String)
	}

	rows, err := db.Query(`
		SELECT model, COUNT(*) FROM turns
		 WHERE role = 'assistant' AND model IS NOT NULL
		 GROUP BY model ORDER BY model`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var m string
		var n int
		if err := rows.Scan(&m, &n); err != nil {
			t.Fatal(err)
		}
		got[m] = n
	}
	if got["claude-opus-5-5"] != 2 || got["claude-opus-4-1"] != 1 {
		t.Errorf("assistant turns per model = %v, want 2 on claude-opus-5-5 and 1 on claude-opus-4-1", got)
	}
}

// Re-ingesting the same file must not duplicate the fallback event. The dedup
// key is the record uuid, and a history table that doubles on every rescan would
// make "did this session fall back twice?" unanswerable.
func TestIngestModelFallbackIsIdempotent(t *testing.T) {
	db := testDB(t)
	path, err := filepath.Abs("../../testdata/fixtures/model-fallback-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := File(db, path); err != nil {
			t.Fatalf("ingest pass %d: %v", i+1, err)
		}
	}
	var events int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type = 'model_fallback'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Errorf("model_fallback events after two ingests = %d, want 1", events)
	}
}
