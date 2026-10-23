package wtcleanup

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

// recordingForcer removes nothing and records what it was handed.
type recordingForcer struct{ calls [][]string }

func (r *recordingForcer) ForceUnmerged(_ string, paths []string) []wtjanitor.ForceResult {
	r.calls = append(r.calls, append([]string(nil), paths...))
	out := make([]wtjanitor.ForceResult, len(paths))
	for i, p := range paths {
		out[i] = wtjanitor.ForceResult{Path: p, Outcome: wtjanitor.ForceRemoved, SalvageBranch: "salvage/x"}
	}
	return out
}

// janitor is the real ForceUnmerged over the fixture: worktrees live under the
// daemon root, liveness is not consulted.
func (f *fixture) janitor() *wtjanitor.Service {
	return &wtjanitor.Service{DB: f.db, Git: wtjanitor.RepoGit{}, DaemonRoot: f.root,
		Remover: wtjanitor.NewRealRemover(&worktree.Manager{Git: worktree.ExecGit{}})}
}

type openRow struct {
	id                  int64
	path, proof, branch string
	proposedAt          string
}

func (f *fixture) openRows() map[string]openRow {
	f.t.Helper()
	rows, err := f.db.Query(`SELECT id, path, proof_kind, branch, proposed_at FROM worktree_cleanup_proposals WHERE decided IS NULL`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]openRow{}
	for rows.Next() {
		var r openRow
		if err := rows.Scan(&r.id, &r.path, &r.proof, &r.branch, &r.proposedAt); err != nil {
			f.t.Fatal(err)
		}
		out[r.path] = r
	}
	return out
}

// threeKinds builds one candidate of each proof kind and returns the scan.
func threeKinds(t *testing.T, f *fixture) (scan Scan, squashed, landed, unmerged string) {
	t.Helper()
	a := f.phase(1, "done", "done", "none")
	squashed = f.worktree("phase-a", "swarm/phase-"+itoa(a), "a.md", "a\n")
	f.squashIntoMain("swarm/phase-" + itoa(a))
	b := f.phase(2, "done", "done", "merged")
	landed = f.worktree("phase-b", "swarm/phase-"+itoa(b), "b.md", "b\n")
	c := f.phase(3, "done", "done", "none")
	unmerged = f.worktree("phase-c", "swarm/phase-"+itoa(c), "c.md", "c\n")
	var err error
	if scan, err = Candidates(f.db, ExecGit{}, nil); err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	if len(scan.Candidates) != 3 {
		t.Fatalf("candidates = %v, want 3", scan.Candidates)
	}
	// Paths as git spells them — what Candidates stored.
	for _, cand := range scan.Candidates {
		switch {
		case strings.HasSuffix(cand.Path, "phase-a"):
			squashed = cand.Path
		case strings.HasSuffix(cand.Path, "phase-b"):
			landed = cand.Path
		case strings.HasSuffix(cand.Path, "phase-c"):
			unmerged = cand.Path
		}
	}
	return scan, squashed, landed, unmerged
}

// Propose writes one open row per candidate; a second pass refreshes in place
// (same ids, same proposed_at) and drops what stopped being a candidate.
func TestPropose_InsertsRefreshesAndDrops(t *testing.T) {
	f := newFixture(t)
	scan, squashed, landed, unmerged := threeKinds(t, f)

	t1 := time.Date(2026, 10, 22, 3, 30, 0, 0, time.UTC)
	sums, err := Propose(f.db, scan, t1)
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(sums) != 1 || sums[0].Added != 3 || sums[0].WithProof != 2 || sums[0].WithoutProof != 1 || sums[0].ProjectSlug != "proj" {
		t.Fatalf("summary = %+v, want 3 added, 2 with proof, 1 without", sums)
	}
	first := f.openRows()
	if first[squashed].proof != ProofContentDiff || first[landed].proof != ProofLanding || first[unmerged].proof != ProofNone {
		t.Errorf("stored proofs = %+v", first)
	}

	// Second pass: the unmerged one is gone from the scan.
	var trimmed Scan
	for _, c := range scan.Candidates {
		if c.Path != unmerged {
			trimmed.Candidates = append(trimmed.Candidates, c)
		}
	}
	sums, err = Propose(f.db, trimmed, t1.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Propose again: %v", err)
	}
	if sums[0].Refreshed != 2 || sums[0].Added != 0 || sums[0].Dropped != 1 {
		t.Errorf("second summary = %+v, want 2 refreshed, 1 dropped", sums[0])
	}
	second := f.openRows()
	if len(second) != 2 {
		t.Fatalf("open rows = %v, want 2", second)
	}
	for _, p := range []string{squashed, landed} {
		if second[p].id != first[p].id || second[p].proposedAt != first[p].proposedAt {
			t.Errorf("%s: refreshed row changed identity: %+v → %+v", p, first[p], second[p])
		}
	}

	// A pass with no candidates at all empties the repository's open proposals.
	if _, err := Propose(f.db, Scan{}, t1.Add(48*time.Hour)); err != nil {
		t.Fatalf("Propose empty: %v", err)
	}
	if n := len(f.openRows()); n != 0 {
		t.Errorf("open rows after an empty scan = %d, want 0", n)
	}
}

