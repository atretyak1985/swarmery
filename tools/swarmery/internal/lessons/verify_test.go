package lessons

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ── effectiveness math (16.1, 16.6) ──

func runsAt(start time.Time, idx ...float64) []AreaRun {
	out := make([]AreaRun, len(idx))
	for i, v := range idx {
		out[i] = AreaRun{SessionUUID: fmt.Sprint(i), At: start.Add(time.Duration(i) * time.Hour), Index: v}
	}
	return out
}

func TestMedian(t *testing.T) {
	if Median(nil) != nil {
		t.Fatal("median of nothing must be nil")
	}
	if got := *Median([]float64{0.9, 0.1, 0.5}); got != 0.5 {
		t.Fatalf("odd median = %v", got)
	}
	if got := *Median([]float64{0.4, 0.1, 0.2, 0.3}); got != 0.25 {
		t.Fatalf("even median = %v", got)
	}
}

func TestEffectMeasuresTheDropAroundActivation(t *testing.T) {
	act := fixedNow
	pre := runsAt(act.Add(-24*time.Hour), 0.8, 0.7, 0.9, 0.6, 0.8, 0.7)
	post := runsAt(act, 0.3, 0.2, 0.4, 0.3, 0.1)
	before, after, drop, mb, ma := Effect(append(post, pre...), act, 10, 5)
	if len(before) != 6 || len(after) != 5 {
		t.Fatalf("split = %d/%d", len(before), len(after))
	}
	if *mb != 0.75 || *ma != 0.3 || *drop != 0.45 {
		t.Fatalf("medians %v/%v drop %v", *mb, *ma, *drop)
	}
}

func TestEffectTakesTheRunsClosestToActivation(t *testing.T) {
	act := fixedNow
	// 3 old high runs, then 5 recent low ones before activation; window 5 must
	// keep only the recent five.
	pre := runsAt(act.Add(-48*time.Hour), 0.9, 0.9, 0.9, 0.2, 0.2, 0.2, 0.2, 0.2)
	post := runsAt(act, 0.2, 0.2, 0.2, 0.2, 0.2, 0.9, 0.9)
	_, _, drop, mb, ma := Effect(append(pre, post...), act, 5, 5)
	if *mb != 0.2 || *ma != 0.2 || *drop != 0 {
		t.Fatalf("window not honoured: %v %v %v", *mb, *ma, *drop)
	}
}

func TestEffectIsNilWithoutEnoughData(t *testing.T) {
	act := fixedNow
	pre := runsAt(act.Add(-24*time.Hour), 0.8, 0.7, 0.9, 0.6)
	post := runsAt(act, 0.3, 0.2, 0.4, 0.3, 0.1)
	before, after, drop, mb, ma := Effect(append(pre, post...), act, 10, 5)
	if drop != nil || mb != nil || ma != nil {
		t.Fatalf("4 runs before must be 'not enough data', got drop=%v", drop)
	}
	if len(before) != 4 || len(after) != 5 {
		t.Fatalf("counts still reported: %d/%d", len(before), len(after))
	}
}

func TestVerifyConfigFromEnv(t *testing.T) {
	env := map[string]string{EnvWindowRuns: "3", EnvStaleChurn: "0.3", EnvAutoRetireDays: "off"}
	c, warn := VerifyConfigFromEnv(func(k string) string { return env[k] })
	if c.WindowRuns != DefaultWindowRuns || len(warn) != 1 {
		t.Fatalf("window below min must be refused: %+v %v", c, warn)
	}
	if c.StaleChurn != 0.3 || c.AutoRetireDays != 0 {
		t.Fatalf("cfg = %+v", c)
	}
	env = map[string]string{EnvStaleChurn: "2", EnvAutoRetireDays: "x", EnvWindowRuns: "12"}
	c, warn = VerifyConfigFromEnv(func(k string) string { return env[k] })
	if c.StaleChurn != DefaultStaleChurn || c.AutoRetireDays != DefaultAutoRetireDays || c.WindowRuns != 12 || len(warn) != 2 {
		t.Fatalf("cfg = %+v warn %v", c, warn)
	}
	if !strings.Contains(DefaultVerifyConfig().String(), "auto-retire=14d") || !strings.Contains(c.String(), "window=12") {
		t.Fatalf("String = %q", c.String())
	}
}

// ── the verification pass ──

