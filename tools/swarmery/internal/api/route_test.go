package api

import (
	"database/sql"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/calibration"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
)

// The routing report's gate is a copy of calibration's (an import would be a
// cycle); this pins the two together.
func TestRouteMinSamplesMatchesCalibration(t *testing.T) {
	if route.MinSamples != calibration.MinSamples {
		t.Fatalf("route.MinSamples = %d, calibration.MinSamples = %d", route.MinSamples, calibration.MinSamples)
	}
}

// seedRoutedCard inserts a board card in the given state plus the shadow
// decision its dispatch would have recorded, and returns the card id.
func seedRoutedCard(t *testing.T, db *sql.DB, tier, status, column string) int64 {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO projects(id, path, slug, first_seen) VALUES (1, '/repo', 'p', 'now')`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, created_at, status, board_column, source)
		VALUES (1, 't', 'p', 'now', ?, ?, 'queue')`, status, column)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	uuid := fmt.Sprintf("route-%d", id)
	if _, err := db.Exec(`UPDATE tasks SET dispatch_session_uuid = ? WHERE id = ?`, uuid, id); err != nil {
		t.Fatal(err)
	}
	if err := route.Record(db, route.Row{
		Surface: route.SurfaceDispatch, Subject: route.SubjectTask(id), SessionUUID: uuid, Mode: route.ModeShadow,
		Decision:  route.Decision{Tier: tier, Model: "haiku", Effort: "low", Playbook: "standard", Reasons: []string{"file_scope=1 (+0)"}},
		UsedModel: "claude-sonnet-5", UsedEffort: "medium", UsedPlaybook: "standard", WonRung: route.RungDefault,
		CreatedAt: time.Now().Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRouteReportSettlesAndHidesSmallGroups(t *testing.T) {
	srv, db := testServerWithDB(t)
	for i := 0; i < 20; i++ {
		status, column := "done", "done"
		if i < 2 {
			status, column = "needs_review", "in_review" // clean review
		}
		seedRoutedCard(t, db, route.TierS, status, column)
	}
	for i := 0; i < 5; i++ {
		seedRoutedCard(t, db, route.TierL, "done", "done")
	}
	seedRoutedCard(t, db, route.TierS, "running", "in_progress") // still in flight

	var rep route.ReportResult
	getJSON(t, srv.URL+"/api/route/report?surface=dispatch&days=7", &rep)
	if rep.Rows != 25 || rep.Unsettled != 1 || rep.MinSamples != 20 || rep.Days != 7 || rep.Surface != "dispatch" {
		t.Fatalf("header = rows %d unsettled %d min %d days %d surface %q",
			rep.Rows, rep.Unsettled, rep.MinSamples, rep.Days, rep.Surface)
	}
	if len(rep.ByTier.Groups) != 1 || rep.ByTier.Groups[0].Tier != route.TierS || rep.ByTier.Groups[0].N != 20 {
		t.Fatalf("byTier = %+v", rep.ByTier.Groups)
	}
	if rep.ByTier.HiddenGroups != 1 || rep.ByTier.HiddenRuns != 5 {
		t.Errorf("byTier hidden = %d/%d, want 1/5", rep.ByTier.HiddenGroups, rep.ByTier.HiddenRuns)
	}
	if g := rep.ByTier.Groups[0]; g.FailRate != 0 || g.Agree != 0 {
		t.Errorf("tier S = %+v, want failRate 0, agree 0 (haiku picked, sonnet ran)", g)
	}
	if len(rep.Divergent.Groups) != 1 || rep.Divergent.Groups[0].Pick != "haiku" || rep.Divergent.Groups[0].Model != "sonnet" {
		t.Errorf("divergent = %+v", rep.Divergent.Groups)
	}
	var settled int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_decisions WHERE outcome IS NOT NULL`).Scan(&settled); err != nil {
		t.Fatal(err)
	}
	if settled != 25 {
		t.Errorf("settled rows = %d, want 25 (the report request settles first)", settled)
	}

	for _, q := range []string{"surface=board", "days=0", "days=x", "days=99999"} {
		if r := decisionReq(t, http.MethodGet, srv.URL+"/api/route/report?"+q, ""); r.StatusCode != http.StatusBadRequest {
			t.Errorf("GET report?%s = %d, want 400", q, r.StatusCode)
		}
	}
	// Default window, both surfaces.
	getJSON(t, srv.URL+"/api/route/report", &rep)
	if rep.Days != 30 || rep.Surface != "" {
		t.Errorf("defaults = days %d surface %q", rep.Days, rep.Surface)
	}
}

func TestRouteDecisionEndpoint(t *testing.T) {
	srv, db := testServerWithDB(t)
	id := seedRoutedCard(t, db, route.TierM, "done", "done")

	var v route.DecisionView
	getJSON(t, srv.URL+"/api/route/decision?subject="+route.SubjectTask(id), &v)
	if v.Tier != route.TierM || v.PickModel != "haiku" || v.UsedModel != "claude-sonnet-5" ||
		v.WonRung != route.RungDefault || v.Mode != "shadow" || len(v.Reasons) != 1 {
		t.Errorf("decision = %+v", v)
	}
	for q, want := range map[string]int{
		"subject=task:999999": http.StatusNotFound,
		"subject=task:abc":    http.StatusBadRequest,
		"subject=board:1":     http.StatusBadRequest,
		"":                    http.StatusBadRequest,
	} {
		if r := decisionReq(t, http.MethodGet, srv.URL+"/api/route/decision?"+q, ""); r.StatusCode != want {
			t.Errorf("GET decision?%s = %d, want %d", q, r.StatusCode, want)
		}
	}
}
