package phasediag

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ── git stub ──

// stubGit is a scripted worktree.Git: it records every invocation and returns a
// canned (output, error) per matched arg-prefix, most-recently-registered first
// (the worktree_test.go idiom). UNSCRIPTED verbs fail, so every git signal a
// test relies on has to be stated explicitly — a missing script degrades the
// diagnosis instead of silently inventing a branch.
type stubGit struct {
	calls   []string
	scripts []gitScript
}

type gitScript struct {
	prefix string
	output string
	err    error
}

func (g *stubGit) Run(dir string, args ...string) (string, error) {
	joined := strings.Join(args, " ")
	g.calls = append(g.calls, joined)
	for i := len(g.scripts) - 1; i >= 0; i-- {
		if strings.HasPrefix(joined, g.scripts[i].prefix) {
			return g.scripts[i].output, g.scripts[i].err
		}
	}
	return "", fmt.Errorf("stubGit: unscripted command %q", joined)
}

func (g *stubGit) on(prefix, output string, err error) *stubGit {
	g.scripts = append(g.scripts, gitScript{prefix: prefix, output: output, err: err})
	return g
}

// onBase pre-scripts the base-branch probe every branch-derived blocker needs.
func newGit(base string) *stubGit {
	g := &stubGit{}
	return g.on("symbolic-ref --short HEAD", base+"\n", nil)
}

// branchExists scripts refs/heads/<branch> as present and `ahead` commits past
// base, with the given subjects (newest first).
func (g *stubGit) branchExists(base, branch string, ahead int, subjects ...string) *stubGit {
	g.on("show-ref --verify --quiet refs/heads/"+branch, "", nil)
	g.on(fmt.Sprintf("rev-list --count %s..refs/heads/%s", base, branch), fmt.Sprintf("%d\n", ahead), nil)
	if ahead > 0 {
		g.on(fmt.Sprintf("log --format=%%s --max-count=20 %s..refs/heads/%s", base, branch),
			strings.Join(subjects, "\n")+"\n", nil)
	}
	return g
}

// branchMissing scripts refs/heads/<branch> as absent.
func (g *stubGit) branchMissing(branch string) *stubGit {
	return g.on("show-ref --verify --quiet refs/heads/"+branch, "", errors.New("exit 1"))
}

// branchList scripts `git branch --list 'swarm/phase-*'`. Real git indents by two
// spaces, marks the current branch "* " and one checked out in another worktree
// "+ " — reproduced here so the parser is exercised against the shape it will
// actually meet. An UNSCRIPTED list (every pre-existing test) errors, which the
// orphan rule degrades to "no orphans".
func (g *stubGit) branchList(names ...string) *stubGit {
	var b strings.Builder
	for i, n := range names {
		switch i {
		case 0:
			b.WriteString("* " + n + "\n")
		case 1:
			b.WriteString("+ " + n + "\n")
		default:
			b.WriteString("  " + n + "\n")
		}
	}
	return g.on("branch --list swarm/phase-*", b.String(), nil)
}

// called reports whether any recorded invocation contains substr — used to assert
// that a rule which should have degraded did not issue git work anyway.
func (g *stubGit) called(substr string) bool {
	for _, c := range g.calls {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// ── db fixture ──

type fx struct {
	db      *sql.DB
	taskID  int64
	planDir string
}

// newFixture builds an in-memory-ish store (temp file, real migrations) with
// one project rooted at /repo/p and one workspace epic task to hang phases off.
func newFixture(t *testing.T) *fx {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "phasediag.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mustExec(t, db, `INSERT INTO projects(id, path, slug, first_seen)
		VALUES(1,'/repo/p','p','2026-01-01T00:00:00Z')`)
	res, err := db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1,'My Epic','goal','running',
		'2026-07-27T00:00:00Z','2026-07-27T00:00:00Z','workspace','2026-07-27-my-epic')`)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	taskID, _ := res.LastInsertId()
	return &fx{db: db, taskID: taskID, planDir: "/ws/plan"}
}

// clearProjectPath blanks projects.path, the "no filesystem path" case.
func (f *fx) clearProjectPath(t *testing.T) {
	t.Helper()
	mustExec(t, f.db, `UPDATE projects SET path='' WHERE id=1`)
}

func (f *fx) addPhase(t *testing.T, seq int, name, dependsOn string, total, done int) int64 {
	t.Helper()
	doc := filepath.Join(f.planDir, fmt.Sprintf("phase-%d-slug.md", seq))
	res, err := f.db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, f.taskID, seq, name, doc, dependsOn, total, done)
	if err != nil {
		t.Fatalf("insert phase %d: %v", seq, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// setRun stamps the run columns. before < 0 leaves run_checkboxes_before NULL
// (the pre-0041 historical shape).
func (f *fx) setRun(t *testing.T, phaseID int64, state, uuid string, before int) {
	t.Helper()
	var b any
	if before >= 0 {
		b = before
	}
	var u any
	if uuid != "" {
		u = uuid
	}
	mustExec(t, f.db, `UPDATE epic_phases
		SET run_state=?, run_session_uuid=?, run_started_at='2026-07-28T10:00:00Z',
		    run_ended_at='2026-07-28T10:20:00Z', run_checkboxes_before=?
		WHERE id=?`, state, u, b, phaseID)
}

// addAssistantTurns seeds a session and its assistant turns (ascending seq —
// the newest is the last one given).
func (f *fx) addAssistantTurns(t *testing.T, uuid string, texts ...string) {
	t.Helper()
	res, err := f.db.Exec(`INSERT INTO sessions (project_id, session_uuid, status, started_at, source)
		VALUES (1, ?, 'completed', '2026-07-28T10:00:00Z', 'jsonl')`, uuid)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	sid, _ := res.LastInsertId()
	for i, text := range texts {
		mustExec(t, f.db, `INSERT INTO turns (session_id, seq, role, started_at, text)
			VALUES (?, ?, 'assistant', ?, ?)`,
			sid, i+1, fmt.Sprintf("2026-07-28T10:%02d:00Z", i), text)
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %s: %v", q, err)
	}
}

// kinds projects a blocker slice to its kinds, in order.
func kinds(bs []Blocker) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Kind)
	}
	return out
}

