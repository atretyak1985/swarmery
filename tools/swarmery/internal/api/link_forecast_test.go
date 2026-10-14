package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// turnEventsBody renders {"events":[…]} for one session's turn halves. Each
// spec is (kind, at, payload).
func turnEventsBody(uuid, cwd string, specs ...[3]string) string {
	evs := make([]string, len(specs))
	for i, s := range specs {
		evs[i] = fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"kind":%q,"at":%q,"payload":%s}`, uuid, cwd, s[0], s[1], s[2])
	}
	return `{"events":[` + strings.Join(evs, ",") + `]}`
}

func ago(d time.Duration) string { return time.Now().UTC().Add(-d).Format("2006-01-02T15:04:05.000Z") }

const (
	apiForecast = `{"turnSeq":%d,"files":["a.go","b.go"],"testsPass":true,"steps":4,"confidence":%s,"model":"haiku","forecastTokens":%d}`
	apiActual   = `{"turnSeq":%d,"files":["a.go","c.go","d.go","e.go"],"testsRan":true,"testsPass":false,"steps":%d}`
)

func forecastSpec(seq int, at, conf string, tokens int) [3]string {
	return [3]string{"turn.forecast", at, fmt.Sprintf(apiForecast, seq, conf, tokens)}
}

func actualSpec(seq int, at string, steps int) [3]string {
	return [3]string{"turn.actual", at, fmt.Sprintf(apiActual, seq, steps)}
}

// Acceptance (phase 10 #2): turn.forecast + turn.actual through POST
// /api/link/events → exactly one turn_forecasts row carrying surprise and
// vector; a lone half stays unscored; an invalid payload is a per-event reject
// inside a 202.
func TestLinkForecastEventsScoreOneRow(t *testing.T) {
	e := newLinkTestEnv(t)
	at := ago(time.Minute)
	code, body := e.do(t, http.MethodPost, "/api/link/events", e.token,
		turnEventsBody("s-tf", "/work/tf", forecastSpec(1, at, "0.8", 150), actualSpec(1, ago(30*time.Second), 8)))
	if code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", code, body)
	}
	if r := decodeLinkResult(t, body); r.Accepted != 2 || r.Rejected != 0 {
		t.Fatalf("result = %+v", r)
	}
	var (
		n        int
		surprise *float64
		vector   *string
	)
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("turn_forecasts rows = %d (%v), want 1", n, err)
	}
	if err := e.db.QueryRow(`SELECT surprise, vector FROM turn_forecasts`).Scan(&surprise, &vector); err != nil {
		t.Fatal(err)
	}
	if surprise == nil || *surprise != 0.73 || vector == nil || !strings.Contains(*vector, `"components"`) {
		t.Fatalf("surprise=%v vector=%v, want 0.73 and a vector", surprise, vector)
	}

	// A lone half (turn 2) stays without a score.
	code, body = e.do(t, http.MethodPost, "/api/link/events", e.token,
		turnEventsBody("s-tf", "/work/tf", forecastSpec(2, ago(10*time.Second), "0.5", 90)))
	if code != http.StatusAccepted || decodeLinkResult(t, body).Accepted != 1 {
		t.Fatalf("lone half: %d %s", code, body)
	}
	var s2, v2 *string
	if err := e.db.QueryRow(`SELECT CAST(surprise AS TEXT), vector FROM turn_forecasts WHERE turn_seq = 2`).Scan(&s2, &v2); err != nil {
		t.Fatal(err)
	}
	if s2 != nil || v2 != nil {
		t.Errorf("half row surprise=%v vector=%v, want NULL", s2, v2)
	}

	// Invalid payloads: rejected per event, 202 still, nothing stored.
	code, body = e.do(t, http.MethodPost, "/api/link/events", e.token,
		turnEventsBody("s-tf", "/work/tf",
			[3]string{"turn.forecast", ago(5 * time.Second), `{"turnSeq":"one"}`},
			[3]string{"turn.actual", ago(4 * time.Second), `{"turnSeq":3,"testsRan":true}`},
			[3]string{"turn.actual", ago(3 * time.Second), `[1,2]`}))
	if code != http.StatusAccepted {
		t.Fatalf("invalid payloads: status %d body %s, want 202", code, body)
	}
	r := decodeLinkResult(t, body)
	if r.Rejected != 3 || r.Accepted != 0 || len(r.Rejects) != 3 || r.Rejects[2].Index != 2 {
		t.Errorf("invalid payloads result = %+v", r)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM turn_forecasts`).Scan(&n); err != nil || n != 2 {
		t.Errorf("rows after rejects = %d (%v), want 2", n, err)
	}
}

