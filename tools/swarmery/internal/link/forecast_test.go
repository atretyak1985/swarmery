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
	tfForecast = `{"runId":"1760436000000-a1b2","turnSeq":1,"files":["a.go","b.go"],"testsPass":true,"steps":4,"confidence":0.8,"model":"haiku","forecastTokens":180}`
	tfActual   = `{"runId":"1760436000000-a1b2","turnSeq":1,"files":["a.go","c.go","d.go","e.go"],"testsRan":true,"testsPass":false,"steps":8}`
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
		turnEvent(KindTurnForecast, "2026-10-14T10:00:00Z", `{"runId":"r1","turnSeq":0}`),
		turnEvent(KindTurnActual, "2026-10-14T10:00:01Z", `{"runId":"r1","turnSeq":1,"steps":2}`),
		{SessionID: tfUUID, Cwd: "/work/tf", Kind: KindTurnForecast, At: "2026-10-14T10:00:02Z"},
		turnEvent(KindTurnForecast, "2026-10-14T10:00:03Z", `{"runId":"r1","turnSeq":1,"confidence":7}`),
		turnEvent(KindTurnStart, "2026-10-14T10:00:04Z", ``),
		turnEvent(KindTurnForecast, "2026-10-14T10:00:05Z", `{"turnSeq":1}`),
		turnEvent(KindTurnActual, "2026-10-14T10:00:06Z", `{"runId":"no spaces allowed","turnSeq":1,"testsRan":false,"steps":0}`),
	})
	if res.Accepted != 1 || res.Rejected != 6 {
		t.Fatalf("result = %+v, want 1 accepted, 6 rejected", res)
	}
	for _, rj := range res.Rejects {
		if !strings.HasPrefix(rj.Reason, "turn.forecast payload: ") && !strings.HasPrefix(rj.Reason, "turn.actual payload: ") {
			t.Errorf("reject %d reason %q lacks the kind prefix", rj.Index, rj.Reason)
		}
	}
	if !strings.Contains(res.Rejects[1].Reason, "testsRan is required") {
		t.Errorf("reject[1] = %q", res.Rejects[1].Reason)
	}
	if !strings.Contains(res.Rejects[4].Reason, "runId is required") || !strings.Contains(res.Rejects[5].Reason, "runId must be") {
		t.Errorf("runId rejects = %q / %q", res.Rejects[4].Reason, res.Rejects[5].Reason)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 0 {
		t.Errorf("turn_forecasts rows = %d (%v), want 0", n, err)
	}
}

// A `--resume` keeps the session id and the mod counts from 1 again under a
// new runId: the resumed run's halves make a second row and leave the first
// run's halves and score untouched.
func TestIngestTurnResumedRunIsASecondRow(t *testing.T) {
	db := testDB(t)
	rec := &recorder{}
	mustIngest(t, db, rec.publish, []Event{
		turnEvent(KindTurnForecast, "2026-10-14T10:00:00Z", tfForecast),
		turnEvent(KindTurnActual, "2026-10-14T10:00:30Z", tfActual),
	})
	var before string
	if err := db.QueryRow(`SELECT forecast || '|' || actual || '|' || surprise || '|' || vector FROM turn_forecasts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	res := mustIngest(t, db, rec.publish, []Event{
		turnEvent(KindTurnForecast, "2026-10-14T11:00:00Z", `{"runId":"1760439600000-z9y8","turnSeq":1,"files":["q.go"],"steps":1}`),
		turnEvent(KindTurnActual, "2026-10-14T11:00:30Z", `{"runId":"1760439600000-z9y8","turnSeq":1,"files":["q.go"],"testsRan":false,"steps":1}`),
	})
	if res.Accepted != 2 || res.Rejected != 0 {
		t.Fatalf("resumed run result = %+v", res)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d (%v), want 2", n, err)
	}
	var after string
	if err := db.QueryRow(`SELECT forecast || '|' || actual || '|' || surprise || '|' || vector FROM turn_forecasts
		WHERE run_id = '1760436000000-a1b2'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the first run's row changed:\n%s\n%s", before, after)
	}
	var s2 sql.NullFloat64
	if err := db.QueryRow(`SELECT surprise FROM turn_forecasts WHERE run_id = '1760439600000-z9y8'`).Scan(&s2); err != nil || !s2.Valid || s2.Float64 != 0 {
		t.Errorf("resumed row surprise = %v (%v), want 0", s2, err)
	}
	if got := countNotes(rec.notes, ingest.NoteTurnSurprise); got != 2 {
		t.Errorf("turn_surprise notes = %d, want 2", got)
	}
}
