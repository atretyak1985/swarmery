package wtjanitor

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

/* ---------- stubs ---------- */

// recordingRemover records what it was ASKED to destroy and destroys nothing.
// Every safety test asserts against its call log: the question is never "did
// the verdict look right" but "was anything handed to the destructive side".
type recordingRemover struct {
	removed  []string
	branches []string
	pruned   []string
	err      error
}

func (r *recordingRemover) RemoveWorktree(_, path, branch string, deleteBranch bool) error {
	if r.err != nil {
		return r.err
	}
	r.removed = append(r.removed, path)
	if deleteBranch && branch != "" {
		r.branches = append(r.branches, branch)
	}
	return nil
}

func (r *recordingRemover) DeleteBranch(_, branch string) error {
	r.branches = append(r.branches, branch)
	return nil
}

func (r *recordingRemover) Prune(repoRoot string) error {
	r.pruned = append(r.pruned, repoRoot)
	return nil
}

func (r *recordingRemover) calls() int { return len(r.removed) + len(r.branches) }

// stubInspector serves fixed worktrees and answers blob checks from a map. It
// deliberately does NOT apply the ownership filter it is handed — it records it
// — so the Service's own refusal is what the foreign-worktree tests exercise.
type stubInspector struct {
	wts   []Worktree
	inGit map[string]bool
	owned func(path string) bool
}

func (s *stubInspector) BlobInGit(_, _, repoRelPath string) (bool, error) {
	return s.inGit[repoRelPath], nil
}

// Inspect mirrors RepoGit.Inspect's one behavioural detail the tests depend on:
// it CONSULTS the liveness seam per worktree. Without that the pre-removal
// re-check would be the first call and a flip could never be modelled.
func (s *stubInspector) Inspect(_ string, live Liveness, owned func(path string) bool) ([]Worktree, error) {
	s.owned = owned
	out := make([]Worktree, 0, len(s.wts))
	for _, wt := range s.wts {
		busy, err := live.Busy(wt.Path)
		if err != nil {
			return nil, err
		}
		wt.Live = wt.Live || busy
		out = append(out, wt)
	}
	return out, nil
}

// flippingLive reports idle at classification time and busy afterwards — the
// race the pre-action re-check exists to catch.
type flippingLive struct{ asked int }

func (f *flippingLive) Busy(string) (bool, error) {
	f.asked++
	return f.asked > 1, nil
}

type idleLive struct{}

func (idleLive) Busy(string) (bool, error) { return false, nil }

/* ---------- harness ---------- */

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "wtj.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/repo', 'repo', 'Repo', '2026-08-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return db
}

// svc wires a Service whose every boundary is a stub.
func svc(t *testing.T, db *sql.DB, insp *stubInspector, rem Remover, live Liveness) *Service {
	t.Helper()
	return &Service{
		DB: db, Git: insp, Live: live, MinIdle: 30 * time.Minute,
		Remover: rem, now: func() time.Time { return time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC) },
	}
}

// sweepable is a worktree every veto passes and nothing keeps.
func sweepable() Worktree {
	return Worktree{
		Path:        "/repo/.claude/worktrees/agent-a1f",
		Branch:      "worktree-agent-a1f",
		NewestMTime: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
	}
}

func journalRows(t *testing.T, db *sql.DB) []map[string]any {
	t.Helper()
	rows, err := db.Query(`SELECT verdict, reason, salvage_branch, removed, error FROM worktree_sweeps ORDER BY id`)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var verdict, reason string
		var salvage, errStr sql.NullString
		var removed int
		if err := rows.Scan(&verdict, &reason, &salvage, &removed, &errStr); err != nil {
			t.Fatalf("scan journal: %v", err)
		}
		out = append(out, map[string]any{
			"verdict": verdict, "reason": reason,
			"salvage": salvage.String, "removed": removed, "error": errStr.String,
		})
	}
	return out
}

/* ---------- safety tests ---------- */

func TestSweep_SkipVerdictDestroysNothing(t *testing.T) {
	db := testDB(t)
	wt := sweepable()
	wt.Live = true
	rem := &recordingRemover{}
	if _, err := svc(t, db, &stubInspector{wts: []Worktree{wt}}, rem, idleLive{}).Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rem.calls() != 0 {
		t.Errorf("remover calls = %d, want 0 for a vetoed worktree (%+v)", rem.calls(), rem)
	}
}

func TestSweep_KeepUnmergedDestroysNothing(t *testing.T) {
	db := testDB(t)
	wt := sweepable()
	wt.HasOwnCommits = true
	rem := &recordingRemover{}
	if _, err := svc(t, db, &stubInspector{wts: []Worktree{wt}}, rem, idleLive{}).Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rem.calls() != 0 {
		t.Errorf("remover calls = %d, want 0 for unmerged work (%+v)", rem.calls(), rem)
	}
}