func blockerOf(t *testing.T, d Diagnosis, kind string) Blocker {
	t.Helper()
	var found []Blocker
	for _, b := range d.Blockers {
		if b.Kind == kind {
			found = append(found, b)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %s blocker, got %d (kinds=%v)", kind, len(found), kinds(d.Blockers))
	}
	return found[0]
}

// ── tests ──

// The live incident: run_state='done', 0 of 7 criteria, pre-0041 NULL baseline.
// The chip said "Run done"; the truth is the run achieved nothing.
func TestDiagnoseNoopOnCleanExitWithoutTicks(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 4, "Phase 4 — Rail", "[]", 7, 0)
	f.setRun(t, id, "done", "", -1) // NULL run_checkboxes_before

	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))
	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.RunOutcome != OutcomeNoop {
		t.Fatalf("RunOutcome = %q, want %q", d.RunOutcome, OutcomeNoop)
	}
	if d.CriteriaBefore != nil || d.CriteriaAfter != 0 || d.CriteriaTotal != 7 {
		t.Fatalf("criteria = %d/%d (before %v), want 0/7 (before nil — unmeasured)",
			d.CriteriaAfter, d.CriteriaTotal, d.CriteriaBefore)
	}
	if d.PhaseID != id || d.Seq != 4 || d.Name != "Phase 4 — Rail" {
		t.Fatalf("identity = %+v", d)
	}
	if d.RunStartedAt == nil || d.RunEndedAt == nil {
		t.Fatalf("want run timestamps, got started=%v ended=%v", d.RunStartedAt, d.RunEndedAt)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("want no blockers, got %v", kinds(d.Blockers))
	}
	if d.AgentMessage != nil {
		t.Fatalf("want nil AgentMessage without a session uuid, got %+v", d.AgentMessage)
	}
}

func TestDiagnosePartialAndFailedOutcomes(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 7, 5)
	f.setRun(t, id, "done", "", 3)
	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.RunOutcome != OutcomePartial {
		t.Fatalf("RunOutcome = %q, want %q", d.RunOutcome, OutcomePartial)
	}

	mustExec(t, f.db, `UPDATE epic_phases SET run_state='failed', run_error='timeout' WHERE id=?`, id)
	d, err = Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.RunOutcome != OutcomeFailed {
		t.Fatalf("RunOutcome = %q, want %q", d.RunOutcome, OutcomeFailed)
	}
	if d.RunError == nil || *d.RunError != "timeout" {
		t.Fatalf("RunError = %v, want \"timeout\"", d.RunError)
	}
}

func TestDiagnoseDepIncomplete(t *testing.T) {
	f := newFixture(t)
	dep := f.addPhase(t, 2, "Phase 2 — Store", "[]", 8, 3)
	id := f.addPhase(t, 5, "Phase 5 — API", "[2]", 7, 0)
	f.setRun(t, id, "done", "", 0)

	git := newGit("dev").
		branchMissing(fmt.Sprintf("swarm/phase-%d", id)).
		branchMissing(fmt.Sprintf("swarm/phase-%d", dep))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindDepIncomplete)
	if !strings.Contains(b.Summary, "3/8") || !strings.Contains(b.Summary, "Phase 2") {
		t.Fatalf("summary %q must name Phase 2 and 3/8", b.Summary)
	}
	if !strings.Contains(b.Detail, "phase-2-slug.md") {
		t.Fatalf("detail %q must name the dependency doc", b.Detail)
	}
}

// A dependency with zero criteria cannot prove completion — it is incomplete. The
// derivation is right, but "is only 0/0 complete" would imply a count that came up
// short; nothing was ever countable. Same Kind, different sentence.
func TestDiagnoseDepWithoutCriteriaIsIncomplete(t *testing.T) {
	f := newFixture(t)
	dep := f.addPhase(t, 2, "Phase 2 — Store", "[]", 0, 0)
	id := f.addPhase(t, 3, "Phase 3", "[2]", 4, 4)

	git := newGit("dev").
		branchMissing(fmt.Sprintf("swarm/phase-%d", id)).
		branchMissing(fmt.Sprintf("swarm/phase-%d", dep))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	// The subject phase is 4/4 with no Completion Report, so the closure gate
	// legitimately adds `unreported` beside this one. Assert the blocker this test
	// is about rather than the whole set: pinning the set here would make every
	// future blocker a failure of an unrelated test.
	if got := kinds(d.Blockers); !slices.Contains(got, KindDepIncomplete) {
		t.Fatalf("kinds = %v, want %s among them", got, KindDepIncomplete)
	}
	b := blockerOf(t, d, KindDepIncomplete)
	if strings.Contains(b.Summary, "0/0") {
		t.Errorf("summary %q reports a 0/0 count; the dependency has no criteria to count", b.Summary)
	}
	if !strings.Contains(b.Summary, "Phase 2") || !strings.Contains(b.Summary, "acceptance-criteria") {
		t.Errorf("summary %q must name Phase 2 and say it has no acceptance-criteria checkboxes", b.Summary)
	}
	if !strings.Contains(b.Detail, "phase-2-slug.md") {
		t.Errorf("detail %q must name the dependency doc", b.Detail)
	}
}

// TestDiagnoseStampedAfterWinsOverLiveCount: run_checkboxes_after (0042) closes the
// measurement interval at exit. Later writers of checkboxes_done — the wsingest
// rescan, TickPhaseChecklist — must not be able to inflate what the run achieved.
func TestDiagnoseStampedAfterWinsOverLiveCount(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 7, 7) // live count has since reached 7/7
	f.setRun(t, id, "done", "", 1)
	mustExec(t, f.db, `UPDATE epic_phases SET run_checkboxes_after=2 WHERE id=?`, id)
	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.CriteriaAfter != 2 {
		t.Errorf("CriteriaAfter = %d, want the stamped 2 — not the live 7", d.CriteriaAfter)
	}
	if d.RunOutcome != OutcomePartial {
		t.Errorf("RunOutcome = %q, want %q (1 → 2 of 7)", d.RunOutcome, OutcomePartial)
	}
}

// TestDiagnoseNullBaselineIsNeverPartial: a NULL run_checkboxes_before means the
// run was never measured. Reading it as 0 would derive the phase's whole ticked
// count as this run's delta and claim a 'partial' success nobody measured.
func TestDiagnoseNullBaselineIsNeverPartial(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 7, 3) // 3 of 7 ticked, provenance unknown
	f.setRun(t, id, "done", "", -1)               // NULL baseline
	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.RunOutcome != OutcomeNoop {
		t.Errorf("RunOutcome = %q, want %q — an unmeasured run cannot claim progress",
			d.RunOutcome, OutcomeNoop)
	}
	if d.CriteriaAfter != 3 || d.CriteriaTotal != 7 {
		t.Errorf("criteria = %d/%d, want 3/7", d.CriteriaAfter, d.CriteriaTotal)
	}

	// The same NULL baseline on a fully ticked phase still reads 'completed' —
	// understating progress, never overstating it.
	mustExec(t, f.db, `UPDATE epic_phases SET checkboxes_done=7 WHERE id=?`, id)
	d, err = Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.RunOutcome != OutcomeCompleted {
		t.Errorf("RunOutcome = %q, want %q on a fully ticked phase", d.RunOutcome, OutcomeCompleted)
	}
}

