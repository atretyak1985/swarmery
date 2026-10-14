package link

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

const (
	tfUUID     = "7d1c0f8e-9a2b-4c3d-8e4f-5a6b7c8d9e0f"
	tfForecast = `{"turnSeq":1,"files":["a.go","b.go"],"testsPass":true,"steps":4,"confidence":0.8,"model":"haiku","forecastTokens":180}`
	tfActual   = `{"turnSeq":1,"files":["a.go","c.go","d.go","e.go"],"testsRan":true,"testsPass":false,"steps":8}`
)

func turnEvent(kind, at, payload string) Event {
	return Event{SessionID: tfUUID, Cwd: "/work/tf", Kind: kind, At: at, Payload: json.RawMessage(payload)}
}

func turnRow(t *testing.T, db *sql.DB, seq int) (surprise sql.NullFloat64, vector sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT tf.surprise, tf.vector FROM turn_forecasts tf
		JOIN sessions s ON s.id = tf.session_id WHERE s.session_uuid = ? AND tf.turn_seq = ?`, tfUUID, seq).
		Scan(&surprise, &vector); err != nil {
		t.Fatalf("read turn %d: %v", seq, err)
	}
	return surprise, vector
}

func countNotes(notes []ingest.Notification, typ string) int {
	n := 0
	for _, x := range notes {
		if x.Type == typ {
			n++
		}
	}
	return n
}

// Forecast + actual in one batch → one scored row, one turn_surprise note; the
// turn kinds leave link_status alone.
func TestIngestTurnForecastAndActualScoreOneRow(t *testing.T) {
	db := testDB(t)
	rec := &recorder{}
	res := mustIngest(t, db, rec.publish, []Event{
		turnEvent(KindTurnForecast, "2026-10-14T10:00:00.000Z", tfForecast),
		turnEvent(KindTurnActual, "2026-10-14T10:00:30.000Z", tfActual),
	})
	if res.Accepted != 2 || res.Rejected != 0 {
		t.Fatalf("result = %+v", res)
	}
	s, v := turnRow(t, db, 1)
	if !s.Valid || s.Float64 != 0.73 || !v.Valid || !strings.Contains(v.String, `"top":"tests_miss"`) {
		t.Fatalf("surprise=%v vector=%v, want 0.73 with top tests_miss", s, v)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 1 {
		t.Errorf("turn_forecasts rows = %d (%v), want 1", n, err)
	}
	if r := readRow(t, db, tfUUID); r.linkStatus.Valid {
		t.Errorf("turn kinds set link_status = %q", r.linkStatus.String)
	}
	if got := countNotes(rec.notes, ingest.NoteTurnSurprise); got != 1 {
		t.Fatalf("turn_surprise notes = %d, want 1 (%+v)", got, rec.notes)
	}
	last := rec.notes[len(rec.notes)-1]
	if last.Type != ingest.NoteTurnSurprise || last.TurnForecastID == 0 || last.SessionID == 0 {
		t.Errorf("turn_surprise note = %+v, want ids set and published after the session note", last)
	}
}

// A lone half is stored unscored and publishes nothing; the other half in a
// later batch completes it. A resent identical event is a duplicate and does
// not rescore.
func TestIngestTurnHalvesAcrossBatches(t *testing.T) {
	db := testDB(t)
	rec := &recorder{}
	mustIngest(t, db, rec.publish, []Event{turnEvent(KindTurnForecast, "2026-10-14T10:00:00Z", tfForecast)})
	if s, v := turnRow(t, db, 1); s.Valid || v.Valid {
		t.Fatalf("half row scored: %v %v", s, v)
	}
	if countNotes(rec.notes, ingest.NoteTurnSurprise) != 0 {
		t.Fatal("a half row published turn_surprise")
	}

	mustIngest(t, db, rec.publish, []Event{turnEvent(KindTurnActual, "2026-10-14T10:01:00Z", tfActual)})
	if s, _ := turnRow(t, db, 1); !s.Valid {
		t.Fatal("second half did not score the row")
	}
	res := mustIngest(t, db, rec.publish, []Event{turnEvent(KindTurnActual, "2026-10-14T10:01:00Z", tfActual)})
	if res.Duplicates != 1 || countNotes(rec.notes, ingest.NoteTurnSurprise) != 1 {
		t.Errorf("resend: %+v, turn_surprise notes %d, want a duplicate and still 1 note",
			res, countNotes(rec.notes, ingest.NoteTurnSurprise))
	}
}

// Invalid turn payloads are rejected per event with a reason; the rest of the
// batch is stored.
func TestIngestTurnPayloadRejects(t *testing.T) {
	db := testDB(t)
	res := mustIngest(t, db, nil, []Event{
		turnEvent(KindTurnForecast, "2026-10-14T10:00:00Z", `{"turnSeq":0}`),
		turnEvent(KindTurnActual, "2026-10-14T10:00:01Z", `{"turnSeq":1,"steps":2}`),
		{SessionID: tfUUID, Cwd: "/work/tf", Kind: KindTurnForecast, At: "2026-10-14T10:00:02Z"},
		turnEvent(KindTurnForecast, "2026-10-14T10:00:03Z", `{"turnSeq":1,"confidence":7}`),
		turnEvent(KindTurnStart, "2026-10-14T10:00:04Z", ``),
	})
	if res.Accepted != 1 || res.Rejected != 4 {
		t.Fatalf("result = %+v, want 1 accepted, 4 rejected", res)
	}
	for _, rj := range res.Rejects {
		if !strings.HasPrefix(rj.Reason, "turn.forecast payload: ") && !strings.HasPrefix(rj.Reason, "turn.actual payload: ") {
			t.Errorf("reject %d reason %q lacks the kind prefix", rj.Index, rj.Reason)
		}
	}
	if !strings.Contains(res.Rejects[1].Reason, "testsRan is required") {
		t.Errorf("reject[1] = %q", res.Rejects[1].Reason)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 0 {
		t.Errorf("turn_forecasts rows = %d (%v), want 0", n, err)
	}
}
