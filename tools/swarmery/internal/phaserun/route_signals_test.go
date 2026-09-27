package phaserun

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// addForecast inserts a phase_forecasts row.
func addForecast(t *testing.T, db *sql.DB, phaseID int64, kind, size, areas, files, risks string, postHoc int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO phase_forecasts(phase_id, kind, size_band, areas_json, files_json, risks_json, post_hoc)
		VALUES(?,?,?,?,?,?,?)`, phaseID, kind, size, areas, files, risks, postHoc)
}

// addActual inserts a phase_actuals row with the given outcome.
func addActual(t *testing.T, db *sql.DB, phaseID int64, uuid, outcome string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO phase_actuals(phase_id, session_uuid, outcome, computed_at)
		VALUES(?,?,?,'2026-09-01T00:00:00Z')`, phaseID, uuid, outcome)
}

func TestRouteSignals_NoPrior(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	for _, tc := range []struct {
		phase    int64
		wantDeps int
	}{{p1, 0}, {p2, 1}} {
		s := signalsForPhase(db, tc.phase)
		want := route.Signals{Surface: route.SurfacePhaseRun, FileScope: -1, Areas: -1, Deps: tc.wantDeps}
		if !reflect.DeepEqual(s, want) {
			t.Errorf("phase %d signals = %+v, want %+v", tc.phase, s, want)
		}
	}
}

func TestRouteSignals_PriorFeedsSizeAreasFilesRisks(t *testing.T) {
	db, _, p1, _ := fixture(t)
	addForecast(t, db, p1, "prior", "L", `["internal/route","internal/store"]`,
		`["a.go","b.go","c.go"]`, `["migration slot collision"]`, 0)
	s := signalsForPhase(db, p1)
	if s.ForecastSize != "L" || s.Areas != 2 || s.FileScope != 3 {
		t.Errorf("size=%q areas=%d files=%d, want L/2/3", s.ForecastSize, s.Areas, s.FileScope)
	}
	if !reflect.DeepEqual(s.RiskPaths, []string{"migration slot collision"}) {
		t.Errorf("risk_paths = %q", s.RiskPaths)
	}
	// A forecast size replaces prompt length in the score.
	d := route.Decide(s, route.DefaultPolicy())
	if d.Playbook != "" {
		t.Errorf("phaserun decision carries a playbook %q", d.Playbook)
	}
}

func TestRouteSignals_PostHocAndPosteriorIgnored(t *testing.T) {
	db, _, p1, _ := fixture(t)
	addForecast(t, db, p1, "prior", "S", `["a"]`, `[]`, `[]`, 0)
	// Later rows that must not win: a post-hoc prior and a posterior.
	addForecast(t, db, p1, "prior", "XL", `["a","b","c","d"]`, `["x"]`, `["r"]`, 1)
	addForecast(t, db, p1, "posterior", "XL", `["a","b","c","d"]`, `["x"]`, `["r"]`, 0)
	s := signalsForPhase(db, p1)
	if s.ForecastSize != "S" || s.Areas != 1 || s.FileScope != -1 || len(s.RiskPaths) != 0 {
		t.Errorf("signals = %+v, want the non-post-hoc prior only (S, areas 1, files unknown, no risks)", s)
	}

	// Only a post-hoc prior ⇒ treated as no prior at all.
	db2, _, q1, _ := fixture(t)
	addForecast(t, db2, q1, "Prior", "XL", `["a"]`, `["x"]`, `["r"]`, 1)
	if s := signalsForPhase(db2, q1); s.ForecastSize != "" || s.Areas != -1 || s.FileScope != -1 {
		t.Errorf("post-hoc-only signals = %+v, want no prior", s)
	}
}

func TestRouteSignals_LatestPriorWins(t *testing.T) {
	db, _, p1, _ := fixture(t)
	addForecast(t, db, p1, "prior", "S", `[]`, `[]`, `[]`, 0)
	addForecast(t, db, p1, " PRIOR ", "M", `[]`, `[]`, `[]`, 0)
	if s := signalsForPhase(db, p1); s.ForecastSize != "M" || s.Areas != -1 {
		t.Errorf("signals = %+v, want the latest prior (M) with empty areas unknown", s)
	}
}