// TestDiagnoseCriteriaBeforeDistinguishesUnmeasured: the wire format must be able
// to say "not measured". A non-pointer CriteriaBefore serialised a NULL baseline as
// `"criteriaBefore": 0`, so a UI chip rendered "0 → 3" next to a 'noop' outcome —
// a fabricated measurement the Go-side policy exists precisely to refuse.
func TestDiagnoseCriteriaBeforeDistinguishesUnmeasured(t *testing.T) {
	f := newFixture(t)
	git := newGit("dev")

	unmeasured := f.addPhase(t, 1, "Phase 1", "[]", 7, 3)
	f.setRun(t, unmeasured, "done", "", -1) // NULL run_checkboxes_before
	git.branchMissing(fmt.Sprintf("swarm/phase-%d", unmeasured))

	d, err := Diagnose(f.db, git, nil, unmeasured)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.CriteriaBefore != nil {
		t.Errorf("CriteriaBefore = %d, want nil — a NULL baseline is unmeasured, not zero", *d.CriteriaBefore)
	}
	if body, err := json.Marshal(d); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(string(body), `"criteriaBefore":null`) {
		t.Errorf("JSON = %s, want criteriaBefore null", body)
	}

	// A genuinely measured zero baseline still serialises as 0 — the two cases
	// must stay distinguishable on the wire.
	measured := f.addPhase(t, 2, "Phase 2", "[]", 7, 3)
	f.setRun(t, measured, "done", "", 0)
	git.branchMissing(fmt.Sprintf("swarm/phase-%d", measured))

	d, err = Diagnose(f.db, git, nil, measured)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.CriteriaBefore == nil || *d.CriteriaBefore != 0 {
		t.Fatalf("CriteriaBefore = %v, want a measured 0", d.CriteriaBefore)
	}
	if d.RunOutcome != OutcomePartial {
		t.Errorf("RunOutcome = %q, want %q (measured 0 → 3)", d.RunOutcome, OutcomePartial)
	}
}

// The incident's real cause: the dependency IS ticked, but its code sits on an
// unmerged branch, so the executor correctly refused to build on top of it.
func TestDiagnoseDepUnmerged(t *testing.T) {
	f := newFixture(t)
	dep := f.addPhase(t, 1, "Phase 1 — Schema", "[]", 8, 8)
	id := f.addPhase(t, 4, "Phase 4 — Rail", "[1]", 7, 0)
	f.setRun(t, id, "done", "", 0)

	depBranch := fmt.Sprintf("swarm/phase-%d", dep)
	git := newGit("dev").
		branchMissing(fmt.Sprintf("swarm/phase-%d", id)).
		branchExists("dev", depBranch, 2, "feat: store", "test: store")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindDepUnmerged)
	// The modal lands the dependency's branch from this card, so it must name it.
	if b.Branch != depBranch || b.CommitsAhead != 2 || b.Base != "dev" {
		t.Fatalf("dep-unmerged data = %q/%d/%q, want %q/2/dev", b.Branch, b.CommitsAhead, b.Base, depBranch)
	}
	for _, want := range []string{"Phase 1", depBranch, "2 commits", "dev"} {
		if !strings.Contains(b.Summary, want) {
			t.Fatalf("summary %q must contain %q", b.Summary, want)
		}
	}
	// The base is stated as what it actually is — the repo's CURRENT checkout —
	// because that is what baseBranch measures against (matching worktree.Acquire).
	// On a feature branch a dependency merged into dev but not into that branch
	// fires this blocker; without the qualifier the user cannot tell that skew from
	// a genuinely unmerged dependency.
	if !strings.Contains(b.Detail, depBranch) || !strings.Contains(b.Detail, "2 commits") {
		t.Fatalf("detail %q must name the branch and count", b.Detail)
	}
	if !strings.Contains(b.Detail, "measured against the currently checked-out branch dev") {
		t.Fatalf("detail %q must name what it compared against", b.Detail)
	}
}

// Ticked AND merged: nothing to say about the dependency at all.
func TestDiagnoseDepMergedIsSilent(t *testing.T) {
	f := newFixture(t)
	dep := f.addPhase(t, 1, "Phase 1 — Schema", "[]", 8, 8)
	id := f.addPhase(t, 4, "Phase 4 — Rail", "[1]", 7, 2)

	git := newGit("dev").
		branchMissing(fmt.Sprintf("swarm/phase-%d", id)).
		branchExists("dev", fmt.Sprintf("swarm/phase-%d", dep), 0)

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("want no blockers, got %v", kinds(d.Blockers))
	}
}

// A dependency whose branch was never created (already merged and deleted, or
// the work landed straight on base) is equally silent.
func TestDiagnoseDepTickedWithoutBranchIsSilent(t *testing.T) {
	f := newFixture(t)
	dep := f.addPhase(t, 1, "Phase 1", "[]", 3, 3)
	id := f.addPhase(t, 2, "Phase 2", "[1]", 5, 1)

	git := newGit("dev").
		branchMissing(fmt.Sprintf("swarm/phase-%d", id)).
		branchMissing(fmt.Sprintf("swarm/phase-%d", dep))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("want no blockers, got %v", kinds(d.Blockers))
	}
}

