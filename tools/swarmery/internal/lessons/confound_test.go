package lessons

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phaserun"
)

func fp(f float64) *float64 { return &f }

// ── the verdict and its precedence ──

func TestVerdictPrecedence(t *testing.T) {
	conf := []Confounder{{Kind: ConfoundLesson, ID: "L-2"}}
	cases := []struct {
		name string
		drop *float64
		cs   []Confounder
		want EffectVerdict
	}{
		{"no drop is insufficient", nil, nil, EffectInsufficient},
		{"insufficient beats confounded", nil, conf, EffectInsufficient},
		{"confounded beats helped", fp(0.5), conf, EffectConfounded},
		{"confounded beats hurt", fp(-0.5), conf, EffectConfounded},
		{"confounded beats no_effect", fp(0), conf, EffectConfounded},
		{"a drop at epsilon helped", fp(0.05), nil, EffectHelped},
		{"a big drop helped", fp(0.4), nil, EffectHelped},
		{"a rise at epsilon hurt", fp(-0.05), nil, EffectHurt},
		{"a drop under epsilon is no_effect", fp(0.0499), nil, EffectNoEffect},
		{"a rise under epsilon is no_effect", fp(-0.0499), nil, EffectNoEffect},
		{"zero is no_effect", fp(0), nil, EffectNoEffect},
	}
	for _, c := range cases {
		if got := Verdict(c.drop, c.cs, DefaultEffectEpsilon); got != c.want {
			t.Errorf("%s: Verdict = %s, want %s", c.name, got, c.want)
		}
	}
	for v, scores := range map[EffectVerdict]bool{EffectHelped: true, EffectHurt: true, EffectNoEffect: false,
		EffectConfounded: false, EffectInsufficient: false} {
		if v.Scores() != scores {
			t.Errorf("%s.Scores() = %t", v, !scores)
		}
	}
}

func TestMarshalConfoundersIsSortedAndDeterministic(t *testing.T) {
	if MarshalConfounders(nil) != "[]" {
		t.Fatal("none must marshal as []")
	}
	cs := []Confounder{
		{Kind: ConfoundProposal, ID: "7", At: "2026-09-02T00:00:00Z", Detail: "p"},
		{Kind: ConfoundModelSwitch, ID: "a→b", At: "2026-09-01T00:00:00Z", Detail: "m"},
		{Kind: ConfoundLesson, ID: "L-3", At: "2026-09-01T00:00:00Z", Detail: "l"},
	}
	got := MarshalConfounders(cs)
	want := `[{"kind":"lesson","id":"L-3","at":"2026-09-01T00:00:00Z","detail":"l"},` +
		`{"kind":"model_switch","id":"a→b","at":"2026-09-01T00:00:00Z","detail":"m"},` +
		`{"kind":"proposal","id":"7","at":"2026-09-02T00:00:00Z","detail":"p"}]`
	if got != want {
		t.Fatalf("marshal =\n%s\nwant\n%s", got, want)
	}
	if cs[0].Kind != ConfoundProposal {
		t.Fatal("MarshalConfounders must not reorder its argument")
	}
	if back := unmarshalConfounders(got); len(back) != 3 || back[0].ID != "L-3" {
		t.Fatalf("round trip = %+v", back)
	}
	if back := unmarshalConfounders("not json"); back == nil || len(back) != 0 {
		t.Fatalf("a broken cell must read as none, got %+v", back)
	}
}

// ── the three detectors ──