func getTurnForecasts(t *testing.T, e *linkTestEnv, query string) (int, turnForecastsDTO, string) {
	t.Helper()
	code, body := e.do(t, http.MethodGet, "/api/insights/turn-forecasts"+query, "", "")
	var dto turnForecastsDTO
	if code == http.StatusOK {
		if err := json.Unmarshal([]byte(body), &dto); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
	return code, dto, body
}

func TestInsightsTurnForecastsEmpty(t *testing.T) {
	e := newLinkTestEnv(t)
	code, dto, body := getTurnForecasts(t, e, "")
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	t.Logf("empty: %s", body)
	if dto.Days != 14 || dto.N != 0 || !dto.TooFew || len(dto.Bins) != 5 || dto.Top == nil || len(dto.Top) != 0 || dto.AvgForecastTokens != nil {
		t.Errorf("empty dto = %+v", dto)
	}
	for _, want := range []string{`"top":[]`, `"avgForecastTokens":null`, `"meanConfidence":null`, `"hitRate":null`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %s missing %s", body, want)
		}
	}
}

// Acceptance (phase 10 #5, API half): bins + top-10, days clamp, projectId
// filter.
func TestInsightsTurnForecastsBinsTopAndFilters(t *testing.T) {
	e := newLinkTestEnv(t)

	// 12 scored turns in project A: steps 4 vs (4+i) → rising surprise; conf .1….
	var specs [][3]string
	for i := 1; i <= 12; i++ {
		at := ago(time.Duration(60-i) * time.Minute)
		conf := fmt.Sprintf("%.2f", float64(i)/13)
		specs = append(specs, forecastSpec(i, at, conf, 100), actualSpec(i, at, 4+i))
	}
	if code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, turnEventsBody("s-a", "/work/a", specs...)); code != http.StatusAccepted {
		t.Fatalf("seed a: %d %s", code, body)
	}
	// Project B: one scored turn now, and one 30 days ago.
	if code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, turnEventsBody("s-b", "/work/b",
		forecastSpec(1, ago(time.Minute), "0.9", 300), actualSpec(1, ago(time.Minute), 4),
		forecastSpec(2, ago(30*24*time.Hour), "0.9", 300), actualSpec(2, ago(30*24*time.Hour), 4),
	)); code != http.StatusAccepted {
		t.Fatalf("seed b: %d %s", code, body)
	}

	code, dto, body := getTurnForecasts(t, e, "")
	if code != http.StatusOK {
		t.Fatalf("status %d %s", code, body)
	}
	t.Logf("seeded: %s", body)
	if dto.N != 13 || !dto.TooFew {
		t.Errorf("n=%d tooFew=%v, want 13 true (the 30-day-old turn is outside 14 days)", dto.N, dto.TooFew)
	}
	if len(dto.Top) != turnForecastTopN {
		t.Fatalf("top has %d rows, want %d", len(dto.Top), turnForecastTopN)
	}
	for i := 1; i < len(dto.Top); i++ {
		if dto.Top[i].Surprise > dto.Top[i-1].Surprise {
			t.Fatalf("top not ordered by surprise: %+v", dto.Top)
		}
	}
	if dto.Top[0].SessionUUID == nil || *dto.Top[0].SessionUUID != "s-a" || dto.Top[0].TurnSeq != 12 {
		t.Errorf("top[0] = %+v, want s-a turn 12", dto.Top[0])
	}
	sum := 0
	for _, b := range dto.Bins {
		sum += b.N
	}
	if sum != dto.N || dto.Bins[4].Hi != 1 || dto.Bins[0].Lo != 0 {
		t.Errorf("bins %+v do not cover n=%d over [0,1]", dto.Bins, dto.N)
	}
	// (12×100 + 300) / 13 in the window.
	if dto.AvgForecastTokens == nil || *dto.AvgForecastTokens != 115.3846 {
		t.Errorf("avgForecastTokens = %v, want 115.3846", dto.AvgForecastTokens)
	}

	// days clamp: 500 → 90 (includes the 30-day-old turn); 0 → 1; junk → 14.
	if _, d, _ := getTurnForecasts(t, e, "?days=500"); d.Days != 90 || d.N != 14 {
		t.Errorf("days=500 → days %d n %d, want 90 14", d.Days, d.N)
	}
	if _, d, _ := getTurnForecasts(t, e, "?days=0"); d.Days != 1 || d.N != 13 {
		t.Errorf("days=0 → days %d n %d, want 1 13", d.Days, d.N)
	}
	if _, d, _ := getTurnForecasts(t, e, "?days=abc"); d.Days != 14 {
		t.Errorf("days=abc → %d, want 14", d.Days)
	}

	// projectId filter.
	var pidB int64
	if err := e.db.QueryRow(`SELECT project_id FROM sessions WHERE session_uuid = 's-b'`).Scan(&pidB); err != nil {
		t.Fatal(err)
	}
	_, d, _ := getTurnForecasts(t, e, fmt.Sprintf("?projectId=%d&days=90", pidB))
	if d.N != 2 || len(d.Top) != 2 || *d.Top[0].SessionUUID != "s-b" || *d.AvgForecastTokens != 300 {
		t.Errorf("project B = n %d top %d avg %v", d.N, len(d.Top), d.AvgForecastTokens)
	}
	if code, _, body := getTurnForecasts(t, e, "?projectId=x"); code != http.StatusBadRequest {
		t.Errorf("projectId=x → %d %s, want 400", code, body)
	}
}

