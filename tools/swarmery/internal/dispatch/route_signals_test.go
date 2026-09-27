package dispatch

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// ── signal extraction ──

func TestRouteSignals_Scope(t *testing.T) {
	cases := []struct {
		name      string
		scope     []string
		wantScope int
		wantAreas int
		wantRisk  []string
	}{
		{"empty scope is unknown, not zero", nil, -1, -1, nil},
		{"blank entries are no scope", []string{"", "  "}, -1, -1, nil},
		{"one file", []string{"README.md"}, 1, 1, nil},
		{"depth-2 areas", []string{
			"tools/swarmery/internal/a.go", "tools/swarmery/web/b.ts", "plugins/core/x.md",
		}, 3, 2, nil},
		{"trailing slash names the dir itself", []string{"internal/store/", "internal/store/x.go", "internal/api/x.go"}, 3, 2, nil},
		{"risk paths", []string{
			"internal/store/migrations/0001_x.sql", "db/seed.sql", "internal/auth/token.go",
			"api/openapi.yaml", "proto/svc.proto", "go.mod", "web/package.json",
			"web/package-lock.json", "internal/ui/button.go",
		}, 9, 8, []string{
			"internal/store/migrations/0001_x.sql", "db/seed.sql", "internal/auth/token.go",
			"api/openapi.yaml", "proto/svc.proto", "go.mod", "web/package.json", "web/package-lock.json",
		}},
		{"root-level migrations dir", []string{"migrations/"}, 1, 1, []string{"migrations/"}},
		{"case-insensitive match", []string{"src/OAuth/Client.go"}, 1, 1, []string{"src/OAuth/Client.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			s := signalsFor(candidate{ID: 1, ProjectID: 1, Prompt: "p", FileScope: tc.scope}, db)
			if s.FileScope != tc.wantScope || s.Areas != tc.wantAreas {
				t.Errorf("file_scope=%d areas=%d, want %d/%d", s.FileScope, s.Areas, tc.wantScope, tc.wantAreas)
			}
			if !reflect.DeepEqual(s.RiskPaths, tc.wantRisk) {
				t.Errorf("risk_paths = %q, want %q", s.RiskPaths, tc.wantRisk)
			}
			if s.Surface != route.SurfaceDispatch {
				t.Errorf("surface = %q", s.Surface)
			}
		})
	}
}

func TestRouteSignals_PromptAndDeps(t *testing.T) {
	db := testDB(t)
	s := signalsFor(candidate{ID: 1, ProjectID: 1, Prompt: strings.Repeat("x", 1234),
		Dependencies: []string{"T-1", " ", "T-2"}}, db)
	if s.PromptBytes != 1234 || s.Deps != 2 {
		t.Errorf("prompt_bytes=%d deps=%d, want 1234/2", s.PromptBytes, s.Deps)
	}
	if s.ForecastSize != "" {
		t.Errorf("forecast_size = %q on a card", s.ForecastSize)
	}
}