var win = Window{From: fixedNow.Add(-24 * time.Hour), To: fixedNow.Add(24 * time.Hour)}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func TestLessonConfounder(t *testing.T) {
	self := Active{ID: 1, AreaGlobs: []string{"internal/ingest/**"}, ActivatedAt: ts(fixedNow)}
	others := []Active{
		self, // itself never confounds
		{ID: 2, Title: "Same area", AreaGlobs: []string{"internal/ingest"}, ActivatedAt: ts(fixedNow.Add(time.Hour))},
		{ID: 3, Title: "Other area", AreaGlobs: []string{"internal/cost/**"}, ActivatedAt: ts(fixedNow.Add(time.Hour))},
		{ID: 4, Title: "Too early", AreaGlobs: []string{"internal/ingest/**"}, ActivatedAt: ts(fixedNow.Add(-48 * time.Hour))},
		{ID: 5, Title: "Too late", AreaGlobs: []string{"internal/ingest/**"}, ActivatedAt: ts(fixedNow.Add(48 * time.Hour))},
		{ID: 6, Title: "Narrower", AreaGlobs: []string{"internal/ingest/sub/*.go"}, ActivatedAt: ts(win.From)},
		{ID: 7, Title: "Unparsable", AreaGlobs: []string{"internal/ingest/**"}, ActivatedAt: "yesterday"},
	}
	got := lessonConfounders(self, others, win)
	if len(got) != 2 || got[0].ID != "L-2" || got[1].ID != "L-6" {
		t.Fatalf("lesson confounders = %+v", got)
	}
	c := got[0]
	if c.Kind != ConfoundLesson || c.At != ts(fixedNow.Add(time.Hour)) ||
		!strings.Contains(c.Detail, `"Same area"`) || !strings.Contains(c.Detail, win.String()) {
		t.Fatalf("confounder = %+v", c)
	}
}

func TestProposalConfounder(t *testing.T) {
	self := Active{ID: 1, AreaGlobs: []string{"plugins/core/skills/testing/**"}}
	in := ts(fixedNow)
	ps := []AppliedProposal{
		{ID: 10, Agent: "testing", TargetKind: "skill", Path: "plugins/core/skills/testing/SKILL.md", DecidedAt: in, Lens: "procedure"},
		{ID: 11, Agent: phaserun.RoleExecutor, TargetKind: "agent", Path: "plugins/core/agents/implementation-agent.md", DecidedAt: in},
		{ID: 12, Agent: "ui-developer", TargetKind: "agent", Path: "plugins/core/agents/ui-developer.md", DecidedAt: in},
		{ID: 13, Agent: "testing", TargetKind: "skill", Path: "plugins/core/skills/testing/SKILL.md", DecidedAt: ts(fixedNow.Add(72 * time.Hour))},
		{ID: 14, Agent: phaserun.RoleReviewer, TargetKind: "", Path: "", DecidedAt: ""},
	}
	got := proposalConfounders(self, ps, phaserun.SpawnedRoles(), win)
	if len(got) != 2 || got[0].ID != "10" || got[1].ID != "11" {
		t.Fatalf("proposal confounders = %+v", got)
	}
	if !strings.Contains(got[0].Detail, "target plugins/core/skills/testing/SKILL.md overlaps") ||
		!strings.Contains(got[0].Detail, "skill proposal #10") {
		t.Fatalf("target rule detail = %q", got[0].Detail)
	}
	if !strings.Contains(got[1].Detail, "implementation-agent is a role phase runs spawn") {
		t.Fatalf("role rule detail = %q", got[1].Detail)
	}
	// The stored lens rides in the detail; a pre-0134 row says so.
	if !strings.Contains(got[0].Detail, "for testing (lens procedure)") {
		t.Fatalf("lens missing from detail = %q", got[0].Detail)
	}
	if !strings.Contains(got[1].Detail, "(lens unrecorded)") {
		t.Fatalf("legacy row detail = %q", got[1].Detail)
	}
	if got := proposalConfounders(self, ps, nil, win); len(got) != 1 {
		t.Fatalf("without the role list only the target rule may fire: %+v", got)
	}
}

func modelRuns(start time.Time, models ...string) []AreaRun {
	out := make([]AreaRun, len(models))
	for i, m := range models {
		out[i] = AreaRun{SessionUUID: fmt.Sprint(i), At: start.Add(time.Duration(i) * time.Hour), Model: m}
	}
	return out
}