func TestDiagnoseOwnBranchStatesAreMutuallyExclusive(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 4, "Phase 4", "[]", 7, 1)
	branch := fmt.Sprintf("swarm/phase-%d", id)

	// Leftover branch, nothing on it.
	leftover := newGit("dev").branchExists("dev", branch, 0)
	d, err := Diagnose(f.db, leftover, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if got := kinds(d.Blockers); len(got) != 1 || got[0] != KindBranchBlocksRetry {
		t.Fatalf("kinds = %v, want [%s]", got, KindBranchBlocksRetry)
	}
	b := blockerOf(t, d, KindBranchBlocksRetry)
	if !strings.Contains(b.Summary, branch) {
		t.Fatalf("summary %q must name the branch", b.Summary)
	}
	if !strings.Contains(b.Detail, "0 commits") ||
		!strings.Contains(b.Detail, "measured against the currently checked-out branch dev") {
		t.Fatalf("detail %q must state 0 commits and the base it compared against", b.Detail)
	}

	// Same branch, three commits on it.
	subjects := []string{"fix: rail width", "feat: rail panel", "test: rail"}
	dirty := newGit("dev").branchExists("dev", branch, 3, subjects...)
	d, err = Diagnose(f.db, dirty, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if got := kinds(d.Blockers); len(got) != 1 || got[0] != KindBranchDirty {
		t.Fatalf("kinds = %v, want [%s]", got, KindBranchDirty)
	}
	b = blockerOf(t, d, KindBranchDirty)
	if !strings.Contains(b.Summary, "3 commits") || !strings.Contains(b.Summary, branch) {
		t.Fatalf("summary %q must name the branch and commit count", b.Summary)
	}
	for _, s := range subjects {
		if !strings.Contains(b.Detail, s) {
			t.Fatalf("detail %q must contain subject %q", b.Detail, s)
		}
	}
	lines := strings.Split(strings.TrimSpace(b.Detail), "\n")
	if lines[0] != subjects[0] {
		t.Fatalf("detail must list newest commit first, got %q", lines[0])
	}
	// The comparison base is appended AFTER the subjects, so the newest-commit-first
	// reading order survives while the count still says what it is relative to.
	if last := lines[len(lines)-1]; last != "measured against the currently checked-out branch dev" {
		t.Fatalf("detail must end by naming the base it compared against, got %q", last)
	}
	// The UI's delete confirmation names what a `branch -D` would destroy off these
	// two fields, so they must carry the facts as data — not leave the client to
	// parse Summary or rebuild "swarm/phase-<id>" on its own.
	if b.Branch != branch || b.CommitsAhead != 3 {
		t.Fatalf("branch/commitsAhead = %q/%d, want %q/3", b.Branch, b.CommitsAhead, branch)
	}
}

func TestDiagnoseNoCriteria(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 6, "Phase 6", "[]", 0, 0)
	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindNoCriteria)
	if !strings.Contains(b.Detail, "phase-6-slug.md") {
		t.Fatalf("detail %q must name the phase doc path", b.Detail)
	}
}

// Detached HEAD (or any symbolic-ref failure): branch-derived blockers are
// omitted rather than guessed, and the diagnosis still answers everything else.
func TestDiagnoseDetachedHeadSkipsBranchBlockers(t *testing.T) {
	f := newFixture(t)
	dep := f.addPhase(t, 1, "Phase 1", "[]", 8, 8)
	id := f.addPhase(t, 2, "Phase 2", "[1]", 0, 0)

	git := &stubGit{}
	git.on("symbolic-ref --short HEAD", "", errors.New("fatal: ref HEAD is not a symbolic ref"))
	git.branchExists("dev", fmt.Sprintf("swarm/phase-%d", id), 3, "feat: x")
	git.branchExists("dev", fmt.Sprintf("swarm/phase-%d", dep), 2, "feat: y")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose must not fail on detached HEAD: %v", err)
	}
	if got := kinds(d.Blockers); len(got) != 1 || got[0] != KindNoCriteria {
		t.Fatalf("kinds = %v, want only [%s]", got, KindNoCriteria)
	}
}

func TestDiagnoseWithoutGitOrProjectPath(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 0, 0)

	d, err := Diagnose(f.db, nil, nil, id)
	if err != nil {
		t.Fatalf("Diagnose with nil git: %v", err)
	}
	if got := kinds(d.Blockers); len(got) != 1 || got[0] != KindNoCriteria {
		t.Fatalf("nil git: kinds = %v, want only [%s]", got, KindNoCriteria)
	}

	f.clearProjectPath(t)
	git := newGit("dev").branchExists("dev", fmt.Sprintf("swarm/phase-%d", id), 4, "feat: x")
	d, err = Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose without project path: %v", err)
	}
	if got := kinds(d.Blockers); len(got) != 1 || got[0] != KindNoCriteria {
		t.Fatalf("no path: kinds = %v, want only [%s]", got, KindNoCriteria)
	}
	if len(git.calls) != 0 {
		t.Fatalf("no project path must mean no git calls, got %v", git.calls)
	}
}

// A git that is broken in every direction must degrade, never fail the request.
func TestDiagnoseGitErrorsDegrade(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 0)

	git := newGit("dev") // show-ref/rev-list unscripted ⇒ error
	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("want no blockers when git cannot answer, got %v", kinds(d.Blockers))
	}

	// rev-list garbage is equally non-fatal.
	git2 := newGit("dev").
		on("show-ref --verify --quiet refs/heads/", "", nil).
		on("rev-list --count", "not-a-number\n", nil)
	d, err = Diagnose(f.db, git2, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("want no blockers on unparseable rev-list, got %v", kinds(d.Blockers))
	}
}

func TestDiagnoseAgentMessage(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 0)
	f.setRun(t, id, "done", "sess-1", 0)
	f.addAssistantTurns(t, "sess-1", "first thought", "I stopped: dependency code is unmerged.")

	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))
	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.AgentMessage == nil {
		t.Fatal("want an AgentMessage")
	}
	if d.AgentMessage.SessionUUID != "sess-1" {
		t.Fatalf("SessionUUID = %q", d.AgentMessage.SessionUUID)
	}
	if d.AgentMessage.Text != "I stopped: dependency code is unmerged." {
		t.Fatalf("want the NEWEST assistant turn, got %q", d.AgentMessage.Text)
	}
	if d.AgentMessage.Truncated {
		t.Fatal("short text must not be marked truncated")
	}
}

// The excerpt is capped in RUNES: a byte-wise cut would split a multi-byte rune
// and render as a replacement char in the modal.
func TestDiagnoseAgentMessageTruncatesOnRuneBoundary(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 0)
	f.setRun(t, id, "done", "sess-2", 0)
	long := strings.Repeat("є", maxAgentText+50) // 2-byte runes
	f.addAssistantTurns(t, "sess-2", long)

	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))
	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.AgentMessage == nil {
		t.Fatal("want an AgentMessage")
	}
	if !d.AgentMessage.Truncated {
		t.Fatal("want Truncated=true")
	}
	if n := len([]rune(d.AgentMessage.Text)); n != maxAgentText {
		t.Fatalf("text = %d runes, want %d", n, maxAgentText)
	}
	if !strings.ContainsRune(d.AgentMessage.Text, 'є') || strings.ContainsRune(d.AgentMessage.Text, '�') {
		t.Fatalf("text was cut mid-rune: %q", d.AgentMessage.Text[:20])
	}
}