// The turn_surprise frame hydrates from the stored turn; a gone row is skipped.
func TestWSTurnSurpriseFrame(t *testing.T) {
	e := newLinkTestEnv(t)
	at := ago(time.Minute)
	if code, body := e.do(t, http.MethodPost, "/api/link/events", e.token,
		turnEventsBody("s-ws", "/work/ws", forecastSpec(1, at, "0.8", 150), actualSpec(1, at, 8))); code != http.StatusAccepted {
		t.Fatalf("seed: %d %s", code, body)
	}
	var id, sid int64
	if err := e.db.QueryRow(`SELECT id, session_id FROM turn_forecasts`).Scan(&id, &sid); err != nil {
		t.Fatal(err)
	}
	h := &Handler{DB: e.db}
	frame, err := h.buildWSMessage(ingest.Notification{Type: ingest.NoteTurnSurprise, SessionID: sid, TurnForecastID: id})
	if err != nil || frame == nil {
		t.Fatalf("buildWSMessage = %s, %v", frame, err)
	}
	for _, want := range []string{`"type":"turn_surprise"`, `"sessionUuid":"s-ws"`, `"turnSeq":1`, `"surprise":0.73`, `"top":"tests_miss"`, `"model":"haiku"`} {
		if !strings.Contains(string(frame), want) {
			t.Errorf("frame %s is missing %s", frame, want)
		}
	}
	if f, err := h.buildWSMessage(ingest.Notification{Type: ingest.NoteTurnSurprise, TurnForecastID: 99999}); err != nil || f != nil {
		t.Errorf("a vanished turn built a frame %s (%v); want skip", f, err)
	}
}
