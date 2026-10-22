package api

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// blast_radius_missing rides the forecast-lint channel: a phase whose forecast
// lists files and whose doc has no `Blast radius:` line (epic_phases.blast_radius
// empty) shows the lint; the same phase with the line does not. Advisory — the
// gate reads the same as without it.
func TestListEpicsBlastRadiusLint(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)

	for _, seq := range []int{1, 2} {
		if _, err := db.Exec(`
			INSERT INTO phase_forecasts (phase_id, kind, areas_json, files_json, doc_hash)
			SELECT id, 'prior', '["internal/orders"]', '["internal/orders/totals.go"]', 'h'
			  FROM epic_phases WHERE workspace_task_id = ? AND seq = ?`, taskID, seq); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE epic_phases SET blast_radius = ?
		WHERE workspace_task_id = ? AND seq = 2`,
		"Blast radius: 1 caller in internal/api — internal/api/line_items.go:88 (sources: rg)", taskID); err != nil {
		t.Fatal(err)
	}

	var epics []epicDTO
	getJSON(t, srv.URL+"/api/epics", &epics)
	if len(epics) != 1 || len(epics[0].Phases) < 2 {
		t.Fatalf("epics = %+v, want one epic with two phases", epics)
	}
	p1, p2 := epics[0].Phases[0], epics[0].Phases[1]

	if len(p1.ForecastLints) != 1 || p1.ForecastLints[0].Code != wsingest.LintBlastRadiusMissing {
		t.Fatalf("phase 1 lints = %+v, want exactly one %s", p1.ForecastLints, wsingest.LintBlastRadiusMissing)
	}
	if len(p2.ForecastLints) != 0 {
		t.Errorf("phase 2 carries its line; lints = %+v, want none", p2.ForecastLints)
	}
	for _, b := range p1.CompletionBlockers {
		if strings.Contains(strings.ToLower(b), "blast") {
			t.Errorf("the blast-radius lint leaked into a completion blocker: %q", b)
		}
	}
}
