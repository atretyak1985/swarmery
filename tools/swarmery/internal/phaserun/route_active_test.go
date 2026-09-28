package phaserun

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
)

// The phaserun ladders in active mode (plan phase 4), first non-empty wins:
//
//	model   request → doc **Model:** → ROUTE → SWARMERY_PHASERUN_MODEL → planning.DefaultModel
//	effort  request → doc **Effort:** → ROUTE → SWARMERY_PHASERUN_EFFORT → DefaultEffort
//
// The fixture's phase 1 has no prior forecast, so the default policy scores it
// 10 (file scope unknown) ⇒ tier S ⇒ haiku / low. Both values differ from every
// other rung's, so each assertion names exactly one rung.

// quietKnobs clears every env knob the phaserun spawn depends on.
func quietKnobs(t *testing.T) {
	t.Helper()
	for _, k := range []string{modelEnv, effortEnv, claudeflags.EffortEnv, permEnv, claudeflags.ModeEnv, timeoutEnv, route.EnvPolicy} {
		t.Setenv(k, "")
	}
}

// startPhase runs phase 1 of a fresh fixture in mode; prep edits the fixture
// first. It returns the runner (every spec it saw) and the db.
func startPhase(t *testing.T, mode, model, effort string, prep func(db *sql.DB, p1 int64)) (*stubRunner, *sql.DB) {
	t.Helper()
	t.Setenv(routeModeEnv, mode)
	db, _, p1, _ := fixture(t)
	if prep != nil {
		prep(db, p1)
	}
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, model, effort); err != nil {
		t.Fatalf("mode=%s Start: %v", mode, err)
	}
	return r, db
}

func withDocModel(t *testing.T, model string) func(db *sql.DB, p1 int64) {
	return func(db *sql.DB, p1 int64) {
		mustExec(t, db, `UPDATE epic_phases SET doc_model=? WHERE id=?`, model, p1)
	}
}

func withDocEffort(t *testing.T, effort string) func(db *sql.DB, p1 int64) {
	return func(db *sql.DB, p1 int64) {
		mustWriteDoc(t, phaseDocPath(t, db, p1), "# Phase 1 — Schema\n\n**Effort:** "+effort+"\n\n- [ ] a\n- [ ] b\n")
	}
}

type routeRow struct {
	mode, wonRung, usedModel, usedEffort, tier, pickModel string
	applied                                               int
}

func readRouteRow(t *testing.T, db *sql.DB) routeRow {
	t.Helper()
	var r routeRow
	if err := db.QueryRow(`SELECT mode, applied, won_rung, used_model, used_effort, tier, pick_model
		FROM route_decisions`).Scan(&r.mode, &r.applied, &r.wonRung, &r.usedModel, &r.usedEffort,
		&r.tier, &r.pickModel); err != nil {
		t.Fatalf("read route row: %v", err)
	}
	return r
}

// ── ladders, unit ──