func TestDiagnoseAgentMessageMissing(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 0)
	f.setRun(t, id, "done", "sess-nope", 0) // uuid set, nothing ingested
	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if d.AgentMessage != nil {
		t.Fatalf("want nil AgentMessage with no ingested turn, got %+v", d.AgentMessage)
	}
}

func TestDiagnosePhaseNotFound(t *testing.T) {
	f := newFixture(t)
	if _, err := Diagnose(f.db, newGit("dev"), nil, 9999); !errors.Is(err, ErrPhaseNotFound) {
		t.Fatalf("err = %v, want ErrPhaseNotFound", err)
	}
}

// The UI renders blockers top-down as "most actionable first"; the order is a
// contract, not an accident of map iteration.
func TestDiagnoseBlockerOrder(t *testing.T) {
	f := newFixture(t)
	depA := f.addPhase(t, 1, "Phase 1 — Schema", "[]", 8, 8) // ticked but unmerged
	depB := f.addPhase(t, 2, "Phase 2 — Store", "[]", 8, 3)  // incomplete
	id := f.addPhase(t, 3, "Phase 3 — API", "[2,1]", 0, 0)   // no criteria of its own

	git := newGit("dev").
		branchExists("dev", fmt.Sprintf("swarm/phase-%d", depA), 2, "feat: schema").
		branchMissing(fmt.Sprintf("swarm/phase-%d", depB)).
		branchExists("dev", fmt.Sprintf("swarm/phase-%d", id), 3, "wip: api", "wip: api 2", "wip: api 3")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	want := []string{KindDepIncomplete, KindDepUnmerged, KindBranchDirty, KindNoCriteria}
	if got := kinds(d.Blockers); !equal(got, want) {
		t.Fatalf("blocker order = %v, want %v", got, want)
	}
}

// Dependencies are visited in ascending seq order regardless of the order the
// depends_on list happens to carry.
func TestDiagnoseDepsSortedBySeq(t *testing.T) {
	f := newFixture(t)
	d1 := f.addPhase(t, 1, "Phase 1", "[]", 4, 1)
	d2 := f.addPhase(t, 2, "Phase 2", "[]", 4, 2)
	id := f.addPhase(t, 3, "Phase 3", "[2,1]", 4, 0)

	git := newGit("dev").
		branchMissing(fmt.Sprintf("swarm/phase-%d", id)).
		branchMissing(fmt.Sprintf("swarm/phase-%d", d1)).
		branchMissing(fmt.Sprintf("swarm/phase-%d", d2))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(d.Blockers) != 2 {
		t.Fatalf("want 2 dep blockers, got %v", kinds(d.Blockers))
	}
	if !strings.Contains(d.Blockers[0].Summary, "Phase 1") ||
		!strings.Contains(d.Blockers[1].Summary, "Phase 2") {
		t.Fatalf("deps not in ascending seq order: %q then %q",
			d.Blockers[0].Summary, d.Blockers[1].Summary)
	}
}

// Garbage in depends_on must not gate anything (epics.go decodeIntList posture),
// and a dependency seq with no sibling row is simply unknowable, not a blocker.
func TestDiagnoseUnknownAndGarbageDeps(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 3, "Phase 3", "[9]", 4, 1)
	git := newGit("dev").branchMissing(fmt.Sprintf("swarm/phase-%d", id))
	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("unknown dep seq: want no blockers, got %v", kinds(d.Blockers))
	}

	mustExec(t, f.db, `UPDATE epic_phases SET depends_on='not json' WHERE id=?`, id)
	d, err = Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose with garbage depends_on: %v", err)
	}
	if len(d.Blockers) != 0 {
		t.Fatalf("garbage deps: want no blockers, got %v", kinds(d.Blockers))
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ── orphan-branch (phase 8) ──

// The incident phase 8 exists for: the dependency work sits on swarm/phase-697,
// minted when those phases were rows 690/691/697, before a plan rescan deleted and
// re-inserted them with new ids. dep-unmerged correctly stays silent (it correlates
// swarm/phase-<depID>, and no such branch exists), so the deterministic verdict was
// empty while the executor's prose named the branch — the exact inversion this
// package was built to remove.
func TestDiagnoseOrphanBranchWithCommits(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1 — API", "[]", 4, 4)

	git := newGit("dev").
		branchMissing(branchName(id)).
		branchList("swarm/phase-697").
		branchExists("dev", "swarm/phase-697", 2, "feat: plugindrift", "feat: findings")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindOrphanBranch)
	if !strings.Contains(b.Summary, "swarm/phase-697") {
		t.Errorf("summary = %q, want it to name the branch", b.Summary)
	}
	if !strings.Contains(b.Summary, "2 commits") {
		t.Errorf("summary = %q, want the commit count", b.Summary)
	}
	if !strings.Contains(b.Detail, "dev") {
		t.Errorf("detail = %q, want the base it was measured against", b.Detail)
	}
	// The cleanup action names the branch the server proved: an orphan's id matches
	// no row, so the client has no phase to rebuild the name from.
	if b.Branch != "swarm/phase-697" || b.CommitsAhead != 2 {
		t.Errorf("blocker = {%q,%d}, want {swarm/phase-697,2} as DATA", b.Branch, b.CommitsAhead)
	}
}

// An empty orphan is litter the next run's ReclaimEmptyBranch deletes on its own,
// not lost work — reporting it would train the user to ignore the kind.
func TestDiagnoseEmptyOrphanIsSilent(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	git := newGit("dev").
		branchMissing(branchName(id)).
		branchList("swarm/phase-580").
		branchExists("dev", "swarm/phase-580", 0)

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	for _, b := range d.Blockers {
		if b.Kind == KindOrphanBranch {
			t.Errorf("empty orphan reported: %+v", b)
		}
	}
}

// A branch whose id is not in canonical decimal form is not one of ours: worktree
// mints names with strconv.FormatInt, so swarm/phase-007 can only be hand-made. It
// matters because the blocker reports branchName(id), not the listed line — left in,
// this would report swarm/phase-7 (a branch that need not exist) and its cleanup
// button would name a branch the orphan route refuses by construction.
func TestDiagnoseNonCanonicalBranchIsNotOrphaned(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	git := newGit("dev").
		branchMissing(branchName(id)).
		branchList("swarm/phase-007").
		branchExists("dev", "swarm/phase-007", 2, "wip").
		branchExists("dev", "swarm/phase-7", 2, "wip")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	for _, b := range d.Blockers {
		if b.Kind == KindOrphanBranch {
			t.Errorf("non-canonical branch reported as an orphan: %+v", b)
		}
	}
}

