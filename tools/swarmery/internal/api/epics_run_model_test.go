package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The phase DTO's `runModel` — which model a finished run ACTUALLY used.
//
// Decision D3 of the phase-model-selection plan: no `epic_phases.model_used`
// column. The fact is owned by the session the run produced, reached through
// `epic_phases.run_session_uuid`, so the only way it can be wrong is if the join
// is wrong. That is exactly what these two tests pin — including the LEFT-ness,
// because an inner join here would silently DROP every phase that has never run
// from the Plans page, turning a missing label into a missing plan.

// A phase linked to a session exposes that session's model verbatim — the full
// pinned ID, not a short name: the UI shortens it for display, and the raw value
// is what makes `claude-opus-5[1m]` legible rather than rewritten.
func TestListEpics_PhaseRunModelFromLinkedSession(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	mustExecEpics(t, db, `INSERT INTO sessions (id, project_id, session_uuid, model, started_at, ended_at)
		VALUES (1, 1, 'u-phase-1', 'claude-fable-5-1', '2026-09-18T10:00:00Z', '2026-09-18T11:00:00Z')`)
	mustExecEpics(t, db, `UPDATE epic_phases SET run_state='done', run_session_uuid='u-phase-1',
		run_started_at='2026-09-18T10:00:00Z', run_ended_at='2026-09-18T11:00:00Z'
		WHERE workspace_task_id=? AND seq=1`, taskID)

	e := firstEpic(t, srv)
	if len(e.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(e.Phases))
	}
	if e.Phases[0].RunModel == nil {
		t.Fatal("phase 1 runModel = null, want the linked session's model")
	}
	if got := *e.Phases[0].RunModel; got != "claude-fable-5-1" {
		t.Errorf("phase 1 runModel = %q, want claude-fable-5-1", got)
	}
	// The un-run sibling must still be here (LEFT JOIN) and must say nothing.
	if e.Phases[1].Seq != 2 {
		t.Fatalf("phase[1].seq = %d, want the never-run phase 2", e.Phases[1].Seq)
	}
	if e.Phases[1].RunModel != nil {
		t.Errorf("never-run phase runModel = %q, want null", *e.Phases[1].RunModel)
	}
}

// The two ways the label has nothing honest to say, both of which must serialize
// as `null` rather than as an empty chip: a run whose session has no model
// recorded, and a run whose session has not been ingested at all.
func TestListEpics_PhaseRunModelNullWhenUnknown(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	// Phase 1: linked to a real session that carries no model.
	mustExecEpics(t, db, `INSERT INTO sessions (id, project_id, session_uuid, model, started_at)
		VALUES (1, 1, 'u-no-model', NULL, '2026-09-18T10:00:00Z')`)
	mustExecEpics(t, db, `UPDATE epic_phases SET run_state='done', run_session_uuid='u-no-model'
		WHERE workspace_task_id=? AND seq=1`, taskID)
	// Phase 2: a run stamped before its session was ingested — nothing to join to.
	mustExecEpics(t, db, `UPDATE epic_phases SET run_state='running', run_session_uuid='u-not-ingested'
		WHERE workspace_task_id=? AND seq=2`, taskID)

	e := firstEpic(t, srv)
	if len(e.Phases) != 2 {
		t.Fatalf("phases = %d, want 2 (a LEFT join must not drop either row)", len(e.Phases))
	}
	for _, p := range e.Phases {
		if p.RunModel != nil {
			t.Errorf("phase %d runModel = %q, want null", p.Seq, *p.RunModel)
		}
	}

	// And the key must be PRESENT in the wire body — the UI reads `runModel` and
	// an absent key would type-check while rendering nothing forever.
	resp, err := http.Get(srv.URL + "/api/epics?projectId=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw []struct {
		Phases []map[string]json.RawMessage `json:"phases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || len(raw[0].Phases) != 2 {
		t.Fatalf("raw epics = %+v", raw)
	}
	got, ok := raw[0].Phases[0]["runModel"]
	if !ok {
		t.Fatal("runModel missing from the phase DTO")
	}
	if string(got) != "null" {
		t.Errorf("runModel = %s, want null", got)
	}
}