// addVerdict inserts a terminal verification run for a card.
func addVerdict(t *testing.T, db *sql.DB, taskID int64, status string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO verification_runs(target_key, task_id, status, started_at)
		VALUES(?, ?, ?, '2026-09-01T00:00:00Z')`, "task:"+itoa(int(taskID)), taskID, status); err != nil {
		t.Fatalf("insert verdict: %v", err)
	}
}

func TestRouteSignals_History(t *testing.T) {
	db := testDB(t)
	self := insertTask(t, db, "T-self", taskOpts{})
	// Four verified cards: below the router's n≥5 gate.
	for i, v := range []string{"fail", "fail", "pass", "fail"} {
		id := insertTask(t, db, "T-h"+itoa(i), taskOpts{column: "done"})
		addVerdict(t, db, id, v)
	}
	// Noise that must not count: a non-terminal run and another project's card.
	noisy := insertTask(t, db, "T-noise", taskOpts{column: "done"})
	addVerdict(t, db, noisy, "inconclusive")
	if _, err := db.Exec(`INSERT INTO projects(id, path, slug, first_seen) VALUES(2,'/other','o','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	other := insertTask(t, db, "T-other", taskOpts{column: "done", projectID: 2})
	addVerdict(t, db, other, "fail")

	c := candidate{ID: self, ProjectID: 1, Prompt: "p"}
	s := signalsFor(c, db)
	if s.HistSamples != 4 || s.HistFailRate != 0.75 {
		t.Fatalf("history = %v over n=%d, want 0.75 over 4", s.HistFailRate, s.HistSamples)
	}
	d := route.Decide(s, route.DefaultPolicy())
	if !containsReason(d.Reasons, "history: n<5, ignored") {
		t.Errorf("n<5 must be ignored by the router; reasons = %q", d.Reasons)
	}

	// A fifth card crosses the gate; a card re-verified pass→fail counts once, as its latest.
	fifth := insertTask(t, db, "T-h5", taskOpts{column: "done"})
	addVerdict(t, db, fifth, "pass")
	addVerdict(t, db, fifth, "fail")
	s = signalsFor(c, db)
	if s.HistSamples != 5 || s.HistFailRate != 0.8 {
		t.Fatalf("history = %v over n=%d, want 0.8 over 5", s.HistFailRate, s.HistSamples)
	}
	d = route.Decide(s, route.DefaultPolicy())
	if !containsReason(d.Reasons, "history: fail_rate=0.8") {
		t.Errorf("history should count at n=5; reasons = %q", d.Reasons)
	}
}

func TestRouteSignals_HistoryWindow(t *testing.T) {
	db := testDB(t)
	// 5 old failures, then 30 newer passes: only the newest 30 count.
	for i := 0; i < 35; i++ {
		v := "pass"
		if i < 5 {
			v = "fail"
		}
		addVerdict(t, db, insertTask(t, db, "T-w"+itoa(i), taskOpts{column: "done"}), v)
	}
	s := signalsFor(candidate{ID: 999, ProjectID: 1, Prompt: "p"}, db)
	if s.HistSamples != routeHistoryWindow || s.HistFailRate != 0 {
		t.Errorf("history = %v over n=%d, want 0 over %d", s.HistFailRate, s.HistSamples, routeHistoryWindow)
	}
}

func TestRouteSignals_HistoryReadFailureIsZeroSamples(t *testing.T) {
	db := testDB(t)
	db.Close()
	s := signalsFor(candidate{ID: 1, ProjectID: 1, Prompt: "p"}, db)
	if s.HistSamples != 0 || s.HistFailRate != 0 {
		t.Errorf("history on a dead DB = %v/%d, want 0/0", s.HistFailRate, s.HistSamples)
	}
}

func containsReason(reasons []string, frag string) bool {
	for _, r := range reasons {
		if strings.Contains(r, frag) {
			return true
		}
	}
	return false
}

// ── shadow is inert / recording ──

// runOnce dispatches one card in mode and returns every spawn spec the runner
// saw plus the db, so a test can compare spawns across modes.
func runOnce(t *testing.T, mode string, prep func(db *sql.DB, id int64)) ([]RunSpec, *sql.DB, int64) {
	t.Helper()
	t.Setenv(routeModeEnv, mode)
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-route", taskOpts{fileScope: `["internal/store/migrations/0001.sql","internal/api/x.go"]`})
	if prep != nil {
		prep(db, id)
	}
	s.Schedule()
	if r.count() == 0 {
		t.Fatalf("mode=%s: nothing spawned", mode)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]RunSpec(nil), r.specs...), db, id
}

// argvOf renders a spec the way ClaudeRunner.Start hands it to runcore, so the
// comparison covers --model and --effort exactly as the process would see them.
func argvOf(spec RunSpec) []string {
	return runcore.Args(runcore.Spec{
		Prompt: agentPrompt(spec), SessionUUID: spec.SessionUUID, Model: spec.Model,
		Effort:         runEffort(spec),
		PermissionMode: permissionMode(spec.PermissionMode), SettingSources: "project,local",
		SettingsFile: spec.SettingsFile,
	})
}

func routeRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM route_decisions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRouteShadowIsInert(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(db *sql.DB, id int64)
	}{
		{"auto-profiled card", nil},
		{"long card (plan-first, two stages)", func(db *sql.DB, id int64) {
			setTaskPrompt(t, db, id, strings.Repeat("x", 5000))
		}},
		{"card model override", func(db *sql.DB, id int64) { setTaskModel(t, db, id, "claude-opus-5") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			off, _, _ := runOnce(t, "off", tc.prep)
			shadow, db, _ := runOnce(t, "shadow", tc.prep)
			if routeRows(t, db) != 1 {
				t.Fatalf("shadow wrote %d route rows, want 1 — the comparison is vacuous", routeRows(t, db))
			}
			if !reflect.DeepEqual(off, shadow) {
				t.Fatalf("shadow changed the spawn specs\n off    %+v\n shadow %+v", off, shadow)
			}
			for i := range off {
				a, b := argvOf(off[i]), argvOf(shadow[i])
				if !reflect.DeepEqual(a, b) {
					t.Errorf("stage %d argv differs\n off    %q\n shadow %q", i+1, a, b)
				}
			}
		})
	}
}