func TestModelSwitchConfounder(t *testing.T) {
	pre := fixedNow.Add(-10 * time.Hour)
	post := fixedNow
	o5, o55, s5 := "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5"
	cases := []struct {
		name      string
		pre, post []AreaRun
		want      string // confounder id, "" for none
	}{
		{"a clean switch", modelRuns(pre, o5, o5, o5, o5, o5), modelRuns(post, o55, o55, o55, o55, o55), o5 + "→" + o55},
		{"the 1M marker is the same model", modelRuns(pre, o55, o55, o55), modelRuns(post, o55+"[1m]", o55+"[1m]", o55), ""},
		{"a switch at the share", modelRuns(pre, o5, o5, o5, s5, s5), modelRuns(post, o55, o55, o55, s5, s5), o5 + "→" + o55},
		{"routing mixes the after side", modelRuns(pre, o5, o5, o5, o5, o5), modelRuns(post, o55, o55, s5, s5, o5), ""},
		{"routing mixes the before side", modelRuns(pre, o5, o5, s5, s5, o55), modelRuns(post, o55, o55, o55, o55, o55), ""},
		{"unknown models never switch", modelRuns(pre, "", "", ""), modelRuns(post, o55, o55, o55), ""},
		{"unknowns are not counted", modelRuns(pre, o5, "", "", ""), modelRuns(post, o55, "", "", ""), o5 + "→" + o55},
		{"same model", modelRuns(pre, o5, o5, o5), modelRuns(post, o5, o5, o5), ""},
	}
	for _, c := range cases {
		got := modelSwitchConfounder(c.pre, c.post, DefaultModelShare, win)
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%s: want none, got %+v", c.name, got)
			}
			continue
		}
		if len(got) != 1 || got[0].ID != c.want || got[0].Kind != ConfoundModelSwitch {
			t.Errorf("%s: got %+v, want %s", c.name, got, c.want)
			continue
		}
		if !strings.Contains(got[0].Detail, win.String()) {
			t.Errorf("%s: detail lacks the window: %q", c.name, got[0].Detail)
		}
	}
	// at is the first after-run on the new model
	got := modelSwitchConfounder(modelRuns(pre, o5, o5, o5), modelRuns(post, o5, o55, o55, o55), DefaultModelShare, win)
	if len(got) != 1 || got[0].At != ts(post.Add(time.Hour)) || !strings.Contains(got[0].Detail, "3/4 after") {
		t.Fatalf("switch = %+v", got)
	}
}

func TestVerifyConfigEffectKnobs(t *testing.T) {
	c, warn := VerifyConfigFromEnv(func(string) string { return "" })
	if c.Epsilon != 0.05 || c.ModelShare != 0.6 || len(warn) != 0 {
		t.Fatalf("defaults = %+v %v", c, warn)
	}
	env := map[string]string{EnvEffectEpsilon: "0.1", EnvModelShare: "0.75"}
	c, warn = VerifyConfigFromEnv(func(k string) string { return env[k] })
	if c.Epsilon != 0.1 || c.ModelShare != 0.75 || len(warn) != 0 {
		t.Fatalf("overrides = %+v %v", c, warn)
	}
	for _, bad := range []map[string]string{
		{EnvEffectEpsilon: "-0.1"}, {EnvEffectEpsilon: "1"}, {EnvEffectEpsilon: "x"},
		{EnvModelShare: "0.5"}, {EnvModelShare: "1.2"}, {EnvModelShare: "most"},
	} {
		c, warn = VerifyConfigFromEnv(func(k string) string { return bad[k] })
		if c.Epsilon != DefaultEffectEpsilon || c.ModelShare != DefaultModelShare || len(warn) != 1 ||
			!strings.Contains(warn[0], "ignoring") {
			t.Errorf("%v: cfg %+v warn %v", bad, c, warn)
		}
	}
	if s := DefaultVerifyConfig().String(); !strings.Contains(s, "effect epsilon=0.05") || !strings.Contains(s, "model share≥0.60") {
		t.Fatalf("String = %q", s)
	}
	if (VerifyConfig{}).modelShare() != DefaultModelShare {
		t.Fatal("a zero config must fall back to the default share")
	}
}

// ── the retire gate ──

