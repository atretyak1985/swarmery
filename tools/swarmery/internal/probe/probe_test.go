package probe

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

const (
	target     = "plugins/core/agents/demo.md"
	baseAgent  = "# demo\n\nAlways answer in one line.\n"
	variantAdd = "Never skip the exit code.\n"
)

// demoDiff appends one line to the demo agent.
const demoDiff = `--- a/plugins/core/agents/demo.md
+++ b/plugins/core/agents/demo.md
@@ -1,3 +1,4 @@
 # demo

 Always answer in one line.
+Never skip the exit code.
`

// demoSuite has three cases for demo.md and one for another agent, which the
// probe must leave out.
const demoSuite = `- description: demo answers in one line
  providers: [opus-tier]
  vars:
    system: file://../../plugins/core/agents/demo.md
    task: say hi
  assert:
    - type: contains
      value: hi
- description: demo reports the exit code
  vars:
    system: file://../../plugins/core/agents/demo.md
    task: run it
  assert:
    - type: regex
      value: "exit"
- description: other agent case
  vars:
    system: file://../../plugins/core/agents/other.md
    task: nope
- description: demo stays short
  vars:
    system: file://../../plugins/core/agents/demo.md
    task: be brief
`

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// fakeSource writes a minimal repo tree; files maps repo-relative path →
// content, and nil content skips the default.
type fakeSource struct {
	files map[string]string
	calls int
}

func (f *fakeSource) Export(_ context.Context, dst string, paths []string) error {
	f.calls++
	files := map[string]string{
		target:                         baseAgent,
		"plugins/core/agents/other.md": "# other\n",
		"evals/agents/demo.yaml":       demoSuite,
		"evals/package.json":           `{"devDependencies":{"promptfoo":"0.125.1"}}`,
	}
	for k, v := range f.files {
		if v == "" {
			delete(files, k)
			continue
		}
		files[k] = v
	}
	for rel, body := range files {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

type suiteCall struct {
	side   string
	caseID string
}

// fakeSuite answers per (side, case) and proves the variant tree carries the
// patch while the base tree does not.
type fakeSuite struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []suiteCall
	pass   map[string]bool // key "side|caseID"; missing → true
	usd    float64
	failOn string // "side|caseID" → return an error
}

func (f *fakeSuite) RunCase(_ context.Context, tree string, c Case) (CaseRun, error) {
	side := filepath.Base(tree)
	f.mu.Lock()
	f.calls = append(f.calls, suiteCall{side, c.ID})
	f.mu.Unlock()
	body, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(target)))
	if err != nil {
		f.t.Fatalf("%s tree has no target: %v", side, err)
	}
	patched := strings.Contains(string(body), variantAdd)
	if patched != (side == "variant") {
		f.t.Fatalf("%s tree patched=%v", side, patched)
	}
	key := side + "|" + c.ID
	if key == f.failOn {
		return CaseRun{}, errors.New("promptfoo exploded")
	}
	pass, ok := f.pass[key]
	if !ok {
		pass = true
	}
	return CaseRun{HardPass: pass, Task: "task for " + c.ID, Output: side + "-output",
		USD: f.usd, SoftTotal: 1, SoftPassed: 1}, nil
}

type fakeJudge struct {
	mu      sync.Mutex
	prompts []string
	answer  string
	usd     float64
	err     error
}

func (f *fakeJudge) Judge(_ context.Context, prompt string) (string, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, prompt)
	return f.answer, f.usd, f.err
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// agentSeq keeps agent keys distinct: the one-open-proposal index (0022/0074)
// would refuse a second open row for the same agent.
var agentSeq atomic.Int64