// Ids are GLOBAL across epics, so the absence check must be against epic_phases
// entirely. Scoped to one epic, every plan would report every other plan's live
// run branches — noise that buries the one branch that really is lost work.
func TestDiagnoseLivePhaseBranchOfAnotherEpicIsNotOrphaned(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	// A second epic in the same project, with its own phase row and run branch.
	res, err := f.db.Exec(`INSERT INTO tasks (project_id, title, prompt, status, created_at,
		started_at, source, external_id) VALUES (1,'Other Epic','goal','running',
		'2026-07-27T00:00:00Z','2026-07-27T00:00:00Z','workspace','2026-07-27-other-epic')`)
	if err != nil {
		t.Fatal(err)
	}
	otherTask, _ := res.LastInsertId()
	res, err = f.db.Exec(`INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done)
		VALUES (?, 1, 'Other Phase', '/ws/other/phase-1.md', '[]', 3, 3)`, otherTask)
	if err != nil {
		t.Fatal(err)
	}
	otherPhase, _ := res.LastInsertId()

	git := newGit("dev").
		branchMissing(branchName(id)).
		branchList(branchName(otherPhase)).
		branchExists("dev", branchName(otherPhase), 4, "feat: other")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	for _, b := range d.Blockers {
		if b.Kind == KindOrphanBranch {
			t.Errorf("another epic's LIVE run branch reported as orphaned: %+v", b)
		}
	}
	// It must not even have been measured — a rev-list per foreign branch would
	// make the diagnosis cost scale with the whole repo's branch count.
	for _, c := range git.calls {
		if strings.Contains(c, "rev-list") && strings.Contains(c, branchName(otherPhase)) {
			t.Errorf("counted commits for a live row's branch (calls: %v)", git.calls)
		}
	}
}

// A repo littered with old run branches must not flood the modal: five named
// blockers, then one line carrying the count of the rest.
func TestDiagnoseOrphanOverflowIsCapped(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	names := []string{}
	git := newGit("dev").branchMissing(branchName(id))
	for i := 0; i < 7; i++ {
		br := fmt.Sprintf("swarm/phase-%d", 900+i)
		names = append(names, br)
		git = git.branchExists("dev", br, 1, "wip")
	}
	git = git.branchList(names...)

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	var named, overflow int
	for _, b := range d.Blockers {
		if b.Kind != KindOrphanBranch {
			continue
		}
		if b.Branch != "" {
			named++
			continue
		}
		overflow++
		if !strings.Contains(b.Summary, "+2 more") {
			t.Errorf("overflow summary = %q, want it to count the 2 unlisted branches", b.Summary)
		}
	}
	if named != maxOrphans {
		t.Errorf("named orphan blockers = %d, want %d", named, maxOrphans)
	}
	if overflow != 1 {
		t.Errorf("overflow lines = %d, want exactly 1", overflow)
	}
}

// Detached HEAD, a nil git seam and a pathless project all leave base == "", and
// every branch-derived rule here degrades to nothing rather than guessing against
// a hard-coded "main". The orphan rule follows the same contract — and must not
// even issue the branch list, since there would be nothing to measure against.
func TestDiagnoseOrphansNeedABase(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	detached := &stubGit{}
	detached.on("symbolic-ref --short HEAD", "", errors.New("exit 1"))
	detached.on("branch --list swarm/phase-*", "  swarm/phase-697\n", nil)

	d, err := Diagnose(f.db, detached, nil, id)
	if err != nil {
		t.Fatalf("Diagnose (detached): %v", err)
	}
	for _, b := range d.Blockers {
		if b.Kind == KindOrphanBranch {
			t.Errorf("orphan reported with no base: %+v", b)
		}
	}
	if detached.called("branch --list") {
		t.Errorf("listed branches with no base to measure against (calls: %v)", detached.calls)
	}

	// nil git: same contract, and no panic.
	if _, err := Diagnose(f.db, nil, nil, id); err != nil {
		t.Fatalf("Diagnose (nil git): %v", err)
	}

	// pathless project: base is never resolved, so the rule never runs.
	f.clearProjectPath(t)
	d, err = Diagnose(f.db, newGit("dev").branchMissing(branchName(id)), nil, id)
	if err != nil {
		t.Fatalf("Diagnose (pathless): %v", err)
	}
	for _, b := range d.Blockers {
		if b.Kind == KindOrphanBranch {
			t.Errorf("orphan reported for a pathless project: %+v", b)
		}
	}
}

// A git failure anywhere in the orphan path degrades to "no orphans": a diagnosis
// explaining why a run failed must never itself fail because git was unhappy.
func TestDiagnoseOrphanGitErrorDegrades(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	git := newGit("dev").branchMissing(branchName(id))
	git.on("branch --list swarm/phase-*", "fatal: not a git repository", errors.New("exit 128"))

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose = %v, want nil — a git failure must degrade, not fail", err)
	}
	for _, b := range d.Blockers {
		if b.Kind == KindOrphanBranch {
			t.Errorf("orphan reported off a failed list: %+v", b)
		}
	}
}

// A branch whose remainder is not a number is not a run branch of ours, and the
// parser must skip it rather than treating it as id 0.
func TestDiagnoseOrphanIgnoresNonNumericIDs(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 4)

	git := newGit("dev").
		branchMissing(branchName(id)).
		branchList("swarm/phase-wip", "swarm/phase-", "swarm/phase-697").
		branchExists("dev", "swarm/phase-697", 1, "feat: real")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindOrphanBranch)
	if b.Branch != "swarm/phase-697" {
		t.Errorf("blocker.branch = %q, want the only numeric orphan", b.Branch)
	}
}

