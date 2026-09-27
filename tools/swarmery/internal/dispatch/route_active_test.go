package dispatch

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/route"
)

// The dispatch ladders in active mode (plan phase 4), first non-empty wins:
//
//	model    card model → playbook model: → ROUTE → DefaultModel
//	effort   ROUTE → SWARMERY_DISPATCH_EFFORT → DefaultEffort
//	playbook card playbook → ROUTE → autoProfile
//
// Card fixtures are sized so the default policy lands them on a known tier:
//
//	sTierScope + short prompt ⇒ score 0  ⇒ S  (haiku / low / standard)
//	sTierScope + 5000-byte prompt ⇒ 30   ⇒ M  (sonnet / medium / standard)
const sTierScope = `["README.md"]`

// quietKnobs clears every env knob the dispatch argv depends on, so a test
// asserts the ladder and not the machine it runs on.
func quietKnobs(t *testing.T) {
	t.Helper()
	for _, k := range []string{effortEnv, claudeflags.EffortEnv, permEnv, claudeflags.ModeEnv, route.EnvPolicy} {
		t.Setenv(k, "")
	}
}

// dispatchCard dispatches one card in mode with the given scope and returns the
// spawn specs, the db and the task id. prep runs after insert, before Schedule.
func dispatchCard(t *testing.T, mode, scope string, prep func(db *sql.DB, id int64)) ([]RunSpec, *sql.DB, int64) {
	t.Helper()
	t.Setenv(routeModeEnv, mode)
	db := testDB(t)
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	id := insertTask(t, db, "T-active", taskOpts{fileScope: scope})
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

type routeRow struct {
	mode, wonRung, usedModel, usedEffort, usedPlaybook, tier, pickModel string
	applied                                                             int
}

func readRouteRow(t *testing.T, db *sql.DB) routeRow {
	t.Helper()
	var r routeRow
	if err := db.QueryRow(`SELECT mode, applied, won_rung, used_model, used_effort, used_playbook, tier, pick_model
		FROM route_decisions`).Scan(&r.mode, &r.applied, &r.wonRung, &r.usedModel, &r.usedEffort,
		&r.usedPlaybook, &r.tier, &r.pickModel); err != nil {
		t.Fatalf("read route row: %v", err)
	}
	return r
}

// ── RunSpec.Effort ──

func TestRunSpecEffort(t *testing.T) {
	quietKnobs(t)
	if got := spawnArgs(t, RunSpec{Prompt: "p", SessionUUID: "e1", Effort: "low"}); !strings.Contains(got, "--effort low\n") {
		t.Errorf("a set Effort must reach --effort: %q", got)
	}
	if got := spawnArgs(t, RunSpec{Prompt: "p", SessionUUID: "e2", Effort: " MAX "}); !strings.Contains(got, "--effort max\n") {
		t.Errorf("a set Effort is normalised: %q", got)
	}
	// Empty keeps today's resolution exactly: the default …
	if got := spawnArgs(t, RunSpec{Prompt: "p", SessionUUID: "e3"}); !strings.Contains(got, "--effort "+DefaultEffort+"\n") {
		t.Errorf("empty Effort must fall back to DefaultEffort: %q", got)
	}
	// … and the site knob above it.
	t.Setenv(effortEnv, "xhigh")
	if got := spawnArgs(t, RunSpec{Prompt: "p", SessionUUID: "e4"}); !strings.Contains(got, "--effort xhigh\n") {
		t.Errorf("empty Effort must keep the %s knob: %q", effortEnv, got)
	}
	// An unusable value never reaches argv and never means "no flag": it is
	// demoted to the knob.
	for _, bad := range []string{"bogus", "off"} {
		if got := spawnArgs(t, RunSpec{Prompt: "p", SessionUUID: "e5", Effort: bad}); !strings.Contains(got, "--effort xhigh\n") {
			t.Errorf("Effort=%q must demote to the knob: %q", bad, got)
		}
	}
}

// ── ladders ──

func TestRouteActive_RouteBeatsDefault(t *testing.T) {
	quietKnobs(t)
	specs, db, id := dispatchCard(t, "active", sTierScope, func(db *sql.DB, id int64) {
		setTaskPrompt(t, db, id, strings.Repeat("x", 5000)) // M tier: sonnet / medium / standard
	})
	for i, sp := range specs {
		if sp.Model != "claude-sonnet-5" || sp.Effort != "medium" {
			t.Errorf("stage %d = %s/%s, want the route's claude-sonnet-5/medium", i+1, sp.Model, sp.Effort)
		}
	}
	row := readRouteRow(t, db)
	if row.tier != route.TierM || row.mode != "active" || row.applied != 1 || row.wonRung != route.RungRoute {
		t.Errorf("row = %+v, want tier M, active, applied=1, won_rung=route", row)
	}
	if row.usedModel != specs[0].Model || row.usedEffort != "medium" {
		t.Errorf("row used %s/%s, spawn %s/%s", row.usedModel, row.usedEffort, specs[0].Model, specs[0].Effort)
	}
	// The route's standard beat autoProfile's plan-first (a 5000-byte prompt) and
	// is stamped on the card, so the board shows what ran.
	if len(specs) != 1 || row.usedPlaybook != route.PlaybookStandard {
		t.Errorf("ran %d stages of %q, want 1 stage of the route's standard", len(specs), row.usedPlaybook)
	}
	if got := taskField(t, db, id, "playbook"); got.String != route.PlaybookStandard {
		t.Errorf("stamped playbook = %q, want the route pick standard", got.String)
	}
}

func TestRouteActive_CardModelBeatsRoute(t *testing.T) {
	quietKnobs(t)
	specs, db, _ := dispatchCard(t, "active", sTierScope, func(db *sql.DB, id int64) {
		setTaskModel(t, db, id, "claude-opus-5")
	})
	if specs[0].Model != "claude-opus-5" {
		t.Errorf("model = %q, want the card's claude-opus-5 over the route's haiku", specs[0].Model)
	}
	if specs[0].Effort != "low" {
		t.Errorf("effort = %q, want the route's low (no card effort rung exists)", specs[0].Effort)
	}
	row := readRouteRow(t, db)
	if row.wonRung != route.RungCard || row.applied != 1 || row.usedModel != "claude-opus-5" {
		t.Errorf("row = %+v, want won_rung=card, applied=1 (the effort ran), used_model=claude-opus-5", row)
	}
}

func TestRouteActive_PlaybookModelBeatsRoute(t *testing.T) {
	quietKnobs(t)
	t.Setenv(routeModeEnv, "active")
	db := testDB(t)
	root := t.TempDir()
	writeProjectPlaybook(t, db, root, "standard.md", "---\nname: standard\nmodel: claude-opus-5\n---\n## Stage: one\n{task_prompt}\n")
	r := &stubRunner{run: func(spec RunSpec) (*Run, error) {
		ingestSession(t, db, spec.SessionUUID, "reply "+spec.SessionUUID)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}}
	s := newTestService(t, db, r, &stubWt{})
	s.Playbooks = newRegistry(t)
	insertTask(t, db, "T-pbmodel", taskOpts{fileScope: sTierScope}) // S ⇒ route picks standard + haiku
	s.Schedule()
	if r.count() != 1 {
		t.Fatalf("ran %d stages, want 1", r.count())
	}
	if got := r.spec(0).Model; got != "claude-opus-5" {
		t.Errorf("model = %q, want the recipe's claude-opus-5 over the route's haiku", got)
	}
	row := readRouteRow(t, db)
	if row.wonRung != route.RungPlaybook || row.usedPlaybook != "standard" || row.applied != 1 {
		t.Errorf("row = %+v, want won_rung=playbook on the routed standard, applied=1", row)
	}
}

func TestRouteActive_RouteEffortBeatsEnv(t *testing.T) {
	quietKnobs(t)
	t.Setenv(effortEnv, "max")
	specs, db, _ := dispatchCard(t, "active", sTierScope, nil)
	if specs[0].Effort != "low" {
		t.Errorf("RunSpec.Effort = %q, want the route's low over %s=max", specs[0].Effort, effortEnv)
	}
	if a := argvOf(specs[0]); !strings.Contains(strings.Join(a, " "), "--effort low") {
		t.Errorf("argv = %q, want --effort low", a)
	}
	if row := readRouteRow(t, db); row.usedEffort != "low" {
		t.Errorf("used_effort = %q, want low", row.usedEffort)
	}
	// And in shadow the knob still wins — the route rung is active-only.
	shadow, _, _ := dispatchCard(t, "shadow", sTierScope, nil)
	if shadow[0].Effort != "" || runEffort(shadow[0]) != "max" {
		t.Errorf("shadow effort = %q → %q, want no route value and the knob's max", shadow[0].Effort, runEffort(shadow[0]))
	}
}

func TestRouteActive_CardPlaybookBeatsRoute(t *testing.T) {
	quietKnobs(t)
	specs, db, id := dispatchCard(t, "active", sTierScope, func(db *sql.DB, id int64) {
		setTaskPlaybook(t, db, id, "plan-first") // the route (S) would pick standard
	})
	if len(specs) != 2 {
		t.Fatalf("ran %d stages, want plan-first's 2", len(specs))
	}
	if row := readRouteRow(t, db); row.usedPlaybook != "plan-first" {
		t.Errorf("used_playbook = %q, want the card's plan-first", row.usedPlaybook)
	}
	if got := taskField(t, db, id, "playbook"); got.String != "plan-first" {
		t.Errorf("card playbook = %q, want it untouched", got.String)
	}
}

// Every stage of a chain carries the same model and effort: the route rungs are
// resolved once per card, like the recipe's own knobs.
func TestRouteActive_EveryStageSameModelEffort(t *testing.T) {
	quietKnobs(t)
	specs, _, _ := dispatchCard(t, "active", sTierScope, func(db *sql.DB, id int64) {
		setTaskPlaybook(t, db, id, "plan-first")
	})
	if len(specs) != 2 {
		t.Fatalf("ran %d stages, want 2", len(specs))
	}
	if specs[0].Model != specs[1].Model || specs[0].Effort != specs[1].Effort || specs[0].Effort == "" {
		t.Errorf("stages differ: %s/%s vs %s/%s", specs[0].Model, specs[0].Effort, specs[1].Model, specs[1].Effort)
	}
}

// review-heavy is a human opt-in: the router never yields it, whatever the policy.
func TestRouteActive_RouteNeverYieldsReviewHeavy(t *testing.T) {
	// A hand-built policy that asks for review-heavy on every tier (LoadPolicy
	// would refuse it; Decide must not rely on that).
	p := route.DefaultPolicy()
	for _, k := range []*route.Pick{&p.Tiers.S, &p.Tiers.M, &p.Tiers.L, &p.Tiers.XL} {
		k.Playbook = route.PlaybookReviewHeavy
	}
	for _, sig := range []route.Signals{
		{Surface: route.SurfaceDispatch},
		{Surface: route.SurfaceDispatch, PromptBytes: 9000, FileScope: 40, Areas: 9, RiskPaths: []string{"go.mod"}, Deps: 3},
	} {
		d := route.Decide(sig, p)
		if got := activeRungs(1, d).playbook; got == route.PlaybookReviewHeavy {
			t.Errorf("tier %s yielded review-heavy", d.Tier)
		}
	}
	// And the dispatch rung refuses it even from a decision built by hand.
	if got := activeRungs(1, route.Decision{Model: "sonnet", Effort: "medium", Playbook: "Review-Heavy"}).playbook; got != "" {
		t.Errorf("activeRungs passed review-heavy through as %q", got)
	}
	// End to end, every tier a card can land on stamps standard or plan-first.
	quietKnobs(t)
	for _, prompt := range []string{"", strings.Repeat("x", 5000)} {
		_, db, id := dispatchCard(t, "active", `["go.mod","a/b/c.go","d/e/f.go","g/h/i.go","j/k/l.go","m/n/o.go"]`, func(db *sql.DB, id int64) {
			if prompt != "" {
				setTaskPrompt(t, db, id, prompt)
			}
		})
		if pb := taskField(t, db, id, "playbook").String; pb != "standard" && pb != "plan-first" {
			t.Errorf("stamped playbook = %q", pb)
		}
	}
}

// A bad pick drops only its own rung; the next rung down takes over.
func TestRouteActive_UnusableRungsFallThrough(t *testing.T) {
	r := activeRungs(1, route.Decision{Model: "gpt-9", Effort: "ludicrous", Playbook: "yolo"})
	if r != (routeRungs{}) {
		t.Errorf("rungs = %+v, want all empty", r)
	}
	c := candidate{}
	if m, rung := stageModel(c, resolvedPlaybook{route: r}); m != DefaultModel || rung != route.RungDefault {
		t.Errorf("model = %s/%s, want the default", m, rung)
	}
}

// ── haiku ──

func TestRouteHaiku(t *testing.T) {
	quietKnobs(t)
	specs, db, _ := dispatchCard(t, "active", sTierScope, nil)
	if specs[0].Model != route.ModelHaiku {
		t.Fatalf("S-tier spawn model = %q, want the full haiku ID %q", specs[0].Model, route.ModelHaiku)
	}
	got := spawnArgs(t, specs[0])
	if !strings.Contains(got, "--model "+route.ModelHaiku+" --effort low\n") {
		t.Errorf("argv tail wrong: %q", got[strings.Index(got, " --session-id"):])
	}
	row := readRouteRow(t, db)
	if row.pickModel != "haiku" || row.usedModel != route.ModelHaiku || row.wonRung != route.RungRoute {
		t.Errorf("row = %+v, want pick haiku (alias) → used %s via the route rung", row, route.ModelHaiku)
	}
}

// ── off golden ──

// TestRouteOffGolden pins mode=off to the argv dispatch produced BEFORE the
// router existed (captured from eec509c0 with the same fixtures): the card's
// full argv — prompt included — hashed, plus the flag tail spelled out. off
// must be byte-for-byte pre-router behaviour; this is the rollback guarantee.
func TestRouteOffGolden(t *testing.T) {
	quietKnobs(t)
	const tail = " --setting-sources project,local --permission-mode bypassPermissions --model claude-sonnet-5 --effort medium\n"
	for _, tc := range []struct {
		name     string
		prep     func(db *sql.DB, id int64)
		playbook string
		golden   []string
	}{
		{"short card, standard", nil, "standard",
			[]string{"54cafdf687812231a7dda070b9ca9eecc7f6f4f6f2bda9b4ceb9ec051b992475"}},
		{"long card, plan-first", func(db *sql.DB, id int64) { setTaskPrompt(t, db, id, strings.Repeat("x", 5000)) }, "plan-first",
			[]string{
				"3a00f9d1be62e561efc1283b3943998b6ea57a190b869b5a1b7049520e86a3ed",
				"73e647fe91b7eb87330faf9da877e8b3bf594ed76ee36c0c2116163b712b3046",
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs, db, id := runOnce(t, "off", tc.prep)
			if len(specs) != len(tc.golden) {
				t.Fatalf("ran %d stages, want %d", len(specs), len(tc.golden))
			}
			for i, sp := range specs {
				argv := spawnArgs(t, sp)
				sum := sha256.Sum256([]byte(argv))
				if got := hex.EncodeToString(sum[:]); got != tc.golden[i] {
					t.Errorf("stage %d argv drifted from the pre-router golden\n got sha %s\n argv tail %q", i+1, got, argv[strings.Index(argv, " --session-id"):])
				}
				if !strings.HasSuffix(argv, tail) {
					t.Errorf("stage %d flags = %q, want suffix %q", i+1, argv[strings.Index(argv, " --session-id"):], tail)
				}
				if sp.Effort != "" {
					t.Errorf("stage %d: off put Effort=%q on the spec", i+1, sp.Effort)
				}
			}
			if pb := taskField(t, db, id, "playbook").String; pb != tc.playbook {
				t.Errorf("stamped playbook = %q, want autoProfile's %q", pb, tc.playbook)
			}
			if n := routeRows(t, db); n != 0 {
				t.Errorf("off wrote %d route rows", n)
			}
		})
	}
}
