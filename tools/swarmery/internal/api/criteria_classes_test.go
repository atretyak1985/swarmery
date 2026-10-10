package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
)

// TestEpicPhaseDTO_LandOnlyRunIsReadyToLand: a run settled `done` with one
// [LAND] criterion open (what phaserun.settle stamps, migration 0105's counter
// set by the scan) reads as landing.state=ready, with the open counts exposed.
func TestEpicPhaseDTO_LandOnlyRunIsReadyToLand(t *testing.T) {
	f := newPhaseLandingFixture(t, "done")
	if _, err := f.db.Exec(`UPDATE epic_phases
		SET criteria_land_open = 1, criteria_manual_open = 2,
		    run_error = '1 [LAND] criteria left for landing'
		WHERE id = ?`, f.phaseID); err != nil {
		t.Fatal(err)
	}
	ph, _, _, err := (&Handler{DB: f.db}).epicPhases(f.taskID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ph) != 1 {
		t.Fatalf("phases = %d", len(ph))
	}
	if ph[0].Landing.State != landingReady {
		t.Errorf("landing.state = %q, want ready", ph[0].Landing.State)
	}
	if ph[0].LandOpen != 1 || ph[0].ManualOpen != 2 {
		t.Errorf("landOpen/manualOpen = %d/%d, want 1/2", ph[0].LandOpen, ph[0].ManualOpen)
	}
	b, _ := json.Marshal(ph[0])
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["landOpen"] != 1.0 || wire["manualOpen"] != 2.0 {
		t.Errorf("wire landOpen/manualOpen = %v/%v", wire["landOpen"], wire["manualOpen"])
	}
}

// TestWritePhaseStartRefusal_DocGates: the two doc gates answer 409 with their
// stable codes, the structured field and a hint.
func TestWritePhaseStartRefusal_DocGates(t *testing.T) {
	cases := []struct {
		err   error
		code  string
		field string
		want  any
	}{
		{&phaserun.ManualOnlyError{ManualOpen: 2}, codeManualOnly, "manualOpen", 2.0},
		{&phaserun.NotYetEarliestError{Earliest: "2026-11-03"}, codeNotYetEarliest, "earliest", "2026-11-03"},
		// Wrapped still maps: errors.As is what the arm uses.
		{fmt.Errorf("admission: %w", &phaserun.ManualOnlyError{ManualOpen: 1}), codeManualOnly, "manualOpen", 1.0},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		writePhaseStartRefusal(rec, c.err)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409", c.code, rec.Code)
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != c.code {
			t.Errorf("code = %v, want %s", body["code"], c.code)
		}
		if body[c.field] != c.want {
			t.Errorf("%s: %s = %v, want %v", c.code, c.field, body[c.field], c.want)
		}
		if h, _ := body["hint"].(string); h == "" {
			t.Errorf("%s: no hint", c.code)
		}
		if e, _ := body["error"].(string); e == "" {
			t.Errorf("%s: empty error message", c.code)
		}
	}
}