func TestRouteActive_ResolveModelLadder(t *testing.T) {
	quietKnobs(t)
	const routed = route.ModelHaiku
	cases := []struct {
		name, choice, doc, routed, env, want string
	}{
		{"request beats doc and route", "sonnet", "fable", routed, "env-model", planning.Models["sonnet"]},
		{"doc beats route", "", "fable", routed, "env-model", planning.Models["fable"]},
		{"route beats env", "", "", routed, "env-model", routed},
		{"env when no route", "", "", "", "env-model", "env-model"},
		{"default last", "", "", "", "", planning.DefaultModel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(modelEnv, tc.env)
			got, err := resolveModel(tc.choice, tc.doc, "/plan/phase-1.md", tc.routed)
			if err != nil || got != tc.want {
				t.Errorf("resolveModel = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	// A bad doc model still fails loudly — a route pick never papers over it.
	if _, err := resolveModel("", "gpt-9", "/plan/phase-1.md", routed); !errors.As(err, new(*DocModelError)) {
		t.Errorf("bad doc model with a route pick: err = %v, want *DocModelError", err)
	}
}

func TestRouteActive_ResolveEffortLadder(t *testing.T) {
	quietKnobs(t)
	doc := "# P\n\n**Effort:** high\n"
	none := "# P\n\nnothing\n"
	cases := []struct {
		name, choice, doc, routed, env, want string
	}{
		{"request beats doc and route", "max", doc, "low", "xhigh", "max"},
		{"doc beats route", "", doc, "low", "xhigh", "high"},
		{"route beats env", "", none, "low", "xhigh", "low"},
		{"env when no route", "", none, "", "xhigh", "xhigh"},
		{"default last", "", none, "", "", DefaultEffort},
		{"unusable route falls through", "", none, "ludicrous", "xhigh", "xhigh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(effortEnv, tc.env)
			got, err := resolveEffort(tc.choice, tc.doc, "/plan/phase-1.md", tc.routed)
			if err != nil || got != tc.want {
				t.Errorf("resolveEffort = %q, %v; want %q", got, err, tc.want)
			}
			if r := effortRung(tc.choice, tc.doc, tc.routed); (r == route.RungRoute) != (tc.want == "low") {
				t.Errorf("effortRung = %q disagrees with the ladder's %q", r, tc.want)
			}
		})
	}
}

// ── ladders, through Start ──

func TestRouteActive_ModelLadderThroughStart(t *testing.T) {
	quietKnobs(t)
	t.Setenv(modelEnv, "claude-env-pinned")
	for _, tc := range []struct {
		name, request string
		prep          func(db *sql.DB, p1 int64)
		want, rung    string
	}{
		{"request", "sonnet", withDocModel(t, "fable"), planning.Models["sonnet"], route.RungRequest},
		{"doc", "", withDocModel(t, "fable"), planning.Models["fable"], route.RungDoc},
		{"route over env", "", nil, route.ModelHaiku, route.RungRoute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, db := startPhase(t, "active", tc.request, "", tc.prep)
			if got := r.firstSpec().Model; got != tc.want {
				t.Errorf("model = %q, want %q", got, tc.want)
			}
			if row := readRouteRow(t, db); row.wonRung != tc.rung || row.usedModel != tc.want || row.mode != "active" {
				t.Errorf("row = %+v, want won_rung=%s used_model=%s mode=active", row, tc.rung, tc.want)
			}
		})
	}
	// Off: the env knob is back on top of the default, exactly as before.
	r, _ := startPhase(t, "off", "", "", nil)
	if got := r.firstSpec().Model; got != "claude-env-pinned" {
		t.Errorf("off model = %q, want the env knob", got)
	}
}

func TestRouteActive_EffortLadderThroughStart(t *testing.T) {
	quietKnobs(t)
	t.Setenv(effortEnv, "xhigh")
	for _, tc := range []struct {
		name, request string
		prep          func(db *sql.DB, p1 int64)
		want          string
	}{
		{"request", "max", withDocEffort(t, "high"), "max"},
		{"doc", "", withDocEffort(t, "high"), "high"},
		{"route over env", "", nil, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := startPhase(t, "active", "", tc.request, tc.prep)
			if got := r.firstSpec().Effort; got != tc.want {
				t.Errorf("effort = %q, want %q", got, tc.want)
			}
		})
	}
	r, _ := startPhase(t, "off", "", "", nil)
	if got := r.firstSpec().Effort; got != "xhigh" {
		t.Errorf("off effort = %q, want the env knob", got)
	}
}

// applied is true when the router's model OR effort ran; won_rung is the model
// rung only. A run whose request pinned both records the decision unapplied.
func TestRouteActive_AppliedAndWonRung(t *testing.T) {
	quietKnobs(t)
	for _, tc := range []struct {
		name, model, effort string
		applied             int
		rung                string
	}{
		{"router owns both", "", "", 1, route.RungRoute},
		{"request model, route effort", "sonnet", "", 1, route.RungRequest},
		{"route model, request effort", "", "max", 1, route.RungRoute},
		{"request owns both", "sonnet", "max", 0, route.RungRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, db := startPhase(t, "active", tc.model, tc.effort, nil)
			row := readRouteRow(t, db)
			if row.mode != "active" || row.applied != tc.applied || row.wonRung != tc.rung {
				t.Errorf("row = %+v, want active applied=%d won_rung=%s", row, tc.applied, tc.rung)
			}
		})
	}
	// Shadow is untouched: applied=0, never the route rung.
	_, db := startPhase(t, "shadow", "", "", nil)
	if row := readRouteRow(t, db); row.mode != "shadow" || row.applied != 0 || row.wonRung == route.RungRoute {
		t.Errorf("shadow row = %+v", row)
	}
}