func insertProposal(t *testing.T, db *sql.DB, status, kind, agentPath, targetPath, diff string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO agent_change_proposals
		(agent, agent_path, target_kind, target_path, base_sha256, diff, rationale, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'why', ?, '2026-11-03T00:00:00.000Z')`,
		fmt.Sprintf("demo-%d", agentSeq.Add(1)), agentPath, kind, targetPath, sha(baseAgent), diff, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newService(t *testing.T, db *sql.DB) (*Service, *fakeSource, *fakeSuite, *fakeJudge) {
	src := &fakeSource{}
	suite := &fakeSuite{t: t, pass: map[string]bool{}, usd: 0.01}
	judge := &fakeJudge{answer: "A is more direct.\nCHOICE: A", usd: 0.02}
	s := &Service{DB: db, Source: src, Suite: suite, Judge: judge, MaxUSD: 100,
		Flip: func() bool { return true }}
	return s, src, suite, judge
}

func mustRows(t *testing.T, db *sql.DB, id int64) []Row {
	t.Helper()
	rows, err := ListRows(db, id)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// proposalSnapshot is every column of the row, so "unchanged" means unchanged.
func proposalSnapshot(t *testing.T, db *sql.DB, id int64) string {
	t.Helper()
	var status, decided, errStr, pr, outcome sql.NullString
	if err := db.QueryRow(`SELECT status, decided_at, error, pr_url, outcome FROM agent_change_proposals WHERE id = ?`, id).
		Scan(&status, &decided, &errStr, &pr, &outcome); err != nil {
		t.Fatal(err)
	}
	return strings.Join([]string{status.String, decided.String, errStr.String, pr.String, outcome.String}, "|")
}

func TestRunStoresPerCaseDeterministicResults(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, suite, judge := newService(t, db)
	suite.pass["variant|demo reports the exit code"] = false

	p, err := s.LoadProposal(id)
	if err != nil {
		t.Fatal(err)
	}
	usd, err := s.Run(context.Background(), "job-1", p)
	if err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, db, id)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (the other-agent case is excluded), got %d: %+v", len(rows), rows)
	}
	for _, c := range suite.calls {
		if c.caseID == "other agent case" {
			t.Fatal("ran a case that does not load the target")
		}
	}
	if len(suite.calls) != 6 {
		t.Errorf("want base+variant per case = 6 runs, got %d", len(suite.calls))
	}
	byTask := map[string]Row{}
	for _, r := range rows {
		byTask[r.TaskID] = r
		if r.JobID != "job-1" || r.Source != SourcePromptfoo {
			t.Errorf("row %+v: job/source", r)
		}
	}
	split := byTask["demo reports the exit code"]
	if *split.BasePass != true || *split.VariantPass != false || split.BlindChoice != ChoiceSkipped {
		t.Errorf("split case: %+v", split)
	}
	tie := byTask["demo answers in one line"]
	if !*tie.BasePass || !*tie.VariantPass || tie.BlindChoice != ChoiceBase {
		t.Errorf("tie case (flip puts base at A, judge picks A): %+v", tie)
	}
	if !strings.HasPrefix(tie.Reason, "[A=base · B=variant] A is more direct.") {
		t.Errorf("reason must carry the A/B key: %q", tie.Reason)
	}
	if !strings.Contains(tie.Reason, "llm-rubric (advisory): base 1/1 · variant 1/1") {
		t.Errorf("rubric results must be recorded: %q", tie.Reason)
	}
	if len(judge.prompts) != 2 {
		t.Errorf("judge must run only on the 2 ties, ran %d", len(judge.prompts))
	}
	if want := 6*0.01 + 2*0.02; usd < want-1e-9 || usd > want+1e-9 {
		t.Errorf("usd = %v, want %v", usd, want)
	}
	if tie.USD < 0.04-1e-9 {
		t.Errorf("tie row usd = %v, want both runs + judge", tie.USD)
	}
}

func TestJudgeVerdictMapsBackThroughTheShuffle(t *testing.T) {
	for _, tc := range []struct {
		baseAtA bool
		answer  string
		want    string
	}{
		{true, "CHOICE: A", ChoiceBase},
		{true, "CHOICE: B", ChoiceVariant},
		{false, "CHOICE: A", ChoiceVariant},
		{false, "CHOICE: B", ChoiceBase},
		{false, "CHOICE: TIE", ChoiceTie},
		{false, "no verdict here", ChoiceSkipped},
	} {
		db := openDB(t)
		id := insertProposal(t, db, "approved", "agent", target, "", demoDiff)
		s, _, _, judge := newService(t, db)
		s.Flip = func() bool { return tc.baseAtA }
		judge.answer = tc.answer
		p, _ := s.LoadProposal(id)
		if _, err := s.Run(context.Background(), "j", p); err != nil {
			t.Fatal(err)
		}
		r := mustRows(t, db, id)[0]
		if r.BlindChoice != tc.want {
			t.Errorf("baseAtA=%v answer=%q: got %s want %s", tc.baseAtA, tc.answer, r.BlindChoice, tc.want)
		}
		// The prompt carries the outputs in the flipped order.
		pr := judge.prompts[0]
		a, b := strings.Index(pr, "base-output"), strings.Index(pr, "variant-output")
		if (a < b) != tc.baseAtA {
			t.Errorf("baseAtA=%v but base output at %d, variant at %d", tc.baseAtA, a, b)
		}
	}
}

func TestJudgeFailureIsASkippedRowNotAFailedJob(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, _, judge := newService(t, db)
	judge.err = errors.New("quota")
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	for _, r := range mustRows(t, db, id) {
		if r.BlindChoice != ChoiceSkipped || !strings.Contains(r.Reason, "blind judge failed: quota") {
			t.Errorf("row %+v", r)
		}
	}
}

func TestCostCapStopsTheJobAndRecordsCapped(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, suite, judge := newService(t, db)
	s.MaxUSD = 0.5
	suite.usd = 0.3 // one case = 0.6 > cap

	p, _ := s.LoadProposal(id)
	usd, err := s.Run(context.Background(), "j", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.calls) != 2 {
		t.Errorf("the cap must stop the job after the first case: %d runs", len(suite.calls))
	}
	if len(judge.prompts) != 0 {
		t.Error("judge ran past the cap")
	}
	rows := mustRows(t, db, id)
	if len(rows) != 3 {
		t.Fatalf("every case still gets a row: %d", len(rows))
	}
	for i, r := range rows {
		if r.BlindChoice != ChoiceCapped {
			t.Errorf("row %d: %+v", i, r)
		}
	}
	if rows[0].BasePass == nil || rows[1].BasePass != nil || rows[2].VariantPass != nil {
		t.Errorf("the run case keeps its results; the unrun ones have none: %+v", rows)
	}
	if !strings.Contains(rows[1].Reason, MaxUSDEnv) {
		t.Errorf("capped reason should name the knob: %q", rows[1].Reason)
	}
	if usd != 0.6 {
		t.Errorf("usd = %v", usd)
	}
}

func TestCapReachedBetweenCases(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, suite, judge := newService(t, db)
	s.MaxUSD = 0.1
	suite.usd = 0.02
	judge.usd = 0.06 // case 1: 0.04 + 0.06 = 0.10 → cap hit before case 2
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, db, id)
	if rows[0].BlindChoice != ChoiceBase || rows[1].BlindChoice != ChoiceCapped || rows[2].BlindChoice != ChoiceCapped {
		t.Errorf("rows: %+v", rows)
	}
}

func TestProbeNeverChangesTheProposal(t *testing.T) {
	for _, status := range []string{"proposed", "approved"} {
		db := openDB(t)
		id := insertProposal(t, db, status, "agent", target, "", demoDiff)
		before := proposalSnapshot(t, db, id)
		s, _, _, _ := newService(t, db)
		s.Go = func(fn func()) { fn() }
		job, err := s.Start(id)
		if err != nil {
			t.Fatal(err)
		}
		if after := proposalSnapshot(t, db, id); after != before {
			t.Errorf("%s: proposal changed by a probe: %s → %s", status, before, after)
		}
		jobs := s.Jobs(id)
		if len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Status != JobDone {
			t.Errorf("%s: jobs %+v", status, jobs)
		}
		if len(mustRows(t, db, id)) != 3 {
			t.Errorf("%s: rows not written", status)
		}
	}
}

func TestStartRefusesUndecidableProposals(t *testing.T) {
	db := openDB(t)
	s, src, _, _ := newService(t, db)
	for _, status := range []string{"applied", "rejected", "failed", "needs_target"} {
		id := insertProposal(t, db, status, "agent", target, "", demoDiff)
		if _, err := s.Start(id); !errors.Is(err, ErrNotProbeable) {
			t.Errorf("%s: err = %v, want ErrNotProbeable", status, err)
		}
		if n := len(mustRows(t, db, id)); n != 0 {
			t.Errorf("%s: %d rows written for a refused probe", status, n)
		}
	}
	if _, err := s.Start(424242); !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if src.calls != 0 {
		t.Error("a refused probe exported a tree")
	}
}

func TestStartSingleFlightsPerProposal(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, _, _ := newService(t, db)
	var pending func()
	s.Go = func(fn func()) { pending = fn }
	first, err := s.Start(id)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != JobRunning || first.MaxUSD != 100 {
		t.Errorf("job %+v", first)
	}
	if again, err := s.Start(id); !errors.Is(err, ErrJobRunning) || again.ID != first.ID {
		t.Errorf("second start: %+v %v", again, err)
	}
	pending()
	if j := s.Jobs(id)[0]; j.Status != JobDone || j.FinishedAt == "" || j.USD <= 0 {
		t.Errorf("finished job %+v", j)
	}
	if _, err := s.Start(id); err != nil {
		t.Errorf("a finished job must not block a new one: %v", err)
	}
	if n := len(s.Jobs(id)); n != 2 {
		t.Errorf("jobs = %d", n)
	}
}

func TestNoSuiteForSkillTargets(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "skill", "", "plugins/core/skills/testing/SKILL.md", demoDiff)
	s, src, suite, _ := newService(t, db)
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, db, id)
	if len(rows) != 1 || rows[0].BlindChoice != ChoiceNoSuite || rows[0].BasePass != nil {
		t.Fatalf("rows %+v", rows)
	}
	if !strings.Contains(rows[0].Reason, "SKILL.md") {
		t.Errorf("reason %q", rows[0].Reason)
	}
	if src.calls != 0 || len(suite.calls) != 0 {
		t.Error("a SKILL.md probe must not export or run anything")
	}
}

func TestNoSuiteForAnAgentWithoutOne(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, src, suite, _ := newService(t, db)
	src.files = map[string]string{"evals/agents/demo.yaml": ""}
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, db, id)
	if len(rows) != 1 || rows[0].BlindChoice != ChoiceNoSuite || !strings.Contains(rows[0].Reason, "evals/agents/demo.yaml") {
		t.Fatalf("rows %+v", rows)
	}
	if len(suite.calls) != 0 {
		t.Error("ran a suite that does not exist")
	}
}

func TestNoSuiteWhenNoCaseLoadsTheTarget(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, src, _, _ := newService(t, db)
	src.files = map[string]string{"evals/agents/demo.yaml": "- description: x\n  vars:\n    system: file://../../plugins/core/agents/other.md\n"}
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, db, id)
	if len(rows) != 1 || rows[0].BlindChoice != ChoiceNoSuite || !strings.Contains(rows[0].Reason, "no case that loads") {
		t.Fatalf("rows %+v", rows)
	}
}

func TestNoSuiteForAProjectLocalAgent(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", "/Volumes/x/.claude/agents/demo.md", "", demoDiff)
	s, _, _, _ := newService(t, db)
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	if rows := mustRows(t, db, id); len(rows) != 1 || rows[0].BlindChoice != ChoiceNoSuite {
		t.Fatalf("rows %+v", rows)
	}
}

func TestDiffOutsideTheTargetIsRefused(t *testing.T) {
	db := openDB(t)
	evil := demoDiff + `--- a/evals/agents/demo.yaml
+++ b/evals/agents/demo.yaml
@@ -1,1 +1,1 @@
-- description: demo answers in one line
+- description: rigged
`
	id := insertProposal(t, db, "proposed", "agent", target, "", evil)
	s, _, suite, _ := newService(t, db)
	p, _ := s.LoadProposal(id)
	_, err := s.Run(context.Background(), "j", p)
	if err == nil || !strings.Contains(err.Error(), "a probe only patches") {
		t.Fatalf("err = %v", err)
	}
	if len(suite.calls) != 0 || len(mustRows(t, db, id)) != 0 {
		t.Error("a diff that edits the suite must not run")
	}
}

func TestStaleBaseIsRefused(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, src, _, _ := newService(t, db)
	src.files = map[string]string{target: "# demo\n\nchanged on main\n"}
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiffThatDoesNotApply(t *testing.T) {
	db := openDB(t)
	bad := strings.Replace(demoDiff, " Always answer in one line.", " Something else entirely.", 1)
	id := insertProposal(t, db, "proposed", "agent", target, "", bad)
	s, _, _, _ := newService(t, db)
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("err = %v", err)
	}
	id2 := insertProposal(t, db, "proposed", "agent", target, "", "  ")
	p2, _ := s.LoadProposal(id2)
	if _, err := s.Run(context.Background(), "j", p2); err == nil || !strings.Contains(err.Error(), "no diff") {
		t.Fatalf("empty diff: err = %v", err)
	}
}

func TestInfraFailureStopsTheJob(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, suite, _ := newService(t, db)
	suite.failOn = "variant|demo answers in one line"
	s.Go = func(fn func()) { fn() }
	if _, err := s.Start(id); err != nil {
		t.Fatal(err)
	}
	rows := mustRows(t, db, id)
	if len(rows) != 1 || rows[0].BlindChoice != ChoiceSkipped || !strings.Contains(rows[0].Reason, "variant run failed") {
		t.Fatalf("rows %+v", rows)
	}
	if j := s.Jobs(id)[0]; j.Status != JobFailed || !strings.Contains(j.Error, "exploded") {
		t.Errorf("job %+v", j)
	}

	id2 := insertProposal(t, db, "proposed", "agent", "plugins/core/agents/demo.md", "", demoDiff)
	suite.failOn = "base|demo answers in one line"
	p2, _ := s.LoadProposal(id2)
	if _, err := s.Run(context.Background(), "j2", p2); err == nil {
		t.Fatal("base failure must stop the job")
	}
	if rows := mustRows(t, db, id2); len(rows) != 1 || !strings.Contains(rows[0].Reason, "base run failed") {
		t.Errorf("rows %+v", rows)
	}
}

func TestRowsListNewestJobFirst(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, _, _ := newService(t, db)
	p, _ := s.LoadProposal(id)
	for _, job := range []string{"old", "new"} {
		if _, err := s.Run(context.Background(), job, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.Rows(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 || rows[0].JobID != "new" || rows[3].JobID != "old" || rows[0].ID > rows[1].ID {
		t.Errorf("order: %+v", rows)
	}
}

func TestMaxUSDFromEnv(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want float64
	}{{"", DefaultMaxUSD}, {"0.75", 0.75}, {"-1", DefaultMaxUSD}, {"abc", DefaultMaxUSD}, {"0", DefaultMaxUSD}, {"+Inf", DefaultMaxUSD}} {
		t.Setenv(MaxUSDEnv, tc.env)
		if got := MaxUSD(); got != tc.want {
			t.Errorf("%q: %v", tc.env, got)
		}
	}
	t.Setenv(MaxUSDEnv, "3")
	if got := (&Service{}).maxUSD(); got != 3 {
		t.Errorf("service falls back to env: %v", got)
	}
}

func TestIsAgentFile(t *testing.T) {
	for p, want := range map[string]bool{
		"plugins/core/agents/x.md":           true,
		"plugins/core/agents/.md":            false,
		"plugins/core/skills/x/SKILL.md":     false,
		"plugins//agents/x.md":               false,
		"/abs/plugins/core/agents/x.md":      false,
		"plugins/core/agents/../agents/x.md": false,
	} {
		if got := isAgentFile(p); got != want {
			t.Errorf("%s: %v", p, got)
		}
	}
}