func TestRouteRecording_OffWritesNothing(t *testing.T) {
	_, db, _ := runOnce(t, "off", nil)
	if n := routeRows(t, db); n != 0 {
		t.Errorf("mode=off wrote %d route rows, want 0", n)
	}
}

func TestRouteRecording_ShadowWritesOneRowPerRun(t *testing.T) {
	// Two stages (plan-first) still record once: signals are per run, not per stage.
	specs, db, id := runOnce(t, "shadow", func(db *sql.DB, id int64) {
		setTaskPrompt(t, db, id, strings.Repeat("x", 5000))
	})
	if len(specs) != 2 {
		t.Fatalf("ran %d stages, want 2", len(specs))
	}
	if n := routeRows(t, db); n != 1 {
		t.Fatalf("shadow wrote %d route rows for one run, want 1", n)
	}
	var (
		surface, subject, uuid, mode, wonRung, usedModel, usedEffort, usedPlaybook, tier string
		applied, score                                                                   int
	)
	if err := db.QueryRow(`SELECT surface, subject, session_uuid, mode, applied, won_rung,
		used_model, used_effort, used_playbook, score, tier FROM route_decisions`).Scan(
		&surface, &subject, &uuid, &mode, &applied, &wonRung, &usedModel, &usedEffort, &usedPlaybook,
		&score, &tier); err != nil {
		t.Fatal(err)
	}
	if surface != "dispatch" || subject != route.SubjectTask(id) || mode != "shadow" || applied != 0 {
		t.Errorf("row = %s %s %s applied=%d", surface, subject, mode, applied)
	}
	if wonRung == "" {
		t.Error("won_rung empty: a shadow row must name the ladder rung that ran")
	}
	if uuid != specs[0].SessionUUID {
		t.Errorf("session_uuid = %q, want the FIRST stage's %q", uuid, specs[0].SessionUUID)
	}
	if usedModel != specs[0].Model || wonRung != route.RungDefault {
		t.Errorf("used_model=%q rung=%q, want %q/default", usedModel, wonRung, specs[0].Model)
	}
	if usedEffort != DefaultEffort || usedPlaybook != "plan-first" {
		t.Errorf("used_effort=%q used_playbook=%q, want %s/plan-first", usedEffort, usedPlaybook, DefaultEffort)
	}
	if tier == "" || score <= 0 {
		t.Errorf("tier=%q score=%d, want a scored decision", tier, score)
	}
}

func TestRouteRecording_RungsFollowTheLadder(t *testing.T) {
	_, db, _ := runOnce(t, "shadow", func(db *sql.DB, id int64) { setTaskModel(t, db, id, "claude-opus-5") })
	var rung, model string
	if err := db.QueryRow(`SELECT won_rung, used_model FROM route_decisions`).Scan(&rung, &model); err != nil {
		t.Fatal(err)
	}
	if rung != route.RungCard || model != "claude-opus-5" {
		t.Errorf("rung=%q model=%q, want card/claude-opus-5", rung, model)
	}

	c := candidate{}
	if m, r := stageModel(c, resolvedPlaybook{model: "claude-opus-5"}); m != "claude-opus-5" || r != route.RungPlaybook {
		t.Errorf("playbook rung = %q/%q", m, r)
	}
}

// An unreadable policy routes nothing in EITHER mode that consults it: no row,
// and — in active — the spawn falls back to the pre-router ladders, so a
// policy typo can never change what runs. (Active applying a readable policy
// is pinned in route_active_test.go.)
func TestRouteRecording_BadPolicySkips(t *testing.T) {
	t.Setenv(route.EnvPolicy, "/does/not/exist.json")
	for _, mode := range []string{"shadow", "active"} {
		specs, db, _ := runOnce(t, mode, nil)
		if n := routeRows(t, db); n != 0 {
			t.Errorf("mode=%s: unreadable policy wrote %d rows, want 0 (logged and skipped)", mode, n)
		}
		off, _, _ := runOnce(t, "off", nil)
		if !reflect.DeepEqual(specs, off) {
			t.Errorf("mode=%s with an unreadable policy changed the spawn\n got %+v\n off %+v", mode, specs, off)
		}
	}
}