// Orphan blockers come LAST, after the phase's own: they are a fact about the repo
// rather than about this phase, so they must never crowd out its own blockers.
func TestDiagnoseOrphanIsEmittedLast(t *testing.T) {
	f := newFixture(t)
	depA := f.addPhase(t, 1, "Phase 1 — Schema", "[]", 8, 8) // ticked but unmerged
	depB := f.addPhase(t, 2, "Phase 2 — Store", "[]", 8, 3)  // incomplete
	id := f.addPhase(t, 3, "Phase 3 — API", "[2,1]", 0, 0)   // no criteria of its own

	git := newGit("dev").
		branchExists("dev", branchName(depA), 2, "feat: schema").
		branchMissing(branchName(depB)).
		branchExists("dev", branchName(id), 3, "wip: api", "wip 2", "wip 3").
		branchList("swarm/phase-697").
		branchExists("dev", "swarm/phase-697", 1, "feat: stranded")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	want := []string{KindDepIncomplete, KindDepUnmerged, KindBranchDirty, KindNoCriteria, KindOrphanBranch}
	if got := kinds(d.Blockers); !equal(got, want) {
		t.Fatalf("blocker order = %v, want %v", got, want)
	}
}

// ── own-worktree vs branch-dirty (phase 8 item 4) ──

// ownStub is an OwnCheckout that claims exactly one branch, at one path.
type ownStub struct {
	branch string
	path   string
	calls  int
}

func (o *ownStub) OwnCheckoutOf(repoRoot, branch string) (string, bool) {
	o.calls++
	if branch == o.branch {
		return o.path, true
	}
	return "", false
}

// Commit 8b9fa7b changed what "branch holds commits" means: a crash leftover still
// checked out at the run's OWN worktree path retries fine (Acquire warm-reuses it)
// and cannot be deleted (git refuses `branch -D` on a live checkout → 409). So
// branch-dirty's advice — "merge or delete it before retrying" — told the user to
// do the one thing that fails, about a situation needing no action at all.
func TestDiagnoseOwnWorktreeIsNotBranchDirty(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 1)
	branch := branchName(id)
	git := newGit("dev").branchExists("dev", branch, 3, "wip: a", "wip: b", "wip: c")
	own := &ownStub{branch: branch, path: "/wts/p/phase-" + fmt.Sprint(id)}

	d, err := Diagnose(f.db, git, own, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindOwnWorktree)
	if !strings.Contains(b.Summary, "retrying continues it") {
		t.Errorf("summary = %q, want the non-alarming wording", b.Summary)
	}
	if !strings.Contains(b.Detail, own.path) {
		t.Errorf("detail = %q, want it to name the holding worktree", b.Detail)
	}
	// No delete affordance: delete is exactly what 409s for this state, so the
	// blocker must not carry the branch the delete action keys on.
	if b.Branch != "" || b.CommitsAhead != 0 {
		t.Errorf("blocker carries delete data {%q,%d} for a state where delete 409s",
			b.Branch, b.CommitsAhead)
	}
	for _, x := range d.Blockers {
		if x.Kind == KindBranchDirty {
			t.Errorf("branch-dirty emitted for our own live checkout: %+v", x)
		}
	}
}

// KindBranchDirty is reserved for the genuinely blocking case: commits, and NOT
// our own leftover. Same git facts as the test above — only the ownership answer
// differs — so the split is proven to hinge on that one signal.
func TestDiagnoseBranchDirtyReservedForForeignLeftover(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 1)
	branch := branchName(id)
	git := newGit("dev").branchExists("dev", branch, 3, "wip: a", "wip: b", "wip: c")
	own := &ownStub{branch: "swarm/phase-999999", path: "/wts/p/other"} // claims nothing

	d, err := Diagnose(f.db, git, own, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindBranchDirty)
	if b.Branch != branch || b.CommitsAhead != 3 {
		t.Errorf("blocker = {%q,%d}, want {%q,3} as delete data", b.Branch, b.CommitsAhead, branch)
	}
	for _, x := range d.Blockers {
		if x.Kind == KindOwnWorktree {
			t.Errorf("own-worktree emitted for a foreign leftover: %+v", x)
		}
	}
	if own.calls == 0 {
		t.Error("the ownership seam was never consulted, so the split cannot be working")
	}
}

// A nil seam means "cannot tell", and the diagnosis then reports the BLOCKING
// reading. Over-warning is the safe direction: the opposite default would tell a
// user that a branch which really does block a retry needs no action.
func TestDiagnoseNilOwnCheckoutFallsBackToBranchDirty(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 1)
	branch := branchName(id)
	git := newGit("dev").branchExists("dev", branch, 2, "wip: a", "wip: b")

	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if b := blockerOf(t, d, KindBranchDirty); b.CommitsAhead != 2 {
		t.Errorf("commitsAhead = %d, want 2", b.CommitsAhead)
	}
}

// An EMPTY own-leftover stays branch-blocks-retry: reclaim deletes an empty branch
// even when it is our own checkout, so the "cleaned up automatically" wording is
// still the right answer and the ownership probe must not run for it.
func TestDiagnoseEmptyOwnLeftoverStaysBlocksRetry(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 4, 1)
	branch := branchName(id)
	git := newGit("dev").branchExists("dev", branch, 0)
	own := &ownStub{branch: branch, path: "/wts/p/phase-x"}

	d, err := Diagnose(f.db, git, own, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if _ = blockerOf(t, d, KindBranchBlocksRetry); own.calls != 0 {
		t.Errorf("ownership probed %d times for an empty branch, want 0", own.calls)
	}
}