func TestRouteSignals_History(t *testing.T) {
	db, _, p1, p2 := fixture(t)
	addActual(t, db, p2, "u1", "completed")
	addActual(t, db, p2, "u2", "failed")
	addActual(t, db, p2, "u3", "partial")
	addActual(t, db, p2, "u4", "noop") // not a verdict
	// Another project's run must not count.
	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'/other','o','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO tasks(id, project_id, title, prompt, status, created_at, source)
		VALUES(900, 2, 'x', 'x', 'running', '2026-01-01T00:00:00Z', 'workspace')`)
	mustExec(t, db, `INSERT INTO epic_phases(id, workspace_task_id, seq, name, doc_path) VALUES(900, 900, 1, 'o', '/o.md')`)
	addActual(t, db, 900, "u9", "failed")

	s := signalsForPhase(db, p1)
	if s.HistSamples != 3 || s.HistFailRate != 2.0/3.0 {
		t.Errorf("history = %v over n=%d, want 2/3 over 3", s.HistFailRate, s.HistSamples)
	}
	d := route.Decide(s, route.DefaultPolicy())
	found := false
	for _, r := range d.Reasons {
		found = found || strings.Contains(r, "history: n<5, ignored")
	}
	if !found {
		t.Errorf("n=3 must be ignored by the router; reasons = %q", d.Reasons)
	}
}

func TestRouteSignals_UnknownPhaseIsAllUnknown(t *testing.T) {
	db, _, _, _ := fixture(t)
	s := signalsForPhase(db, 424242)
	want := route.Signals{Surface: route.SurfacePhaseRun, FileScope: -1, Areas: -1}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("signals = %+v, want %+v", s, want)
	}
}

func TestRouteSignals_ModelRung(t *testing.T) {
	t.Setenv(modelEnv, "")
	if r := modelRung("opus", "sonnet"); r != route.RungRequest {
		t.Errorf("request rung = %q", r)
	}
	if r := modelRung("", "sonnet"); r != route.RungDoc {
		t.Errorf("doc rung = %q", r)
	}
	if r := modelRung("", ""); r != route.RungDefault {
		t.Errorf("default rung = %q", r)
	}
	t.Setenv(modelEnv, "claude-opus-5[1m]")
	if r := modelRung("", ""); r != route.RungEnv {
		t.Errorf("env rung = %q", r)
	}
}

// ── shadow is inert / recording ──

// startOnce runs phase 1 of a fresh fixture in the given router mode and
// returns every spec the runner saw plus the db.
func startOnce(t *testing.T, mode, model, effort string) ([]RunSpec, *sql.DB, int64) {
	t.Helper()
	t.Setenv(routeModeEnv, mode)
	db, _, p1, _ := fixture(t)
	addForecast(t, db, p1, "prior", "L", `["internal/a","internal/b"]`, `["x.go"]`, `["risky"]`, 0)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, model, effort); err != nil {
		t.Fatalf("mode=%s Start: %v", mode, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RunSpec(nil), r.specs...), db, p1
}

func argvOf(spec RunSpec) []string {
	return runcore.Args(runcore.Spec{
		Prompt: spec.Prompt, SessionUUID: spec.SessionUUID, Resume: spec.Resume,
		Model: spec.Model, Effort: spec.Effort, SettingsFile: spec.SettingsFile,
	})
}

func routeRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_decisions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRouteShadowIsInert(t *testing.T) {
	for _, tc := range []struct{ name, model, effort string }{
		{"ladder defaults", "", ""},
		{"request model and effort", "opus", "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			off, _, _ := startOnce(t, "off", tc.model, tc.effort)
			shadow, db, _ := startOnce(t, "shadow", tc.model, tc.effort)
			if routeRowCount(t, db) != 1 {
				t.Fatalf("shadow wrote %d rows, want 1 — the comparison is vacuous", routeRowCount(t, db))
			}
			if !reflect.DeepEqual(off, shadow) {
				t.Fatalf("shadow changed the spawn specs\n off    %+v\n shadow %+v", off, shadow)
			}
			for i := range off {
				if a, b := argvOf(off[i]), argvOf(shadow[i]); !reflect.DeepEqual(a, b) {
					t.Errorf("spawn %d argv differs\n off    %q\n shadow %q", i+1, a, b)
				}
			}
		})
	}
}

func TestRouteRecording_OffWritesNothing(t *testing.T) {
	_, db, _ := startOnce(t, "off", "", "")
	if n := routeRowCount(t, db); n != 0 {
		t.Errorf("mode=off wrote %d rows, want 0", n)
	}
}

func TestRouteRecording_ShadowWritesOneRow(t *testing.T) {
	t.Setenv(modelEnv, "")
	t.Setenv(effortEnv, "")
	specs, db, p1 := startOnce(t, "shadow", "", "")
	if n := routeRowCount(t, db); n != 1 {
		t.Fatalf("shadow wrote %d rows, want 1", n)
	}
	var (
		surface, subject, uuid, mode, wonRung, usedModel, usedEffort, usedPlaybook string
		pickModel, pickPlaybook, sigJSON                                           string
		applied                                                                    int
	)
	if err := db.QueryRow(`SELECT surface, subject, session_uuid, mode, applied, won_rung,
		used_model, used_effort, used_playbook, pick_model, pick_playbook, signals_json
		FROM route_decisions`).Scan(&surface, &subject, &uuid, &mode, &applied, &wonRung,
		&usedModel, &usedEffort, &usedPlaybook, &pickModel, &pickPlaybook, &sigJSON); err != nil {
		t.Fatal(err)
	}
	if surface != "phaserun" || subject != route.SubjectPhase(p1) || mode != "shadow" || applied != 0 {
		t.Errorf("row = %s %s %s applied=%d", surface, subject, mode, applied)
	}
	if wonRung != route.RungDefault {
		t.Errorf("won_rung = %q, want default", wonRung)
	}
	if uuid != specs[0].SessionUUID || usedModel != specs[0].Model || usedEffort != specs[0].Effort {
		t.Errorf("used = %s/%s/%s, spawn = %s/%s/%s", uuid, usedModel, usedEffort,
			specs[0].SessionUUID, specs[0].Model, specs[0].Effort)
	}
	if usedModel != planning.DefaultModel || usedEffort != DefaultEffort || usedPlaybook != "" || pickPlaybook != "" {
		t.Errorf("used_model=%q used_effort=%q used_playbook=%q pick_playbook=%q", usedModel, usedEffort, usedPlaybook, pickPlaybook)
	}
	if pickModel == "" {
		t.Error("pick_model empty")
	}
	if !strings.Contains(sigJSON, `"forecast_size":"L"`) {
		t.Errorf("signals_json does not carry the prior: %s", sigJSON)
	}
}

func TestRouteRecording_RequestRungAndRefusedStartWritesNothing(t *testing.T) {
	_, db, _ := startOnce(t, "shadow", "opus", "")
	var rung string
	if err := db.QueryRow(`SELECT won_rung FROM route_decisions`).Scan(&rung); err != nil {
		t.Fatal(err)
	}
	if rung != route.RungRequest {
		t.Errorf("won_rung = %q, want request", rung)
	}

	// A refused Start (unmet dependency) records nothing: no run, no row.
	t.Setenv(routeModeEnv, "shadow")
	db2, _, _, p2 := fixture(t)
	s := newTestService(db2, &stubRunner{}, &stubWt{})
	if _, err := s.Start(p2, "", ""); err == nil {
		t.Fatal("Start of a phase with unmet deps succeeded")
	}
	if n := routeRowCount(t, db2); n != 0 {
		t.Errorf("refused Start wrote %d rows", n)
	}
}

func TestRouteRecording_ActiveRecordsAsShadowAndBadPolicySkips(t *testing.T) {
	active, db, _ := startOnce(t, "active", "", "")
	var mode string
	var applied int
	if err := db.QueryRow(`SELECT mode, applied FROM route_decisions`).Scan(&mode, &applied); err != nil {
		t.Fatal(err)
	}
	if mode != "shadow" || applied != 0 {
		t.Errorf("active before phase 4: mode=%q applied=%d", mode, applied)
	}
	off, _, _ := startOnce(t, "off", "", "")
	if !reflect.DeepEqual(active, off) {
		t.Error("active changed the spawn before it is implemented")
	}

	t.Setenv(route.EnvPolicy, "/does/not/exist.json")
	_, db2, _ := startOnce(t, "shadow", "", "")
	if n := routeRowCount(t, db2); n != 0 {
		t.Errorf("unreadable policy wrote %d rows, want 0", n)
	}
}