// A continuation is a resume of the same run: it must carry the route's model
// and effort, not re-walk the ladders (it copies the whole spec).
func TestRouteActive_ContinuationKeepsRoutePicks(t *testing.T) {
	quietKnobs(t)
	t.Setenv(routeModeEnv, "active")
	db, _, p1, _ := fixture(t)
	doc := phaseDocPath(t, db, p1)
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		if spec.Resume {
			mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
			seedTranscript(t, db, spec.SessionUUID, "Finished.\n\nPHASE DONE")
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
		mustWriteDoc(t, doc, "# Phase 1 — Schema\n\n- [x] a\n- [ ] b\n")
		seedTranscript(t, db, spec.SessionUUID, "Did the first one. Shall I continue?")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	s := newTestService(db, r, &stubWt{})
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := r.specCount(); n != 2 {
		t.Fatalf("spawned %d times, want original + one continuation", n)
	}
	first, cont := r.firstSpec(), r.lastSpec()
	if !cont.Resume || first.Model != route.ModelHaiku || first.Effort != "low" ||
		cont.Model != first.Model || cont.Effort != first.Effort {
		t.Errorf("first %s/%s, continuation %s/%s (resume=%v), want haiku/low on both",
			first.Model, first.Effort, cont.Model, cont.Effort, cont.Resume)
	}
}

// ── haiku ──

func TestRouteHaiku(t *testing.T) {
	quietKnobs(t)
	r, db := startPhase(t, "active", "", "", nil)
	sp := r.firstSpec()
	if sp.Model != route.ModelHaiku {
		t.Fatalf("S-tier spawn model = %q, want the full haiku ID %q (and no ErrUnknownModel)", sp.Model, route.ModelHaiku)
	}
	if a := argvOf(sp); !strings.Contains(strings.Join(a, " "), "--model "+route.ModelHaiku+" --effort low") {
		t.Errorf("argv = %q", a[2:])
	}
	row := readRouteRow(t, db)
	if row.tier != route.TierS || row.pickModel != "haiku" || row.usedModel != route.ModelHaiku {
		t.Errorf("row = %+v, want tier S, pick haiku (alias), used %s", row, route.ModelHaiku)
	}
}

// ── off golden ──

// TestRouteOffGolden pins mode=off to the argv phaserun produced BEFORE the
// router existed (captured from eec509c0 with startOnce's fixture): the whole
// argv — prompt included — hashed, plus the flag tail spelled out.
func TestRouteOffGolden(t *testing.T) {
	quietKnobs(t)
	const golden = "ed9dcefdefcbf2ff09a46594153f04bd1ac99768e7449777e792e096eb84f65b"
	wantTail := []string{"--session-id", "uuid-1", "--model", "claude-opus-5-5", "--effort", "high"}
	specs, db, _ := startOnce(t, "off", "", "")
	if len(specs) != 1 {
		t.Fatalf("spawned %d times, want 1", len(specs))
	}
	a := argvOf(specs[0])
	sum := sha256.Sum256([]byte(strings.Join(a, "\x00")))
	if got := hex.EncodeToString(sum[:]); got != golden {
		t.Errorf("off argv drifted from the pre-router golden: sha %s, tail %q", got, a[2:])
	}
	if strings.Join(a[2:], " ") != strings.Join(wantTail, " ") {
		t.Errorf("off flags = %q, want %q", a[2:], wantTail)
	}
	if n := routeRowCount(t, db); n != 0 {
		t.Errorf("off wrote %d route rows", n)
	}
}