// Apply removes ONLY the selected paths, through the real janitor (salvage
// branch first), and a second identical apply is a no-op.
func TestApply_RemovesOnlySelectedAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	scan, squashed, landed, unmerged := threeKinds(t, f)
	if _, err := Propose(f.db, scan, time.Now()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	anchor := f.openRows()[landed].id

	res, err := Apply(f.db, ExecGit{}, f.janitor(), anchor, []string{squashed, unmerged, "/nowhere"}, time.Now())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := map[string]PathOutcome{}
	for _, o := range res.Paths {
		got[o.Path] = o
	}
	if o := got[squashed]; o.Outcome != wtjanitor.ForceRemoved || !strings.HasPrefix(o.SalvageBranch, "salvage/swarm-phase-") {
		t.Errorf("squashed: %+v, want removed with a salvage branch", o)
	}
	if got[unmerged].Outcome != OutcomeNoProof {
		t.Errorf("unmerged: %+v, want no-proof", got[unmerged])
	}
	if got["/nowhere"].Outcome != OutcomeNotProposed {
		t.Errorf("/nowhere: %+v, want not-proposed", got["/nowhere"])
	}
	if _, err := os.Stat(squashed); !os.IsNotExist(err) {
		t.Errorf("selected worktree still present: %v", err)
	}
	for _, p := range []string{landed, unmerged} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("unselected worktree %s was touched: %v", p, err)
		}
	}
	if out := f.git(f.repo, "branch", "--list", got[squashed].SalvageBranch); out == "" {
		t.Errorf("salvage branch %s missing", got[squashed].SalvageBranch)
	}
	open := f.openRows()
	if _, still := open[squashed]; still {
		t.Error("removed path still has an open proposal")
	}
	if _, ok := open[landed]; !ok {
		t.Error("the unselected landed proposal was decided")
	}

	// Same click again: idempotent, nothing reaches the janitor.
	rec := &recordingForcer{}
	again, err := Apply(f.db, ExecGit{}, rec, anchor, []string{squashed}, time.Now())
	if err != nil {
		t.Fatalf("Apply again: %v", err)
	}
	if len(rec.calls) != 0 || again.Paths[0].Outcome != OutcomeAlready || again.Paths[0].Reason != DecidedRemoved {
		t.Errorf("second apply = %+v, forcer calls %v; want already-decided and no call", again.Paths, rec.calls)
	}
}

// A path that disappeared between propose and apply is reported and decided
// gone — not an error; an unknown proposal id is ErrNotFound.
func TestApply_GonePathAndUnknownID(t *testing.T) {
	f := newFixture(t)
	scan, _, landed, _ := threeKinds(t, f)
	if _, err := Propose(f.db, scan, time.Now()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	anchor := f.openRows()[landed].id
	f.git(f.repo, "worktree", "remove", "--force", landed)

	res, err := Apply(f.db, ExecGit{}, f.janitor(), anchor, []string{landed}, time.Now())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Paths[0].Outcome != wtjanitor.ForceGone {
		t.Errorf("outcome = %+v, want gone", res.Paths[0])
	}
	var decided string
	if err := f.db.QueryRow(`SELECT decided FROM worktree_cleanup_proposals WHERE id = ?`, anchor).Scan(&decided); err != nil || decided != DecidedGone {
		t.Errorf("decided = %q (%v), want gone", decided, err)
	}

	if _, err := Apply(f.db, ExecGit{}, f.janitor(), 99999, []string{landed}, time.Now()); err != ErrNotFound {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}
}

// A checkout reused by another branch since the proposal is refused before the
// janitor is asked: the proof was for the old branch.
func TestApply_BranchChangedIsRefused(t *testing.T) {
	f := newFixture(t)
	scan, _, landed, _ := threeKinds(t, f)
	if _, err := Propose(f.db, scan, time.Now()); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	anchor := f.openRows()[landed].id
	f.git(landed, "checkout", "-q", "-b", "swarm/phase-other")

	rec := &recordingForcer{}
	res, err := Apply(f.db, ExecGit{}, rec, anchor, []string{landed}, time.Now())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Paths[0].Outcome != OutcomeBranchChanged || len(rec.calls) != 0 {
		t.Errorf("outcome = %+v, forcer calls %v; want branch-changed and no call", res.Paths[0], rec.calls)
	}
}
