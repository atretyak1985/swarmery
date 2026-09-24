package api

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// The phase DTO carries the doc's `## Forecast` blocks and the lints over them.
// The lint is computed in the READ path, like the spec rollup's unknownRefs, and
// like it refuses nothing: the malformed phase below still serves, still reports
// its checkboxes and still reads `incomplete` for the reason it always did.
func TestListEpicsPhaseForecasts(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	// Phase 1: a clean prior + posterior pair (what wsingest would have written).
	if _, err := db.Exec(`
		INSERT INTO phase_forecasts
			(phase_id, kind, written_at, areas_json, files_json, size_band,
			 duration_band, outcome, risks_json, confidence, post_hoc, doc_hash)
		SELECT id, 'prior', '2026-09-23T10:12:00Z', '["internal/ingest"]',
		       '["internal/ingest/record.go"]', 'M', '30-90m', 'done',
		       '["migration touches turns"]', 0.7, 0, 'abc123'
		  FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO phase_forecasts
			(phase_id, kind, written_at, areas_json, size_band, duration_band,
			 outcome, confidence, post_hoc, doc_hash)
		SELECT id, 'posterior', '2026-09-23T13:40:00Z', '["internal/ingest","internal/api"]',
		       'L', '90m-4h', 'partial', 0.4, 0, 'abc123'
		  FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`, taskID); err != nil {
		t.Fatal(err)
	}
	// Phase 2: a malformed one — unknown band, confidence out of range, no areas.
	if _, err := db.Exec(`
		INSERT INTO phase_forecasts
			(phase_id, kind, size_band, confidence, doc_hash)
		SELECT id, 'prior', 'ENORMOUS', 3.5, 'def456'
		  FROM epic_phases WHERE workspace_task_id = ? AND seq = 2`, taskID); err != nil {
		t.Fatal(err)
	}

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if len(epics) != 1 {
		t.Fatalf("epics = %d, want 1", len(epics))
	}
	p1, p2 := epics[0].Phases[0], epics[0].Phases[1]

	if len(p1.Forecasts) != 2 {
		t.Fatalf("phase 1 forecasts = %d, want 2", len(p1.Forecasts))
	}
	prior, post := p1.Forecasts[0], p1.Forecasts[1]
	if prior.Kind != "prior" || post.Kind != "posterior" {
		t.Errorf("kinds = %q, %q, want document order prior, posterior", prior.Kind, post.Kind)
	}
	if !reflect.DeepEqual(prior.Areas, []string{"internal/ingest"}) {
		t.Errorf("prior.areas = %v", prior.Areas)
	}
	if prior.Confidence == nil || *prior.Confidence != 0.7 {
		t.Errorf("prior.confidence = %v, want 0.7", prior.Confidence)
	}
	if prior.DocHash != "abc123" {
		t.Errorf("prior.docHash = %q, want the scan's stamp", prior.DocHash)
	}
	if len(p1.ForecastLints) != 0 {
		t.Errorf("phase 1 lints = %+v, want none", p1.ForecastLints)
	}

	// The malformed phase: lints, and NOTHING else changed about the row.
	codes := map[string]bool{}
	for _, l := range p2.ForecastLints {
		codes[l.Code] = true
	}
	for _, want := range []string{"unknown-size-band", "bad-confidence", "missing-areas"} {
		if !codes[want] {
			t.Errorf("phase 2: lint %s not raised; got %v", want, codes)
		}
	}
	if p2.Forecasts[0].SizeBand != "ENORMOUS" {
		t.Errorf("size band = %q, want the author's own word — a hidden typo cannot be fixed",
			p2.Forecasts[0].SizeBand)
	}
	// A forecast is DATA, never a fence: the lint must not touch the gate.
	if p2.CompletionState != "incomplete" {
		t.Errorf("completionState = %q, want the checkbox-derived verdict, untouched by a lint",
			p2.CompletionState)
	}
	for _, b := range p2.CompletionBlockers {
		if strings.Contains(strings.ToLower(b), "forecast") {
			t.Errorf("a forecast lint leaked into a completion blocker: %q", b)
		}
	}
}

// A plan whose phases declare no forecast is wire-unchanged in the shape that
// matters to the client: [] rather than null on both fields, so the UI can map
// over them without a guard.
func TestListEpicsNoForecasts(t *testing.T) {
	srv, _, _, _ := epicFixture(t)
	resp, err := http.Get(srv.URL + "/api/epics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	var phases []map[string]json.RawMessage
	if err := json.Unmarshal(raw[0]["phases"], &phases); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"forecasts", "forecastLints"} {
		if got := string(phases[0][field]); got != "[]" {
			t.Errorf("phases[0].%s = %s, want []", field, got)
		}
	}
}