// An open stop decision is the first thing the operator must act on, and the
// phase's own branch that waits on it must not be reported as "merge or delete":
// deleting throws away the work the answer would let the next run finish.
func TestDiagnoseOpenStopDecisionLeadsAndReframesOwnBranch(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 10, 9)
	branch := fmt.Sprintf("swarm/phase-%d", id)
	guard := `{"stop":{"trigger":"criterion-impossible","reason":"screenshots fail on unmodified main",` +
		`"options":[{"id":"fix-it","label":"fix it"},{"id":"accept","label":"accept"}]}}`
	if _, _, err := store.UpsertStopDecision(f.db, f.taskID, id, "screenshots fail on unmodified main",
		[]string{"fix-it", "accept"}, guard, "2026-07-28T10:21:00Z"); err != nil {
		t.Fatalf("UpsertStopDecision: %v", err)
	}

	git := newGit("main").branchExists("main", branch, 1, "feat: islands")
	d, err := Diagnose(f.db, git, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if got := kinds(d.Blockers); !slices.Equal(got, []string{KindDecisionOpen, KindBranchAwaitsDecision}) {
		t.Fatalf("kinds = %v, want [%s %s]", got, KindDecisionOpen, KindBranchAwaitsDecision)
	}
	dec := blockerOf(t, d, KindDecisionOpen)
	if dec.DecisionID == "" || dec.Trigger != "criterion-impossible" {
		t.Fatalf("decision blocker = %+v, want a decision id and the trigger", dec)
	}
	if dec.Detail != "screenshots fail on unmodified main" {
		t.Fatalf("detail %q must carry the agent's reason", dec.Detail)
	}
	br := blockerOf(t, d, KindBranchAwaitsDecision)
	if br.Branch != branch || br.CommitsAhead != 1 || br.Base != "main" {
		t.Fatalf("branch blocker = %+v, want %s/1 against main", br, branch)
	}

	// Once answered, the decision is gone and the branch is an ordinary leftover.
	mustExec(t, f.db, `UPDATE phase_decisions SET status='decided', answer='accept'`)
	d, err = Diagnose(f.db, newGit("main").branchExists("main", branch, 1, "feat: islands"), nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if got := kinds(d.Blockers); !slices.Equal(got, []string{KindBranchDirty}) {
		t.Fatalf("kinds after answer = %v, want [%s]", got, KindBranchDirty)
	}
	if b := blockerOf(t, d, KindBranchDirty); b.Base != "main" {
		t.Fatalf("branch-dirty base = %q, want main", b.Base)
	}
}

// A stop-source decision raised by the guard check alone carries no `stop` key.
func TestDiagnoseOpenGuardDecisionWithoutStopBlock(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 3, 0)
	if _, _, err := store.UpsertStopDecision(f.db, f.taskID, id, "The run changed protected paths",
		[]string{"allow-add-to-scope", "reject"}, `{"protectedChanged":["a.go"]}`, "2026-07-28T10:21:00Z"); err != nil {
		t.Fatalf("UpsertStopDecision: %v", err)
	}
	d, err := Diagnose(f.db, nil, nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	b := blockerOf(t, d, KindDecisionOpen)
	if b.Trigger != "" || b.Detail != "The run changed protected paths" || !strings.Contains(b.Summary, b.DecisionID) {
		t.Fatalf("guard decision blocker = %+v", b)
	}
	if !slices.Equal(b.ProtectedChanged, []string{"a.go"}) {
		t.Fatalf("decision blocker protectedChanged = %v, want [a.go]", b.ProtectedChanged)
	}
}

// With protected edits on the stopped run's branch, an answer does NOT keep the
// work unless it is the guard's allow (AnswerContinuesBranch). The diagnosis must
// carry the edited files on both the decision and the branch, and the branch's
// summary must not promise that any answer lets a retry continue on it.
func TestDiagnoseOpenStopWithProtectedEditsQualifiesTheBranch(t *testing.T) {
	f := newFixture(t)
	id := f.addPhase(t, 1, "Phase 1", "[]", 10, 9)
	branch := fmt.Sprintf("swarm/phase-%d", id)
	guard := `{"protectedChanged":["scripts/tests/a.sh"],"stop":{"trigger":"criterion-impossible","reason":"r"}}`
	if _, _, err := store.UpsertStopDecision(f.db, f.taskID, id, "q",
		[]string{"fix-it", GuardOptionAllowID}, guard, "2026-07-28T10:21:00Z"); err != nil {
		t.Fatalf("UpsertStopDecision: %v", err)
	}
	d, err := Diagnose(f.db, newGit("main").branchExists("main", branch, 1, "feat: islands"), nil, id)
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	want := []string{"scripts/tests/a.sh"}
	if dec := blockerOf(t, d, KindDecisionOpen); !slices.Equal(dec.ProtectedChanged, want) {
		t.Fatalf("decision protectedChanged = %v, want %v", dec.ProtectedChanged, want)
	}
	br := blockerOf(t, d, KindBranchAwaitsDecision)
	if !slices.Equal(br.ProtectedChanged, want) {
		t.Fatalf("branch protectedChanged = %v, want %v", br.ProtectedChanged, want)
	}
	if strings.Contains(br.Summary, "answer the decision and a retry continues") ||
		!strings.Contains(br.Summary, GuardOptionAllowID) || !strings.Contains(br.Summary, "scripts/tests/a.sh") {
		t.Fatalf("branch summary %q must name the edits and the one answer that keeps them", br.Summary)
	}
}

// After the operator answered a blocked run's stop, the diagnosis tells the
// branch apart the way the run engine will: refusals only (nothing protected on
// the branch) ⇒ a retry continues on it; protected edits the answer did not
// accept ⇒ branch-dirty, naming the files and the decision.
func TestDiagnoseAnsweredStopDecidesTheBranchReading(t *testing.T) {
	for _, c := range []struct {
		name, guard, answer string
		want                string
	}{
		{"refusals only, agent option", `{"guardBlocks":[{"tool":"Bash","path":".swarmery/plan"}],"stop":{"trigger":"criterion-impossible"}}`,
			"fix-mobile-docs-scroll", KindBranchContinues},
		{"protected edit, agent option", `{"protectedChanged":["scripts/tests/a.sh"],"stop":{"trigger":"criterion-impossible"}}`,
			"fix-mobile-docs-scroll", KindBranchDirty},
		{"protected edit, allow", `{"protectedChanged":["scripts/tests/a.sh"],"stop":{"trigger":"criterion-impossible"}}`,
			GuardOptionAllowID, KindBranchContinues},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id := f.addPhase(t, 1, "Phase 1", "[]", 10, 9)
			f.setRun(t, id, "blocked", "", 0)
			branch := fmt.Sprintf("swarm/phase-%d", id)
			if _, _, err := store.UpsertStopDecision(f.db, f.taskID, id, "q", []string{"a"}, c.guard,
				"2026-07-28T10:20:00Z"); err != nil {
				t.Fatal(err)
			}
			mustExec(t, f.db, `UPDATE phase_decisions SET status='decided', answer=?, decided_at='2026-07-28T10:30:00Z'`, c.answer)
			d, err := Diagnose(f.db, newGit("main").branchExists("main", branch, 1, "feat: islands"), nil, id)
			if err != nil {
				t.Fatal(err)
			}
			if got := kinds(d.Blockers); !slices.Equal(got, []string{c.want}) {
				t.Fatalf("kinds = %v, want [%s]", got, c.want)
			}
			b := d.Blockers[0]
			if b.DecisionID == "" || b.Branch != branch {
				t.Fatalf("blocker = %+v, want the decision and the branch as data", b)
			}
			if c.want == KindBranchDirty && !strings.Contains(b.Summary, "scripts/tests/a.sh") {
				t.Fatalf("summary %q must name the unaccepted protected edit", b.Summary)
			}
		})
	}
}