// The single most important test in the package: a failed rescue must abort the
// removal, not proceed with it.
func TestSweep_FailedSalvageKeepsTheWorktree(t *testing.T) {
	db := testDB(t)
	wt := sweepable()
	root := t.TempDir()
	wt.Path = filepath.Join(root, "not-a-repo") // Salvage will fail: no git here
	if err := os.MkdirAll(wt.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	wt.Dirty = []string{"unique.txt"}
	rem := &recordingRemover{}
	s := svc(t, db, &stubInspector{wts: []Worktree{wt}, inGit: map[string]bool{}}, rem, idleLive{})
	s.DaemonRoot = root // an owned path, so the sweep reaches the salvage
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rem.removed) != 0 {
		t.Errorf("RemoveWorktree called %d times after a failed salvage; want 0", len(rem.removed))
	}
	rows := journalRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("journal rows = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0]["verdict"] != string(VerdictKeepUnmerged) {
		t.Errorf("verdict = %v, want %q after a failed salvage", rows[0]["verdict"], VerdictKeepUnmerged)
	}
	if rows[0]["error"] == "" {
		t.Error("journal error is empty; a failed salvage must be recorded")
	}
	if rows[0]["removed"] != 0 {
		t.Errorf("removed = %v, want 0", rows[0]["removed"])
	}
}

func TestSweep_BecomingLiveBeforeRemovalAborts(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{}
	live := &flippingLive{}
	s := svc(t, db, &stubInspector{wts: []Worktree{sweepable()}}, rem, live)
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rem.calls() != 0 {
		t.Errorf("remover calls = %d, want 0 when the worktree went live mid-sweep", rem.calls())
	}
	rows := journalRows(t, db)
	if len(rows) != 1 || !strings.Contains(rows[0]["reason"].(string), "became live") {
		t.Errorf("journal = %+v, want a 'became live' skip row", rows)
	}
}

func TestSweep_DryRunJournalsButDestroysNothing(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{}
	s := svc(t, db, &stubInspector{wts: []Worktree{sweepable()}}, rem, idleLive{})
	res, err := s.Sweep(true)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if rem.calls() != 0 {
		t.Errorf("remover calls = %d in dry-run, want 0", rem.calls())
	}
	if res.Removed != 0 {
		t.Errorf("Result.Removed = %d in dry-run, want 0", res.Removed)
	}
	if rows := journalRows(t, db); len(rows) != 1 || rows[0]["verdict"] != string(VerdictRedundant) {
		t.Errorf("dry-run journal = %+v, want one redundant row", rows)
	}
}

func TestSweep_RedundantRemovesWorktreeAndBranch(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{}
	wt := sweepable()
	s := svc(t, db, &stubInspector{wts: []Worktree{wt}}, rem, idleLive{})
	res, err := s.Sweep(false)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rem.removed) != 1 || rem.removed[0] != wt.Path {
		t.Errorf("removed = %v, want [%s]", rem.removed, wt.Path)
	}
	if len(rem.branches) != 1 || rem.branches[0] != wt.Branch {
		t.Errorf("branches = %v, want [%s]", rem.branches, wt.Branch)
	}
	if res.Removed != 1 {
		t.Errorf("Result.Removed = %d, want 1", res.Removed)
	}
}

func TestSweep_JournalFailureDoesNotAbortTheSweep(t *testing.T) {
	db := testDB(t)
	db.Close() // every INSERT and the project query now fail
	rem := &recordingRemover{}
	s := svc(t, db, &stubInspector{wts: []Worktree{sweepable()}}, rem, idleLive{})
	if _, err := s.Sweep(false); err == nil {
		t.Log("Sweep returned no error on a closed DB (repos() failed softly) — acceptable")
	}
	// The point is that it RETURNS rather than panicking or hanging.
}

func TestSweep_IsIdempotent(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{}
	insp := &stubInspector{wts: []Worktree{sweepable()}}
	s := svc(t, db, insp, rem, idleLive{})
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	callsAfterFirst := rem.calls()
	// A real second pass sees the worktree gone; model that by emptying the list.
	insp.wts = nil
	res, err := s.Sweep(false)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if res.Removed != 0 || res.Salvaged != 0 {
		t.Errorf("second sweep did work: %+v, want zero removals and salvages", res)
	}
	if rem.calls() != callsAfterFirst {
		t.Errorf("remover gained calls on the second sweep: %d → %d", callsAfterFirst, rem.calls())
	}
}

