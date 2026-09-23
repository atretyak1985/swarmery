package api

import (
	"path/filepath"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

// Acceptance criterion 1 of phase 4, end to end: a real fixture transcript in
// which an Opus 5.5 safeguard moved the session onto Opus 4.1 is INGESTED, and
// the phase DTO the Plans page renders then lists BOTH models with their turn
// counts and says the run fell back.
//
// Ingesting rather than hand-inserting turns is the point: every link in the
// chain that used to drop this — the un-decoded stop_reason, the un-ingested
// system record, sessions.model standing in for "the model the run used" — is
// exercised by the same test.
func TestListEpics_PhaseRunModelsShowsTheFallback(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	fixture, err := filepath.Abs("../../testdata/fixtures/model-fallback-session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.File(db, fixture); err != nil {
		t.Fatalf("ingest fixture: %v", err)
	}
	mustExecEpics(t, db, `UPDATE epic_phases SET run_state='done',
		run_session_uuid='fb00fb00-0000-4000-8000-00000000fb01',
		run_started_at='2026-09-23T10:00:00Z', run_ended_at='2026-09-23T10:00:05Z'
		WHERE workspace_task_id=? AND seq=1`, taskID)

	e := firstEpic(t, srv)
	if len(e.Phases) != 2 {
		t.Fatalf("phases = %d, want 2", len(e.Phases))
	}
	p := e.Phases[0]

	// The first model is still the one sessions.model owns — unchanged, because
	// it is a true statement about the run's first turn.
	if p.RunModel == nil || *p.RunModel != "claude-opus-5-5" {
		t.Errorf("runModel = %v, want claude-opus-5-5", p.RunModel)
	}
	// …and it is no longer the ONLY thing said about the run.
	if len(p.RunModels) != 2 {
		t.Fatalf("runModels = %+v, want both models", p.RunModels)
	}
	if p.RunModels[0].Model != "claude-opus-5-5" || p.RunModels[0].Turns != 2 {
		t.Errorf("runModels[0] = %+v, want claude-opus-5-5 with 2 turns", p.RunModels[0])
	}
	if p.RunModels[1].Model != "claude-opus-4-1" || p.RunModels[1].Turns != 1 {
		t.Errorf("runModels[1] = %+v, want claude-opus-4-1 with 1 turn", p.RunModels[1])
	}
	if !p.RunModelFellBack {
		t.Error("runModelFellBack = false — the chip renders on this flag")
	}

	// The never-run sibling must carry [] and false, not null and not a chip.
	if p2 := e.Phases[1]; len(p2.RunModels) != 0 || p2.RunModelFellBack {
		t.Errorf("never-run phase = (%+v, %v), want ([], false)", p2.RunModels, p2.RunModelFellBack)
	}
}

// fellBack is the claim behind the chip, so its false positives matter more than
// its coverage: a context-window marker, a fast SKU and an UPGRADE must all read
// as "no fallback happened".
func TestFellBack(t *testing.T) {
	use := func(models ...string) []phaseModelUseDTO {
		out := make([]phaseModelUseDTO, 0, len(models))
		for _, m := range models {
			out = append(out, phaseModelUseDTO{Model: m, Turns: 1})
		}
		return out
	}
	cases := []struct {
		name string
		in   []phaseModelUseDTO
		want bool
	}{
		{"one model", use("claude-opus-5-5"), false},
		{"nothing ran", nil, false},
		{"context-window marker is the same model", use("claude-opus-5-5", "claude-opus-5-5[1m]"), false},
		{"fast SKU is the same model", use("claude-opus-5-5", "claude-opus-5-5-fast"), false},
		{"an upgrade is not a fallback", use("claude-sonnet-5", "claude-opus-5-5"), false},
		{"a newer generation is not a fallback", use("claude-opus-5", "claude-opus-5-5"), false},
		{"unknown family on either side stays silent", use("claude-opus-5-5", "gpt-4o"), false},
		{"older generation, same family", use("claude-opus-5-5", "claude-opus-4-1"), true},
		{"weaker family", use("claude-opus-5-5", "claude-sonnet-4-6"), true},
		{"the LAST model decides, not the middle one", use("claude-opus-5-5", "claude-haiku-4-5", "claude-opus-5-5"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fellBack(tc.in); got != tc.want {
				t.Errorf("fellBack(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