// seedLessonPhase inserts a project, a plan and a phase to hang lessons and
// scored runs on; returns the phase id.
func seedLessonPhase(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	mustExec(t, db, `INSERT OR IGNORE INTO projects(id, path, slug, first_seen) VALUES(1, '/repo', 'p', '2026-01-01T00:00:00Z')`)
	taskID := mustExec(t, db, `INSERT INTO tasks (project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (1, 'Plan', 'goal', 'running', '2026-09-01T00:00:00Z', '2026-09-01T00:00:00Z', 'workspace', 'plan-v')`)
	return mustExec(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, depends_on, run_state)
		VALUES (?, 1, 'Phase', '/ws/plan/phase-v.md', '[]', 'done')`, taskID)
}

func seedVerifyActive(t *testing.T, db *sql.DB, phaseID int64, norm, globs string, activated time.Time) int64 {
	t.Helper()
	at := activated.UTC().Format(time.RFC3339)
	return mustExec(t, db, `INSERT INTO surprise_lessons (source_phase_run, phase_id, seq, title, norm_title, guidance,
		area_globs, status, created_at, updated_at, activated_at) VALUES (?, ?, 1, ?, ?, 'Do the thing.', ?, 'active', ?, ?, ?)`,
		"src-"+norm+at, phaseID, norm, norm, globs, at, at, at)
}

var runSeq int

// seedScored inserts n scored runs in area, one hour apart from start.
func seedScored(t *testing.T, db *sql.DB, phaseID int64, area string, start time.Time, idx ...float64) {
	t.Helper()
	for i, v := range idx {
		runSeq++
		mustExec(t, db, `INSERT INTO phase_surprise (phase_id, session_uuid, surprise_index, detail_json, computed_at)
			VALUES (?, ?, ?, ?, ?)`, phaseID, fmt.Sprintf("run-%d", runSeq), v,
			`{"forecastAreas":["`+area+`"],"actualAreas":["`+area+`"]}`,
			start.Add(time.Duration(i)*time.Hour).UTC().Format(time.RFC3339))
	}
}

func newVerifier(db *sql.DB, now time.Time) *Verifier {
	v := NewVerifier(db, nil, nil, DefaultVerifyConfig())
	v.Now = func() time.Time { return now }
	return v
}

// Acceptance: a lesson with no measured drop after 5 runs appears in the
// retirement queue.
func TestNoDropAfterFiveRunsIsProposedIneffective(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	act := fixedNow.Add(-10 * 24 * time.Hour)
	id := seedVerifyActive(t, db, ph, "ingest lesson", "internal/ingest/**", act)
	seedScored(t, db, ph, "internal/ingest", act.Add(-24*time.Hour), 0.5, 0.5, 0.5, 0.5, 0.5)
	seedScored(t, db, ph, "internal/ingest", act.Add(time.Hour), 0.5, 0.6, 0.5, 0.5, 0.5)
	// a run in another area never counts
	seedScored(t, db, ph, "internal/cost", act.Add(time.Hour), 0.0)

	st, err := newVerifier(db, fixedNow).Run()
	if err != nil {
		t.Fatal(err)
	}
	if st.Measured != 1 || st.Proposed != 1 {
		t.Fatalf("stats = %+v", st)
	}
	q, err := ListProposals(db, DefaultVerifyConfig(), false)
	if err != nil || len(q) != 1 {
		t.Fatalf("queue = %+v, %v", q, err)
	}
	p := q[0]
	if p.LessonID != id || p.Reason != ReasonIneffective || p.State != ProposalOpen || p.AutoRetireAt == nil {
		t.Fatalf("proposal = %+v", p)
	}
	if p.Effectiveness == nil || p.Effectiveness.AfterN != 5 || *p.Effectiveness.MedianDrop != 0 {
		t.Fatalf("effectiveness = %+v", p.Effectiveness)
	}
	l, _ := Get(db, id)
	if l.Status != StatusActive || l.Effectiveness == nil {
		t.Fatalf("a proposal must not retire: %+v", l)
	}

	// idempotent: a second pass proposes nothing new
	st, err = newVerifier(db, fixedNow).Run()
	if err != nil || st.Proposed != 0 || st.AutoRetired != 0 {
		t.Fatalf("second pass = %+v, %v", st, err)
	}
}

func TestADropIsNotProposedAndRanksFirst(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	act := fixedNow.Add(-10 * 24 * time.Hour)
	good := seedVerifyActive(t, db, ph, "good", "internal/ingest/**", act)
	fresh := seedVerifyActive(t, db, ph, "fresh", "internal/ingest/**", fixedNow.Add(-time.Hour))
	seedScored(t, db, ph, "internal/ingest", act.Add(-24*time.Hour), 0.8, 0.8, 0.8, 0.8, 0.8)
	seedScored(t, db, ph, "internal/ingest", act.Add(time.Hour), 0.2, 0.2, 0.2, 0.2, 0.2)
	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	q, _ := ListProposals(db, DefaultVerifyConfig(), false)
	if len(q) != 0 {
		t.Fatalf("a helpful lesson must not be proposed: %+v", q)
	}
	sel, err := Select(db, Scope{Areas: []string{"internal/ingest"}}, 1000)
	if err != nil || len(sel.Lessons) != 2 {
		t.Fatalf("select = %+v, %v", sel, err)
	}
	// the measured lesson outranks the newer, unscored one
	if sel.Lessons[0].ID != good || sel.Lessons[0].Effectiveness == nil || sel.Lessons[1].ID != fresh {
		t.Fatalf("rank = %+v", sel.Lessons)
	}
}

func TestUnusedAndSupersededAreProposed(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	old := seedVerifyActive(t, db, ph, "same identity", "internal/a/**", fixedNow.Add(-90*24*time.Hour))
	newer := seedVerifyActive(t, db, ph, "same identity", "internal/b/**", fixedNow.Add(-24*time.Hour))
	idle := seedVerifyActive(t, db, ph, "idle", "internal/c/**", fixedNow.Add(-90*24*time.Hour))
	used := seedVerifyActive(t, db, ph, "used", "internal/d/**", fixedNow.Add(-90*24*time.Hour))
	mustExec(t, db, `INSERT INTO lesson_uses (session_uuid, run_kind, lesson_id, rank, injected_at)
		VALUES ('s1', 'phaserun', ?, 1, ?)`, used, fixedNow.Add(-2*24*time.Hour).Format(time.RFC3339))
	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	q, _ := ListProposals(db, DefaultVerifyConfig(), false)
	got := map[int64]string{}
	for _, p := range q {
		got[p.LessonID] = p.Reason
	}
	if got[old] != ReasonSuperseded || got[idle] != ReasonUnused || len(got) != 2 {
		t.Fatalf("proposals = %v (newer %d, used %d)", got, newer, used)
	}
}

func TestAutoRetireAfterTheWindowAndOperatorActions(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	a := seedVerifyActive(t, db, ph, "a", "internal/a/**", fixedNow.Add(-90*24*time.Hour))
	b := seedVerifyActive(t, db, ph, "b", "internal/b/**", fixedNow.Add(-90*24*time.Hour))
	c := seedVerifyActive(t, db, ph, "c", "internal/c/**", fixedNow.Add(-90*24*time.Hour))
	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultVerifyConfig()
	q, _ := ListProposals(db, cfg, false)
	if len(q) != 3 {
		t.Fatalf("queue = %+v", q)
	}
	byLesson := map[int64]Proposal{}
	for _, p := range q {
		byLesson[p.LessonID] = p
	}
	// operator: confirm a, keep b
	p, err := ConfirmRetirement(db, cfg, byLesson[a].ID, fixedNow)
	if err != nil || p.State != ProposalConfirmed {
		t.Fatalf("confirm = %+v, %v", p, err)
	}
	if l, _ := Get(db, a); l.Status != StatusRetired || l.RetireReason == nil || *l.RetireReason != ReasonUnused {
		t.Fatalf("confirmed lesson = %+v", l)
	}
	if _, err := ConfirmRetirement(db, cfg, byLesson[a].ID, fixedNow); err != ErrState {
		t.Fatalf("second confirm = %v", err)
	}
	if p, err := KeepLesson(db, cfg, byLesson[b].ID, fixedNow); err != nil || p.State != ProposalKept {
		t.Fatalf("keep = %+v, %v", p, err)
	}
	if _, err := GetProposal(db, cfg, 999); err != ErrNotFound {
		t.Fatalf("missing proposal = %v", err)
	}

	// 13 days later: nothing auto-retires yet; b's reason is on cooldown
	st, err := newVerifier(db, fixedNow.Add(13*24*time.Hour)).Run()
	if err != nil || st.AutoRetired != 0 || st.Proposed != 0 {
		t.Fatalf("day 13 = %+v, %v", st, err)
	}
	// 15 days later: c's unanswered proposal auto-retires with its reason
	st, err = newVerifier(db, fixedNow.Add(15*24*time.Hour)).Run()
	if err != nil || st.AutoRetired != 1 {
		t.Fatalf("day 15 = %+v, %v", st, err)
	}
	if l, _ := Get(db, c); l.Status != StatusRetired || *l.RetireReason != ReasonUnused {
		t.Fatalf("auto-retired lesson = %+v", l)
	}
	all, _ := ListProposals(db, cfg, true)
	states := map[string]int{}
	for _, p := range all {
		states[p.State]++
	}
	if states[ProposalAuto] != 1 || states[ProposalConfirmed] != 1 || states[ProposalKept] != 1 {
		t.Fatalf("states = %v", states)
	}
	if l, _ := Get(db, b); l.Status != StatusActive {
		t.Fatalf("kept lesson must stay active: %+v", l)
	}
	// a third pass is a no-op
	if st, err = newVerifier(db, fixedNow.Add(15*24*time.Hour)).Run(); err != nil || st.AutoRetired+st.Proposed != 0 {
		t.Fatalf("third pass = %+v, %v", st, err)
	}
}

func TestProposalIsWithdrawnWhenTheLessonLeavesActive(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	id := seedVerifyActive(t, db, ph, "x", "internal/x/**", fixedNow.Add(-90*24*time.Hour))
	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := Retire(db, id, "retired by operator", fixedNow); err != nil {
		t.Fatal(err)
	}
	st, err := newVerifier(db, fixedNow).Run()
	if err != nil || st.Withdrawn != 1 {
		t.Fatalf("withdraw = %+v, %v", st, err)
	}
}

// ── churn detection (16.2 stale, 16.6) ──

type fakeGit struct {
	calls []string
	fail  bool
}

func (g *fakeGit) Run(dir string, args ...string) (string, error) {
	line := strings.Join(args, " ")
	g.calls = append(g.calls, dir+": "+line)
	if g.fail {
		return "", fmt.Errorf("git exploded")
	}
	switch {
	case args[0] == "rev-list":
		return "base123\n", nil
	case strings.Contains(line, " base123 HEAD"):
		return "10\t5\tinternal/ingest/a.go\n", nil
	case strings.Contains(line, emptyTree):
		return "20\t0\tinternal/ingest/a.go\n-\t-\tinternal/ingest/logo.png\n", nil
	}
	return "", fmt.Errorf("unexpected git %s", line)
}

func TestAreaChurn(t *testing.T) {
	g := &fakeGit{}
	changed, total, ok := AreaChurn(g, "/repo", []string{"internal/ingest/**", "internal/ingest/*.go"}, "2026-09-01T00:00:00Z")
	if !ok || changed != 15 || total != 20 {
		t.Fatalf("churn = %d/%d ok=%v", changed, total, ok)
	}
	if !strings.Contains(g.calls[0], "--before=2026-09-01T00:00:00Z") || !strings.HasSuffix(g.calls[1], "-- internal/ingest") {
		t.Fatalf("calls = %v", g.calls)
	}
	if _, _, ok := AreaChurn(&fakeGit{fail: true}, "/repo", []string{"a/**"}, "2026-09-01T00:00:00Z"); ok {
		t.Fatal("a git failure must be unknown")
	}
	if _, _, ok := AreaChurn(g, "/repo", []string{"a/**"}, "not a time"); ok {
		t.Fatal("an unreadable activation must be unknown")
	}
}

func TestStaleVerdict(t *testing.T) {
	if v := staleVerdict(15, 20, 0.5); !v.fires || !v.known {
		t.Fatalf("75%% churn must be stale: %+v", v)
	}
	if v := staleVerdict(5, 20, 0.5); v.fires || !v.known {
		t.Fatalf("25%% churn is not stale: %+v", v)
	}
	if v := staleVerdict(5, 0, 0.5); !v.fires {
		t.Fatal("an area deleted since activation is stale")
	}
	if v := staleVerdict(0, 0, 0.5); v.known {
		t.Fatal("nothing to judge must be unknown")
	}
}

func TestStaleLessonIsProposedThroughGit(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	id := seedVerifyActive(t, db, ph, "st", "internal/ingest/**", fixedNow.Add(-5*24*time.Hour))
	v := newVerifier(db, fixedNow)
	v.Git = &fakeGit{}
	v.Resolve = func(projectPath string, cells ...string) (string, error) { return projectPath, nil }
	if _, err := v.Run(); err != nil {
		t.Fatal(err)
	}
	q, _ := ListProposals(db, v.Cfg, false)
	if len(q) != 1 || q[0].LessonID != id || q[0].Reason != ReasonStale || q[0].Evidence["areaLines"] != float64(20) {
		t.Fatalf("queue = %+v", q)
	}
}