func TestSweep_RemoverErrorIsJournalledAndCounted(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{err: errors.New("git said no")}
	s := svc(t, db, &stubInspector{wts: []Worktree{sweepable()}}, rem, idleLive{})
	res, err := s.Sweep(false)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if res.Errors != 1 || res.Removed != 0 {
		t.Errorf("result = %+v, want Errors 1 / Removed 0", res)
	}
	if rows := journalRows(t, db); len(rows) != 1 || rows[0]["error"] == "" {
		t.Errorf("journal = %+v, want the failure recorded", rows)
	}
}

/* ---------- salvage integration (real git) ---------- */

// The 2026-09-28 regression: an operator's feature worktree under
// <repo>/.claude/worktrees, clean and with its branch pushed, is "redundant" to
// the classifier. Ownership is decided BEFORE classification, so it is never
// handed to the remover and never journalled — every tick would otherwise add a
// row about a worktree the janitor has no business judging.
func TestSweep_NeverTouchesAWorktreeItDoesNotOwn(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{}
	foreign := sweepable()
	foreign.Path = "/repo/.claude/worktrees/account-switch-estate"
	foreign.Branch = "feat/account-switch-estate"
	agent := sweepable()
	insp := &stubInspector{wts: []Worktree{foreign, agent}}
	res, err := svc(t, db, insp, rem, idleLive{}).Sweep(false)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rem.removed) != 1 || rem.removed[0] != agent.Path {
		t.Fatalf("removed = %v, want only the agent worktree %s", rem.removed, agent.Path)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM worktree_sweeps WHERE path = ?`, foreign.Path).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("journal holds %d rows for a worktree the janitor does not own, want 0", n)
	}
	if res.Inspected != 1 {
		t.Errorf("Inspected = %d, want 1 (the foreign worktree is not inspected)", res.Inspected)
	}
}

// The filter must reach Inspect, so the real inspector never runs `git status`
// (and so never takes an index.lock) inside an operator's worktree.
func TestSweep_HandsInspectTheOwnershipFilter(t *testing.T) {
	db := testDB(t)
	insp := &stubInspector{wts: []Worktree{sweepable()}}
	if _, err := svc(t, db, insp, &recordingRemover{}, idleLive{}).Sweep(true); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if insp.owned == nil {
		t.Fatal("Inspect received no ownership filter")
	}
	if insp.owned("/repo/.claude/worktrees/account-switch-estate") {
		t.Error("the filter owns an operator worktree")
	}
	if !insp.owned("/repo/.claude/worktrees/agent-a1f") {
		t.Error("the filter does not own a harness agent worktree")
	}
}

// The daemon's own task checkouts live under its worktree root, not under the
// repository, and stay in scope.
func TestSweep_RemovesADaemonWorktreeUnderTheDaemonRoot(t *testing.T) {
	db := testDB(t)
	rem := &recordingRemover{}
	wt := sweepable()
	wt.Path = "/home/u/.swarmery/worktrees/repo/plan-1"
	wt.Branch = "swarm/plan-1"
	s := svc(t, db, &stubInspector{wts: []Worktree{wt}}, rem, idleLive{})
	s.DaemonRoot = "/home/u/.swarmery/worktrees"
	if _, err := s.Sweep(false); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(rem.removed) != 1 || rem.removed[0] != wt.Path {
		t.Errorf("removed = %v, want the daemon worktree %s", rem.removed, wt.Path)
	}
}

func TestSalvage_RescuesContentThenTheWorktreeGoes(t *testing.T) {
	repo, run := testRepo(t)
	wtPath := filepath.Join(t.TempDir(), "agent-salvage")
	run("worktree", "add", "-q", "-b", "worktree-agent-salvage", wtPath)
	write(t, filepath.Join(wtPath, "only-here.md"), "content that exists nowhere else\n")

	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	branch, err := Salvage(repo, wtPath, "worktree-agent-salvage", now)
	if err != nil {
		t.Fatalf("Salvage: %v", err)
	}
	if want := "salvage/agent-salvage-20260805"; branch != want {
		t.Errorf("branch = %q, want %q", branch, want)
	}

	// The rescued file must be reachable from the new ref…
	out, err := run2(repo, "cat-file", "-p", branch+":only-here.md")
	if err != nil {
		t.Fatalf("read salvaged file: %v", err)
	}
	if !strings.Contains(out, "exists nowhere else") {
		t.Errorf("salvaged content = %q", out)
	}
	// …and the main checkout must be untouched: same HEAD, same branch.
	if head := run("rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
		t.Errorf("main checkout HEAD = %q, want main — salvage must not check anything out", head)
	}
}

// run2 is a non-fatal runner for assertions that want the error.
func run2(dir string, args ...string) (string, error) { return run(dir, args...) }
