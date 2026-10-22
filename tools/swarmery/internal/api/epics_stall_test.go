package api

import (
	"encoding/json"
	"testing"
	"time"
)

// A phase serves its current stall from the detector's ledger, and null when it
// is not stalled.
func TestListEpicsPhaseStalled(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	since := time.Now().UTC().Add(-25 * time.Minute).Format(needsYouTSFormat)
	if _, err := db.Exec(`INSERT INTO stall_notices (phase_id, reason, stage, session_uuid, since, first_seen)
		SELECT id, 'no-turns', 'executor', 'run-1', ?, ? FROM epic_phases WHERE workspace_task_id = ? AND seq = 1`,
		since, since, taskID); err != nil {
		t.Fatal(err)
	}

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	p1, p2 := epics[0].Phases[0], epics[0].Phases[1]
	if p1.Stalled == nil {
		t.Fatal("phase 1 stalled = null, want the ledger's stall")
	}
	if p1.Stalled.Stage != "executor" || p1.Stalled.Reason != "no-turns" || p1.Stalled.SessionUUID != "run-1" {
		t.Errorf("phase 1 stalled = %+v", p1.Stalled)
	}
	if p1.Stalled.SinceMinutes != 25 || p1.Stalled.Since != since || p1.Stalled.RequestID != nil {
		t.Errorf("phase 1 since = %q / %d min / request %v, want %q / 25 / nil",
			p1.Stalled.Since, p1.Stalled.SinceMinutes, p1.Stalled.RequestID, since)
	}
	if p2.Stalled != nil {
		t.Errorf("phase 2 stalled = %+v, want null", p2.Stalled)
	}

	resp, err := srv.Client().Get(srv.URL + "/api/epics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if s, ok := raw[0]["phases"].([]any)[1].(map[string]any)["stalled"]; !ok || s != nil {
		t.Errorf(`phase 2 "stalled" = %v (present %v), want null`, s, ok)
	}
}