func TestIneffectiveFiresOnlyOnNoEffectAndHurt(t *testing.T) {
	measured := func(v EffectVerdict, drop float64) EffectivenessRow {
		return EffectivenessRow{BeforeN: 5, AfterN: 5, MedianBefore: fp(0.5), MedianAfter: fp(0.5 - drop),
			MedianDrop: fp(drop), Verdict: v}
	}
	cases := []struct {
		name         string
		eff          EffectivenessRow
		fires, known bool
	}{
		{"helped is definitely not", measured(EffectHelped, 0.3), false, true},
		{"no_effect fires", measured(EffectNoEffect, 0.01), true, true},
		{"hurt fires", measured(EffectHurt, -0.2), true, true},
		{"confounded is unknown", measured(EffectConfounded, -0.3), false, false},
		{"confounded no-drop is unknown", measured(EffectConfounded, 0), false, false},
		{"insufficient is unknown", EffectivenessRow{BeforeN: 2, AfterN: 5, Verdict: EffectInsufficient}, false, false},
	}
	for _, c := range cases {
		vd := ineffective(c.eff)
		if vd.fires != c.fires || vd.known != c.known {
			t.Errorf("%s: fires=%t known=%t", c.name, vd.fires, vd.known)
		}
	}
	if vd := ineffective(measured(EffectHurt, -0.2)); !strings.Contains(vd.detail, "more surprising") {
		t.Fatalf("hurt detail = %q", vd.detail)
	}
}

// ── the whole pass against the database ──

// seedScoredModel inserts scored runs with a session row carrying model.
func seedScoredModel(t *testing.T, db *sql.DB, phaseID int64, area, model string, start time.Time, idx ...float64) {
	t.Helper()
	for i, v := range idx {
		runSeq++
		uuid := fmt.Sprintf("mrun-%d", runSeq)
		at := start.Add(time.Duration(i) * time.Hour).UTC().Format(time.RFC3339)
		mustExec(t, db, `INSERT INTO sessions (project_id, session_uuid, model, started_at) VALUES (1, ?, ?, ?)`, uuid, model, at)
		mustExec(t, db, `INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, detail_json, computed_at)
			VALUES (?, ?, ?, ?, ?)`, phaseID, uuid, v, `{"forecastAreas":["`+area+`"],"actualAreas":["`+area+`"]}`, at)
	}
}

func seedApplied(t *testing.T, db *sql.DB, agent, kind, agentPath, targetPath string, decided time.Time) int64 {
	t.Helper()
	return mustExec(t, db, `INSERT INTO agent_change_proposals (agent, agent_path, target_kind, target_path, base_sha256,
		diff, rationale, status, created_at, decided_at) VALUES (?, ?, ?, ?, 'sha', 'diff', 'why', 'applied', ?, ?)`,
		agent, agentPath, kind, targetPath, ts(decided.Add(-time.Hour)), ts(decided))
}

