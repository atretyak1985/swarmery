package runcore

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func refusalTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "refusal.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedSession writes one session with one assistant turn carrying stopReason.
func seedSession(t *testing.T, db *sql.DB, uuid, stopReason string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO projects (id, path, slug, first_seen)
		VALUES (1, '/tmp/refusal', 'refusal', '2026-09-23T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, model, started_at)
		VALUES (1, ?, 'claude-opus-5-5', '2026-09-23T10:00:00Z')`, uuid)
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := res.LastInsertId()
	var stop any
	if stopReason != "" {
		stop = stopReason
	}
	if _, err := db.Exec(`INSERT INTO turns (session_id, seq, role, message_id, model, started_at, stop_reason)
		VALUES (?, 1, 'assistant', 'msg_1', 'claude-opus-5-5', '2026-09-23T10:00:01Z', ?)`,
		sid, stop); err != nil {
		t.Fatal(err)
	}
}

// The classifier's whole point: a refused run is BLOCKED, whatever the ticks say.
//
// Both halves used to be wrong in opposite directions. With criteria unticked,
// plain ClassifyEnd returns `continue` and the engine resumes the session
// straight back into the classifier — twice, at the run's pinned effort. With
// criteria ticked it returns `done`, stamping green over a session that was cut
// off mid-work.
func TestClassifyRunEndRefusalBeatsBothOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		stop, cat  string
		done, tot  int
		wantState  EndState
		wantDetail string
	}{
		{
			name: "refusal with work left is blocked, not continue",
			stop: "refusal", done: 1, tot: 5,
			wantState: EndBlocked, wantDetail: "safeguard refusal (no category reported)",
		},
		{
			name: "refusal with everything ticked is blocked, not done",
			stop: "refusal", done: 5, tot: 5,
			wantState: EndBlocked, wantDetail: "safeguard refusal (no category reported)",
		},
		{
			name: "the category is named when the record carried one",
			stop: "refusal", cat: "reasoning_extraction", done: 0, tot: 3,
			wantState: EndBlocked, wantDetail: "safeguard refusal (reasoning_extraction)",
		},
		{
			name: "the executor's own sentinel outranks the inferred one",
			text: "PHASE BLOCKED: the migration slot is taken",
			stop: "refusal", done: 0, tot: 3,
			wantState: EndBlocked, wantDetail: "the migration slot is taken",
		},
		{
			name: "end_turn is not a refusal — the ordinary paths survive",
			stop: "end_turn", done: 5, tot: 5,
			wantState: EndDone,
		},
		{
			name: "no stop reason recorded (pre-0078 row) changes nothing",
			stop: "", done: 1, tot: 5,
			wantState: EndContinue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, detail := ClassifyRunEnd(tc.text, tc.stop, tc.cat, tc.done, tc.tot)
			if state != tc.wantState {
				t.Errorf("state = %q, want %q", state, tc.wantState)
			}
			if detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tc.wantDetail)
			}
		})
	}
}

// `refusal` must be matched as a value, not as a substring of the transcript,
// and must not be case-sensitive to a harness that spells it differently.
func TestRefusalDetailMatching(t *testing.T) {
	if _, ok := RefusalDetail("Refusal", ""); !ok {
		t.Error("RefusalDetail should accept a differently-cased spelling")
	}
	for _, s := range []string{"", "end_turn", "tool_use", "max_tokens", "refusal_pending"} {
		if _, ok := RefusalDetail(s, ""); ok {
			t.Errorf("RefusalDetail(%q) reported a refusal", s)
		}
	}
}

// LastStopReason reads the NEWEST assistant turn and must not require prose:
// a refused turn very often has no text block at all, which is exactly the
// restriction LastAssistantText carries and this query must not.
func TestLastStopReasonReadsTextlessTurns(t *testing.T) {
	db := refusalTestDB(t)
	seedSession(t, db, "uuid-refused", "refusal")
	if got := LastStopReason(db, "uuid-refused"); got != "refusal" {
		t.Errorf("LastStopReason = %q, want refusal", got)
	}
	seedSession(t, db, "uuid-legacy", "")
	if got := LastStopReason(db, "uuid-legacy"); got != "" {
		t.Errorf("LastStopReason on a pre-0078 row = %q, want \"\"", got)
	}
	if got := LastStopReason(db, "uuid-missing"); got != "" {
		t.Errorf("LastStopReason on an unknown session = %q, want \"\"", got)
	}
	if got := LastStopReason(nil, "uuid-refused"); got != "" {
		t.Errorf("LastStopReason(nil db) = %q, want \"\"", got)
	}
}

// The category is read opportunistically out of a payload whose shape is not
// catalogued. It must find one when it is there and stay quiet when it is not —
// never fail, and never make one up.
func TestRefusalCategoryFromPayload(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"nested under raw", `{"raw":{"subtype":"model_refusal_fallback","category":"bio"}}`, "bio"},
		{"flat", `{"category":"cyber"}`, "cyber"},
		{"alternate spelling", `{"raw":{"refusalCategory":"reasoning_extraction"}}`, "reasoning_extraction"},
		{"no category anywhere", `{"raw":{"subtype":"model_refusal_fallback"}}`, ""},
		{"not an object", `"just a string"`, ""},
		{"malformed", `{oh no`, ""},
		{"empty string value is not a category", `{"category":"   "}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categoryFromPayload([]byte(tc.payload)); got != tc.want {
				t.Errorf("categoryFromPayload = %q, want %q", got, tc.want)
			}
		})
	}
}

// BlockedOrRefused is the doc-unreadable branch's test, where there is no tick
// count at all. It must still catch both kinds of evidence.
func TestBlockedOrRefused(t *testing.T) {
	if _, ok := BlockedOrRefused("all good", "end_turn", ""); ok {
		t.Error("a clean ending must not be blocked")
	}
	reason, ok := BlockedOrRefused("", "refusal", "bio")
	if !ok || reason != "safeguard refusal (bio)" {
		t.Errorf("BlockedOrRefused = (%q, %v), want the refusal detail", reason, ok)
	}
	reason, ok = BlockedOrRefused("PLAN BLOCKED at phase 3: deps unmet", "refusal", "bio")
	if !ok || reason != "deps unmet" {
		t.Errorf("BlockedOrRefused = (%q, %v), want the executor's own reason", reason, ok)
	}
}
