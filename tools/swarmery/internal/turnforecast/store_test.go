package turnforecast

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "tf.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{DB: db}
}

// seedSession inserts a project + session and returns sessions.id.
func seedSession(t *testing.T, db *sql.DB, projectPath, uuid string) (sessionID, projectID int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT id FROM projects WHERE path = ?`, projectPath).Scan(&projectID); err != nil {
		r, err := db.Exec(`INSERT INTO projects (path, slug, name, first_seen, last_activity) VALUES (?, ?, ?, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
			projectPath, projectPath, projectPath)
		if err != nil {
			t.Fatalf("seed project: %v", err)
		}
		projectID, _ = r.LastInsertId()
	}
	r, err := db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source) VALUES (?, ?, 'active', '2026-10-01T00:00:00Z', 'mod')`,
		projectID, uuid)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	sessionID, _ = r.LastInsertId()
	return sessionID, projectID
}

type row struct {
	startedAt string
	forecast  sql.NullString
	actual    sql.NullString
	surprise  sql.NullFloat64
	vector    sql.NullString
	model     sql.NullString
	tokens    sql.NullInt64
}

func readTurn(t *testing.T, db *sql.DB, sid int64, seq int) row {
	t.Helper()
	var r row
	if err := db.QueryRow(`SELECT started_at, forecast, actual, surprise, vector, model, forecast_tokens
		FROM turn_forecasts WHERE session_id = ? AND turn_seq = ?`, sid, seq).
		Scan(&r.startedAt, &r.forecast, &r.actual, &r.surprise, &r.vector, &r.model, &r.tokens); err != nil {
		t.Fatalf("read turn %d/%d: %v", sid, seq, err)
	}
	return r
}

const (
	fcJSON = `{"turnSeq":1,"files":["a.go","b.go"],"testsPass":true,"steps":4,"confidence":0.8,"model":"haiku","forecastTokens":200}`
	acJSON = `{"turnSeq":1,"files":["a.go","c.go","d.go","e.go"],"testsRan":true,"testsPass":false,"steps":8}`
)

func mustUpsert(t *testing.T, s *Store, sid int64, seq int, half Half, at, payload string) Upserted {
	t.Helper()
	u, err := s.Upsert(context.Background(), sid, seq, half, at, []byte(payload))
	if err != nil {
		t.Fatalf("Upsert %s: %v", half, err)
	}
	return u
}

// A forecast alone is a half row: stored, unscored. The actual completes it and
// the row is scored in the same write; re-sending a half rescores.
func TestUpsertHalvesScoreAndRescore(t *testing.T) {
	s := testStore(t)
	sid, _ := seedSession(t, s.DB, "/w/p", "u-1")

	u := mustUpsert(t, s, sid, 1, HalfForecast, "2026-10-14T10:00:00.000Z", fcJSON)
	if u.Scored || u.ID == 0 {
		t.Fatalf("forecast alone: %+v, want unscored with an id", u)
	}
	r := readTurn(t, s.DB, sid, 1)
	if r.surprise.Valid || r.vector.Valid || r.actual.Valid || !r.forecast.Valid {
		t.Fatalf("half row = %+v, want forecast only, no score", r)
	}
	if r.model.String != "haiku" || r.tokens.Int64 != 200 || r.startedAt != "2026-10-14T10:00:00.000Z" {
		t.Errorf("model/tokens/started_at = %v/%v/%s", r.model, r.tokens, r.startedAt)
	}

	u2 := mustUpsert(t, s, sid, 1, HalfActual, "2026-10-14T10:01:00.000Z", acJSON)
	if !u2.Scored || u2.ID != u.ID || u2.Surprise == nil || *u2.Surprise != 0.73 || u2.Top != CompTestsMiss {
		t.Fatalf("both halves: %+v, want scored 0.73 top tests_miss on the same row", u2)
	}
	r = readTurn(t, s.DB, sid, 1)
	if !r.surprise.Valid || r.surprise.Float64 != 0.73 || !r.vector.Valid {
		t.Fatalf("scored row = %+v", r)
	}
	if r.startedAt != "2026-10-14T10:00:00.000Z" {
		t.Errorf("started_at moved to the actual's at: %s", r.startedAt)
	}
	vector1 := r.vector.String

	// Re-send the same actual (another at): identical vector — deterministic.
	mustUpsert(t, s, sid, 1, HalfActual, "2026-10-14T10:02:00.000Z", acJSON)
	if r := readTurn(t, s.DB, sid, 1); r.vector.String != vector1 {
		t.Errorf("rescore of identical halves changed the vector:\n%s\n%s", vector1, r.vector.String)
	}

	// Re-send a better actual: rescored.
	u3 := mustUpsert(t, s, sid, 1, HalfActual, "2026-10-14T10:03:00.000Z",
		`{"turnSeq":1,"files":["a.go","b.go"],"testsRan":true,"testsPass":true,"steps":4}`)
	if !u3.Scored || *u3.Surprise != 0 || u3.Top != "" {
		t.Errorf("perfect rescore = %+v, want 0 with no top", u3)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows = %d (%v), want 1", n, err)
	}
}

// Actual first: started_at is the actual's at until the forecast arrives, then
// the forecast's.
func TestUpsertActualFirst(t *testing.T) {
	s := testStore(t)
	sid, _ := seedSession(t, s.DB, "/w/p", "u-1")
	if u := mustUpsert(t, s, sid, 2, HalfActual, "2026-10-14T10:05:00.000Z", acJSON); u.Scored {
		t.Fatalf("actual alone scored: %+v", u)
	}
	if r := readTurn(t, s.DB, sid, 2); r.startedAt != "2026-10-14T10:05:00.000Z" || r.forecast.Valid || r.model.Valid {
		t.Fatalf("actual-only row = %+v", r)
	}
	if u := mustUpsert(t, s, sid, 2, HalfForecast, "2026-10-14T10:04:00.000Z", fcJSON); !u.Scored {
		t.Fatalf("forecast completing the row did not score: %+v", u)
	}
	if r := readTurn(t, s.DB, sid, 2); r.startedAt != "2026-10-14T10:04:00.000Z" {
		t.Errorf("started_at = %s, want the forecast's", r.startedAt)
	}
}

// Both halves present but nothing measurable → stays unscored (never zero).
func TestUpsertBothHalvesNothingMeasurable(t *testing.T) {
	s := testStore(t)
	sid, _ := seedSession(t, s.DB, "/w/p", "u-1")
	mustUpsert(t, s, sid, 1, HalfForecast, "2026-10-14T10:00:00.000Z", `{"turnSeq":1,"files":[],"confidence":null}`)
	u := mustUpsert(t, s, sid, 1, HalfActual, "2026-10-14T10:00:01.000Z", `{"turnSeq":1,"files":[],"testsRan":false,"steps":2}`)
	if u.Scored {
		t.Errorf("nothing measurable was scored: %+v", u)
	}
	if r := readTurn(t, s.DB, sid, 1); r.surprise.Valid {
		t.Errorf("surprise = %v, want NULL", r.surprise.Float64)
	}
}

func TestUpsertRejects(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Upsert(ctx, 1, 1, HalfForecast, "2026-10-14T10:00:00.000Z", []byte(`{"turnSeq":0}`)); err == nil {
		t.Error("invalid forecast accepted")
	}
	if _, err := s.Upsert(ctx, 1, 1, HalfActual, "2026-10-14T10:00:00.000Z", []byte(`{"turnSeq":1}`)); err == nil {
		t.Error("invalid actual accepted")
	}
	if _, err := s.Upsert(ctx, 1, 1, Half("bogus"), "2026-10-14T10:00:00.000Z", []byte(`{}`)); err == nil {
		t.Error("unknown half accepted")
	}
	if _, err := (&Store{}).Upsert(ctx, 1, 1, HalfForecast, "x", []byte(fcJSON)); err == nil {
		t.Error("Upsert without a DB succeeded")
	}
}

// scoreTurn writes a fully scored turn with the given confidence and actual
// steps against a 10-step forecast (steps miss = |10-steps|/10).
func scoreTurn(t *testing.T, s *Store, sid int64, seq int, at string, conf *float64, actualSteps int, tokens int64) {
	t.Helper()
	c := "null"
	if conf != nil {
		c = fmt.Sprint(*conf)
	}
	mustUpsert(t, s, sid, seq, HalfForecast, at,
		fmt.Sprintf(`{"turnSeq":%d,"steps":10,"confidence":%s,"model":"haiku","forecastTokens":%d}`, seq, c, tokens))
	mustUpsert(t, s, sid, seq, HalfActual, at,
		fmt.Sprintf(`{"turnSeq":%d,"testsRan":false,"steps":%d}`, seq, actualSteps))
}

func TestCalibrationBins(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sid, pid := seedSession(t, s.DB, "/w/p", "u-1")
	other, _ := seedSession(t, s.DB, "/w/q", "u-2")
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// Empty window.
	cal, err := s.Calibration(ctx, nil, since)
	if err != nil {
		t.Fatal(err)
	}
	if cal.N != 0 || !cal.TooFew || len(cal.Bins) != 5 || cal.AvgForecastTokens != nil {
		t.Fatalf("empty calibration = %+v", cal)
	}
	for _, b := range cal.Bins {
		if b.N != 0 || b.MeanConfidence != nil || b.HitRate != nil {
			t.Errorf("empty bin = %+v, want nulls", b)
		}
	}

	// Exact forecasts (surprise 0, hits) at conf .1, .6 (the boundary goes UP to
	// [.6,.8)) and 1.0 (the last bin is closed at 1).
	scoreTurn(t, s, sid, 1, "2026-10-10T00:00:00.000Z", fp(0.1), 10, 100)
	scoreTurn(t, s, sid, 2, "2026-10-10T00:01:00.000Z", fp(0.6), 10, 300)
	scoreTurn(t, s, sid, 3, "2026-10-10T00:02:00.000Z", fp(1.0), 10, 200)
	scoreTurn(t, s, sid, 4, "2026-10-10T00:03:00.000Z", nil, 10, 200) // no confidence: outside the bins
	// A real miss at conf .9: unexpected files .3 + steps 1*.15 + overconf .09 = .54.
	mustUpsert(t, s, sid, 5, HalfForecast, "2026-10-10T00:04:00.000Z", `{"turnSeq":5,"files":[],"steps":10,"confidence":0.9}`)
	mustUpsert(t, s, sid, 5, HalfActual, "2026-10-10T00:04:00.000Z", `{"turnSeq":5,"files":["x"],"testsRan":false,"steps":0}`)
	// Outside the window, and another project.
	scoreTurn(t, s, sid, 6, "2026-09-01T00:00:00.000Z", fp(0.3), 10, 999)
	scoreTurn(t, s, other, 1, "2026-10-10T00:00:00.000Z", fp(0.3), 10, 0)
	// A forecast-only half row: counts toward avg tokens, never toward the bins.
	mustUpsert(t, s, sid, 7, HalfForecast, "2026-10-10T00:05:00.000Z", `{"turnSeq":7,"confidence":0.5,"forecastTokens":400}`)

	cal, err = s.Calibration(ctx, nil, since)
	if err != nil {
		t.Fatal(err)
	}
	if cal.N != 5 || !cal.TooFew {
		t.Fatalf("all-project calibration n=%d tooFew=%v, want 5 true", cal.N, cal.TooFew)
	}
	byLo := map[float64]Bin{}
	for _, b := range cal.Bins {
		byLo[b.Lo] = b
	}
	if b := byLo[0]; b.N != 1 || *b.HitRate != 1 || *b.MeanConfidence != 0.1 {
		t.Errorf("bin 0 = %+v", b)
	}
	if b := byLo[0.2]; b.N != 1 || *b.MeanConfidence != 0.3 { // the other project's turn
		t.Errorf("bin .2 = %+v", b)
	}
	if b := byLo[0.6]; b.N != 1 || *b.MeanConfidence != 0.6 {
		t.Errorf("bin .6 = %+v, want the 0.6 turn (boundary goes up)", b)
	}
	if b := byLo[0.8]; b.N != 2 || *b.HitRate != 0.5 || *b.MeanConfidence != 0.95 || b.Hi != 1 {
		t.Errorf("bin .8 = %+v, want 2 turns (1.0 and .9), hit .5", b)
	}
	// tokens in window: 100 300 200 200 0(other) 400 = 1200 / 6 = 200 (turn 5 reported none).
	if cal.AvgForecastTokens == nil || *cal.AvgForecastTokens != 200 {
		t.Errorf("avg tokens = %v, want 200", val(cal.AvgForecastTokens))
	}

	// Project filter.
	cal, err = s.Calibration(ctx, &pid, since)
	if err != nil {
		t.Fatal(err)
	}
	if cal.N != 4 || byLoOf(cal)[0.2].N != 0 {
		t.Errorf("project calibration n=%d, want 4 with no .2 bin turn", cal.N)
	}

	// tooFew flips at MinCalibration.
	for i := range MinCalibration {
		scoreTurn(t, s, other, 100+i, "2026-10-11T00:00:00.000Z", fp(0.5), 10, 1)
	}
	if cal, _ = s.Calibration(ctx, nil, since); cal.TooFew {
		t.Errorf("n=%d still tooFew", cal.N)
	}
}

func byLoOf(c Calibration) map[float64]Bin {
	out := map[float64]Bin{}
	for _, b := range c.Bins {
		out[b.Lo] = b
	}
	return out
}

func TestTopSurprisesOrderAndFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	sid, pid := seedSession(t, s.DB, "/w/p", "u-1")
	other, _ := seedSession(t, s.DB, "/w/q", "u-2")
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// steps miss share s → surprise .15*s (no confidence).
	scoreTurn(t, s, sid, 1, "2026-10-10T00:00:00.000Z", nil, 5, 0)  // .5 → .075
	scoreTurn(t, s, sid, 2, "2026-10-10T00:01:00.000Z", nil, 0, 0)  // 1 → .15
	scoreTurn(t, s, sid, 3, "2026-10-10T00:02:00.000Z", nil, 0, 0)  // .15, newer → first on the tie
	scoreTurn(t, s, sid, 4, "2026-10-10T00:03:00.000Z", nil, 10, 0) // 0
	scoreTurn(t, s, other, 1, "2026-10-10T00:00:00.000Z", nil, 0, 0)
	scoreTurn(t, s, sid, 5, "2026-09-01T00:00:00.000Z", nil, 0, 0) // outside the window
	mustUpsert(t, s, sid, 6, HalfForecast, "2026-10-10T00:04:00.000Z", fcJSON)

	top, err := s.TopSurprises(ctx, &pid, since, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int
	for _, tt := range top {
		seqs = append(seqs, tt.TurnSeq)
	}
	if fmt.Sprint(seqs) != "[3 2 1 4]" {
		t.Fatalf("order = %v, want [3 2 1 4]", seqs)
	}
	if top[0].Surprise != 0.15 || top[0].Top != CompStepsMiss || top[0].Model != "haiku" ||
		top[0].SessionUUID == nil || *top[0].SessionUUID != "u-1" || top[0].ProjectID == nil || *top[0].ProjectID != pid {
		t.Errorf("top[0] = %+v", top[0])
	}
	if top[3].Top != "" {
		t.Errorf("a zero surprise has top %q", top[3].Top)
	}
	all, err := s.TopSurprises(ctx, nil, since, 2)
	if err != nil || len(all) != 2 {
		t.Fatalf("limit 2 → %d rows (%v)", len(all), err)
	}

	got, err := s.LoadTurn(ctx, top[0].ID)
	if err != nil || got == nil || got.TurnSeq != 3 {
		t.Errorf("LoadTurn = %+v, %v", got, err)
	}
	var halfID int64
	if err := s.DB.QueryRow(`SELECT id FROM turn_forecasts WHERE turn_seq = 6`).Scan(&halfID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadTurn(ctx, halfID); err != nil || got != nil {
		t.Errorf("LoadTurn(half row) = %+v, %v; want nil", got, err)
	}
	if got, err := s.LoadTurn(ctx, 99999); err != nil || got != nil {
		t.Errorf("LoadTurn(missing) = %+v, %v; want nil", got, err)
	}
}