func storedRow(t *testing.T, db *sql.DB, id int64) EffectivenessRow {
	t.Helper()
	rows, err := LoadEffectiveness(db, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := rows[id]
	if !ok {
		t.Fatalf("no effectiveness row for %s", Ref(id))
	}
	return r
}

func TestPassStoresVerdictsAndConfounders(t *testing.T) {
	act := fixedNow.Add(-10 * 24 * time.Hour)
	type world func(t *testing.T, db *sql.DB, ph int64)
	sameModel := func(t *testing.T, db *sql.DB, ph int64, before, after []float64) {
		seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(-24*time.Hour), before...)
		seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(time.Hour), after...)
	}
	high, low := []float64{0.8, 0.8, 0.8, 0.8, 0.8}, []float64{0.2, 0.2, 0.2, 0.2, 0.2}
	cases := []struct {
		name     string
		seed     world
		verdict  EffectVerdict
		kinds    []string
		proposed bool
	}{
		{"helped", func(t *testing.T, db *sql.DB, ph int64) { sameModel(t, db, ph, high, low) }, EffectHelped, nil, false},
		{"hurt", func(t *testing.T, db *sql.DB, ph int64) { sameModel(t, db, ph, low, high) }, EffectHurt, nil, true},
		{"no_effect", func(t *testing.T, db *sql.DB, ph int64) {
			sameModel(t, db, ph, []float64{0.5, 0.5, 0.5, 0.5, 0.5}, []float64{0.48, 0.48, 0.48, 0.48, 0.48})
		}, EffectNoEffect, nil, true},
		{"insufficient", func(t *testing.T, db *sql.DB, ph int64) { sameModel(t, db, ph, high[:4], high) }, EffectInsufficient, nil, false},
		{"confounded by a lesson", func(t *testing.T, db *sql.DB, ph int64) {
			sameModel(t, db, ph, low, high)
			seedVerifyActive(t, db, ph, "other", "internal/ingest", act.Add(3*time.Hour))
		}, EffectConfounded, []string{ConfoundLesson}, false},
		{"confounded by a proposal on a spawned role", func(t *testing.T, db *sql.DB, ph int64) {
			sameModel(t, db, ph, low, high)
			seedApplied(t, db, phaserun.RoleVerifier, "agent", "plugins/core/agents/verification-agent.md", "", act.Add(2*time.Hour))
		}, EffectConfounded, []string{ConfoundProposal}, false},
		{"a proposal elsewhere does not confound", func(t *testing.T, db *sql.DB, ph int64) {
			sameModel(t, db, ph, high, low)
			seedApplied(t, db, "ui-developer", "agent", "plugins/core/agents/ui-developer.md", "", act.Add(2*time.Hour))
		}, EffectHelped, nil, false},
		{"confounded by a model switch", func(t *testing.T, db *sql.DB, ph int64) {
			seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5", act.Add(-24*time.Hour), low...)
			seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(time.Hour), high...)
		}, EffectConfounded, []string{ConfoundModelSwitch}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openDB(t)
			ph := seedLessonPhase(t, db)
			id := seedVerifyActive(t, db, ph, "measured", "internal/ingest/**", act)
			c.seed(t, db, ph)
			if _, err := newVerifier(db, fixedNow).Run(); err != nil {
				t.Fatal(err)
			}
			r := storedRow(t, db, id)
			if r.Verdict != c.verdict {
				t.Fatalf("verdict = %s, want %s (row %+v)", r.Verdict, c.verdict, r)
			}
			var kinds []string
			for _, cf := range r.Confounders {
				kinds = append(kinds, cf.Kind)
			}
			if strings.Join(kinds, ",") != strings.Join(c.kinds, ",") {
				t.Fatalf("confounders = %+v, want kinds %v", r.Confounders, c.kinds)
			}
			var cell string
			if err := db.QueryRow(`SELECT confounders_json FROM lesson_effectiveness WHERE lesson_id = ?`, id).Scan(&cell); err != nil {
				t.Fatal(err)
			}
			if cell != MarshalConfounders(r.Confounders) {
				t.Fatalf("stored cell %s is not the canonical marshal", cell)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM lesson_retirements WHERE lesson_id = ? AND reason = ?`,
				id, ReasonIneffective).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if (n > 0) != c.proposed {
				t.Fatalf("ineffective proposals = %d, want proposed=%t", n, c.proposed)
			}
			scores, err := StoredEffectiveness(db, []int64{id})
			if err != nil {
				t.Fatal(err)
			}
			if _, scored := scores[id]; scored != c.verdict.Scores() {
				t.Fatalf("scored = %t for verdict %s", scored, c.verdict)
			}
		})
	}
}

// A confounded verdict leaves an already-open ineffective proposal alone: it is
// unknown, not "definitely helped".
func TestConfoundedNeitherProposesNorWithdraws(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	act := fixedNow.Add(-10 * 24 * time.Hour)
	id := seedVerifyActive(t, db, ph, "measured", "internal/ingest/**", act)
	seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(-24*time.Hour), 0.5, 0.5, 0.5, 0.5, 0.5)
	seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(time.Hour), 0.5, 0.5, 0.5, 0.5, 0.5)
	if st, err := newVerifier(db, fixedNow).Run(); err != nil || st.Proposed != 1 {
		t.Fatalf("no_effect pass = %+v, %v", st, err)
	}
	seedVerifyActive(t, db, ph, "other", "internal/ingest", act.Add(2*time.Hour))
	st, err := newVerifier(db, fixedNow).Run()
	if err != nil || st.Proposed != 0 || st.Withdrawn != 0 {
		t.Fatalf("confounded pass = %+v, %v", st, err)
	}
	if r := storedRow(t, db, id); r.Verdict != EffectConfounded {
		t.Fatalf("verdict = %s", r.Verdict)
	}
	q, _ := ListProposals(db, DefaultVerifyConfig(), false)
	if len(q) != 1 || q[0].LessonID != id || q[0].Effectiveness == nil || q[0].Effectiveness.Verdict != EffectConfounded {
		t.Fatalf("queue = %+v", q)
	}
}

// ── injection ranking ──

// A confounded lesson ranks as unmeasured — by recency, below a scored one —
// and no lesson is dropped from the selection by its verdict.
func TestConfoundedLessonRanksAsUnmeasured(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	act := fixedNow.Add(-10 * 24 * time.Hour)
	// confounded: a big drop, but an applied executor rewrite landed inside its window
	confounded := seedVerifyActive(t, db, ph, "confounded", "internal/ingest/**", act)
	seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(-24*time.Hour), 0.9, 0.9, 0.9, 0.9, 0.9)
	seedScoredModel(t, db, ph, "internal/ingest", "claude-opus-5-5", act.Add(time.Hour), 0.1, 0.1, 0.1, 0.1, 0.1)
	seedApplied(t, db, phaserun.RoleExecutor, "agent", "plugins/core/agents/implementation-agent.md", "", act.Add(2*time.Hour))
	// helped: a smaller, clean drop in another area
	act2 := fixedNow.Add(-5 * 24 * time.Hour)
	helped := seedVerifyActive(t, db, ph, "helped", "internal/cost/**", act2)
	seedScoredModel(t, db, ph, "internal/cost", "claude-opus-5-5", act2.Add(-24*time.Hour), 0.6, 0.6, 0.6, 0.6, 0.6)
	seedScoredModel(t, db, ph, "internal/cost", "claude-opus-5-5", act2.Add(time.Hour), 0.4, 0.4, 0.4, 0.4, 0.4)
	// hurt and no_effect lessons are still selected; an unmeasured newer one ranks by recency
	hurtAct := fixedNow.Add(-3 * 24 * time.Hour)
	hurt := seedVerifyActive(t, db, ph, "hurt", "internal/store/**", hurtAct)
	seedScoredModel(t, db, ph, "internal/store", "claude-opus-5-5", hurtAct.Add(-24*time.Hour), 0.2, 0.2, 0.2, 0.2, 0.2)
	seedScoredModel(t, db, ph, "internal/store", "claude-opus-5-5", hurtAct.Add(time.Hour), 0.6, 0.6, 0.6, 0.6, 0.6)
	fresh := seedVerifyActive(t, db, ph, "fresh", "internal/ingest/**", fixedNow.Add(-time.Hour))

	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]EffectVerdict{confounded: EffectConfounded, helped: EffectHelped, hurt: EffectHurt,
		fresh: EffectInsufficient} {
		if r := storedRow(t, db, id); r.Verdict != want {
			t.Fatalf("%s verdict = %s, want %s", Ref(id), r.Verdict, want)
		}
	}
	sel, err := Select(db, Scope{Areas: []string{"internal/ingest", "internal/cost", "internal/store"}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var order []int64
	for _, l := range sel.Lessons {
		order = append(order, l.ID)
	}
	// scored first (helped 0.2 > hurt −0.4), then unscored by recency: fresh, confounded
	want := []int64{helped, hurt, fresh, confounded}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("rank = %v, want %v", order, want)
	}
	if sel.Lessons[3].Effectiveness != nil {
		t.Fatalf("a confounded lesson must be unscored: %+v", sel.Lessons[3])
	}
}

// The loader reads the stored lens off agent_change_proposals, so the detail
// a pass writes names it.
func TestLoadConfoundSourceReadsLens(t *testing.T) {
	db := openDB(t)
	id := seedApplied(t, db, phaserun.RoleExecutor, "agent", "plugins/core/agents/implementation-agent.md", "", fixedNow)
	mustExec(t, db, `UPDATE agent_change_proposals SET lens = 'mixed' WHERE id = ?`, id)
	src, err := loadConfoundSource(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(src.proposals) != 1 || src.proposals[0].Lens != "mixed" {
		t.Fatalf("proposals = %+v", src.proposals)
	}
	w := Window{From: fixedNow.Add(-time.Hour), To: fixedNow.Add(time.Hour)}
	got := proposalConfounders(Active{ID: 1}, src.proposals, src.roles, w)
	if len(got) != 1 || !strings.Contains(got[0].Detail, "(lens mixed)") {
		t.Fatalf("confounders = %+v", got)
	}
}
