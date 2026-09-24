package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// A phase serves the score of its CURRENT run, and null — never a zero score —
// when that run was not scored.
func TestListEpicsPhaseSurprise(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	mustDB := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustDB(`UPDATE epic_phases SET run_session_uuid = 'run-' || seq, run_state = 'done' WHERE workspace_task_id = ?`, taskID)
	// Phase 1: scored current run. Phase 2: only a PREVIOUS run was scored.
	mustDB(`INSERT INTO phase_surprise (phase_id, session_uuid, forecast_kind, surprise_index, top_component,
		components_json, detail_json, summary, computed_at)
		SELECT id, 'run-1', 'posterior', 0.72, 'outcome_miss', '{"outcome_miss":1,"size_miss":null}',
		       '{"forecastOutcome":"done","actualOutcome":"partial","unexpectedAreas":["web/src"]}',
		       'surprise 0.72 — top: outcome_miss', '2026-09-23T12:00:00Z'
		  FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`, taskID)
	mustDB(`INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, computed_at)
		SELECT id, 'old-run', 0.9, '2026-09-22T12:00:00Z' FROM epic_phases WHERE workspace_task_id = ? AND seq = 2`, taskID)

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	p1, p2 := epics[0].Phases[0], epics[0].Phases[1]
	if p1.Surprise == nil || p1.Surprise.Index != 0.72 || p1.Surprise.Top != "outcome_miss" {
		t.Fatalf("phase 1 surprise = %+v, want 0.72 led by outcome_miss", p1.Surprise)
	}
	if v := p1.Surprise.Components["size_miss"]; v != nil {
		t.Errorf("an unmeasured component serves %v, want null", *v)
	}
	if p1.Surprise.Detail.ActualOutcome != "partial" || len(p1.Surprise.Detail.UnexpectedAreas) != 1 {
		t.Errorf("detail = %+v", p1.Surprise.Detail)
	}
	if p2.Surprise != nil {
		t.Errorf("phase 2 serves a previous run's score %+v; want null", p2.Surprise)
	}

	// The raw JSON says null, not 0.
	resp, err := srv.Client().Get(srv.URL + "/api/epics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	phases := raw[0]["phases"].([]any)
	if s, ok := phases[1].(map[string]any)["surprise"]; !ok || s != nil {
		t.Errorf(`phase 2 "surprise" = %v (present %v), want null`, s, ok)
	}

	// The attention frame hydrates from the same row.
	h := &Handler{DB: db}
	var phaseID int64
	if err := db.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`, taskID).Scan(&phaseID); err != nil {
		t.Fatal(err)
	}
	frame, err := h.buildWSMessage(ingest.Notification{Type: ingest.NotePhaseSurprise, TaskID: taskID, PhaseID: phaseID})
	if err != nil || frame == nil {
		t.Fatalf("buildWSMessage = %s, %v", frame, err)
	}
	for _, want := range []string{`"type":"phase_surprise"`, `"phaseId":`, `"index":0.72`, `"top":"outcome_miss"`, `"projectId":1`} {
		if !strings.Contains(string(frame), want) {
			t.Errorf("frame %s is missing %s", frame, want)
		}
	}
	if f, err := h.buildWSMessage(ingest.Notification{Type: ingest.NotePhaseSurprise, PhaseID: 99999}); err != nil || f != nil {
		t.Errorf("a vanished phase built a frame %s (%v); want skip", f, err)
	}
}

// The retro report carries the window's top non-post-hoc surprises, and the
// digest cites each as [E:phase:<id>].
func TestRetroReportCarriesSurprises(t *testing.T) {
	srv, db := retroReportServer(t)
	today := retroDay(t, 0)
	for _, q := range []string{
		`INSERT INTO epic_phases (id, workspace_task_id, seq, name, doc_path, depends_on) VALUES
			(41, 1, 1, 'Scoring', '/ws/plan/phase-1.md', '[]'),
			(42, 1, 2, 'Backfilled', '/ws/plan/phase-2.md', '[]'),
			(43, 1, 3, 'Calm', '/ws/plan/phase-3.md', '[]')`,
		`INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, top_component, summary, forecast_post_hoc, computed_at) VALUES
			(41, 'r1', 0.7, 'outcome_miss', 'surprise 0.70 — top: outcome_miss', 0, ?),
			(41, 'r0', 0.2, 'size_miss', 'older run', 0, ?),
			(42, 'r2', 0.9, 'size_miss', 'post hoc', 1, ?),
			(43, 'r3', 0, '', 'as forecast', 0, ?)`,
	} {
		args := []any{}
		if strings.Contains(q, "?") {
			args = []any{today, today, today, today}
		}
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var out retroReportResponse
	getJSON(t, srv.URL+"/api/retro/report?"+retroRange(7), &out)
	got := out.Report.Surprises.Surprises
	if len(got) != 1 || got[0].PhaseID != 41 || got[0].Index != 0.7 || got[0].Plan != "Ship the loop" {
		t.Fatalf("surprises = %+v, want only phase 41 at its highest run (0.7)", got)
	}
	if !strings.Contains(out.Digest, "[E:phase:41]") || strings.Contains(out.Digest, "[E:phase:42]") {
		t.Errorf("digest citations wrong:\n%s", out.Digest)
	}
}
