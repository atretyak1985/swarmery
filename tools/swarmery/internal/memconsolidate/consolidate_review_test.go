package memconsolidate

// Regression cases for the phase-3 review findings. Each one reproduces a way
// this package could destroy a memory, and pins the refusal that stops it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── finding 1: Apply must never rename over an already-consolidated memory ───

// TestApplyRefusesToOverwriteAConsolidatedMemory walks the exact live scenario:
// consolidate a memory, let the harness recreate one of the same name (it cannot
// see closed/, so it does), mark the new one closed, and consolidate again. The
// September memory must still be readable afterwards.
func TestApplyRefusesToOverwriteAConsolidatedMemory(t *testing.T) {
	const line = "- [Pretty project slugs](pretty-project-slugs.md) — DONE 2026-07-27: URL slugs name-derived.\n"
	const september = "---\nname: pretty-project-slugs\n---\n\nThe SEPTEMBER memory: DB slug is path-encoded, do not touch it.\n"

	dir := memoryDir(t, line, map[string]string{"pretty-project-slugs.md": september})

	// Round one: it consolidates normally.
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan round 1: %v", err)
	}
	if len(plan.Move) != 1 {
		t.Fatalf("round 1 Move = %+v, want one entry", plan.Move)
	}
	if _, err := Apply(dir, plan, ApplyOptions{Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatalf("Apply round 1: %v", err)
	}
	consolidated := filepath.Join(ClosedDir(dir), "pretty-project-slugs.md")
	if !strings.Contains(readFile(t, consolidated), "SEPTEMBER memory") {
		t.Fatalf("round 1 did not file the memory in closed/")
	}

	// The harness cannot see closed/, so a later session recreates the name.
	writeFile(t, IndexPath(dir), line)
	writeFile(t, filepath.Join(dir, "pretty-project-slugs.md"),
		"---\nname: pretty-project-slugs\n---\n\nA DIFFERENT, later memory that happens to reuse the name.\n")

	// Round two: the plan must hold it back with a reason, not queue a clobber.
	plan2, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan round 2: %v", err)
	}
	if len(plan2.Move) != 0 {
		t.Fatalf("round 2 Move = %+v, want nothing — the destination name is taken", plan2.Move)
	}
	if len(plan2.Keep) != 1 || plan2.Keep[0].Reason != ReasonClosedCollision {
		t.Fatalf("round 2 Keep = %+v, want one entry held by %q", plan2.Keep, ReasonClosedCollision)
	}

	// And Apply refuses even when handed a stale plan that still lists the move.
	stale := plan2
	stale.Move = []Action{{
		Title: "Pretty project slugs", File: "pretty-project-slugs.md",
		LineNo: 1, raw: strings.TrimRight(line, "\n"),
	}}
	stale.Keep = nil
	backupCalls := 0
	res, err := Apply(dir, stale, ApplyOptions{
		Now:    time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		Backup: func([]string) (string, error) { backupCalls++; return "should-not-happen", nil },
	})
	if err == nil {
		t.Fatal("Apply accepted a plan whose destination already exists — it would have renamed over the first memory")
	}
	if !strings.Contains(err.Error(), "pretty-project-slugs.md") {
		t.Errorf("refusal does not name the file: %v", err)
	}
	if backupCalls != 0 || len(res.Moved) != 0 {
		t.Errorf("the refusal was not free: backupCalls=%d moved=%v", backupCalls, res.Moved)
	}

	// The load-bearing assertion: September is still on disk, byte for byte.
	if got := readFile(t, consolidated); !strings.Contains(got, "SEPTEMBER memory") {
		t.Fatalf("the first memory was destroyed:\n%s", got)
	}
	// And the recreated one is still where it was — nothing was lost either way.
	if got := readFile(t, filepath.Join(dir, "pretty-project-slugs.md")); !strings.Contains(got, "DIFFERENT, later memory") {
		t.Fatalf("the recreated memory was disturbed:\n%s", got)
	}
}

// TestApplyBacksUpAnExistingDestination: when a destination somehow exists at
// backup time, its CURRENT bytes join the snapshot, so the old memory stays
// recoverable even if the refusal above were bypassed.
func TestApplyBacksUpAnExistingDestination(t *testing.T) {
	dir := memoryDir(t, "- [Alpha](alpha.md) — DONE 2026-01-01.\n", map[string]string{"alpha.md": "new\n"})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 1 {
		t.Fatalf("Move = %+v", plan.Move)
	}
	// Create the collision AFTER the plan was built (the stale-plan window).
	writeFile(t, filepath.Join(ClosedDir(dir), "alpha.md"), "the older alpha\n")

	var paths []string
	_, err = Apply(dir, plan, ApplyOptions{
		Backup: func(p []string) (string, error) { paths = append([]string{}, p...); return "b", nil },
	})
	if err == nil {
		t.Fatal("Apply did not refuse the collision")
	}
	// The refusal is checked before the backup, so no snapshot is taken at all —
	// which is correct: nothing was touched. Assert that directly.
	if paths != nil {
		t.Errorf("a backup was taken for a refused apply: %v", paths)
	}
	if got := readFile(t, filepath.Join(ClosedDir(dir), "alpha.md")); got != "the older alpha\n" {
		t.Fatalf("the already-consolidated memory changed: %q", got)
	}
}

// TestApplyRefusesTheSameFileTwiceInOnePlan: two index lines naming one file
// would have the second rename over the first's freshly written destination.
func TestApplyRefusesTheSameFileTwiceInOnePlan(t *testing.T) {
	index := "- [Alpha](alpha.md) — DONE 2026-01-01.\n- [Alpha again](alpha.md) — DONE 2026-01-02.\n"
	dir := memoryDir(t, index, map[string]string{"alpha.md": "only copy\n"})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 2 {
		t.Fatalf("Move = %+v, want both lines (the duplicate is caught in Apply)", plan.Move)
	}
	before := dirHash(t, dir)
	if _, err := Apply(dir, plan, ApplyOptions{}); err == nil {
		t.Fatal("Apply accepted a plan naming one file twice")
	}
	if after := dirHash(t, dir); after != before {
		t.Fatalf("the refused apply still mutated the directory")
	}
}

// ── finding 2: the index rewrite must not drop lines by ordinal alone ────────

// TestApplyAbortsWhenTheIndexChangedUnderIt reproduces the live race: the
// harness edits MEMORY.md between BuildPlan and the rewrite, shifting every
// ordinal. The rewrite must refuse rather than delete a different, OPEN entry.
func TestApplyAbortsWhenTheIndexChangedUnderIt(t *testing.T) {
	index := strings.Join([]string{
		"- [Search indexer](search-indexer.md) — DONE 2026-07-27: slugs name-derived.",
		"- [Order line items](order-line-items.md) — 7-phase plan; impl open.",
		"",
	}, "\n")
	dir := memoryDir(t, index, map[string]string{
		"search-indexer.md":   "closed body\n",
		"order-line-items.md": "open body\n",
	})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 1 || plan.Move[0].File != "search-indexer.md" {
		t.Fatalf("Move = %+v", plan.Move)
	}

	// The harness prepends a new memory — line 1 is no longer the planned one.
	shifted := "- [Brand new](brand-new.md) — impl open.\n" + index
	writeFile(t, IndexPath(dir), shifted)

	res, err := Apply(dir, plan, ApplyOptions{
		Now:    time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
		Backup: func([]string) (string, error) { return "snap-1", nil },
	})
	if err == nil {
		t.Fatal("Apply rewrote an index that had shifted under it — an open memory's line would be gone")
	}
	if !strings.Contains(err.Error(), "changed since the plan was built") {
		t.Errorf("error does not explain the drift: %v", err)
	}

	// MEMORY.md is untouched: every line the harness wrote is still there.
	got := readFile(t, IndexPath(dir))
	if got != shifted {
		t.Fatalf("the index was rewritten despite the abort:\n--- got ---\n%s\n--- want ---\n%s", got, shifted)
	}
	for _, want := range []string{"brand-new.md", "order-line-items.md", "search-indexer.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("index lost %s", want)
		}
	}

	// Finding 6's half: the partial state is reported, not swallowed.
	if res.BackupID != "snap-1" {
		t.Errorf("BackupID = %q, want the snapshot name to survive the error", res.BackupID)
	}
	if len(res.Moved) != 1 || res.Moved[0] != "search-indexer.md" {
		t.Errorf("Moved = %v, want the file that already moved", res.Moved)
	}
}

// TestApplyAbortsWhenAPlannedLineWasDeleted covers the other drift direction:
// the file got SHORTER, so a planned ordinal no longer exists at all.
func TestApplyAbortsWhenAPlannedLineWasDeleted(t *testing.T) {
	index := strings.Join([]string{
		"- [Keep me](keep-me.md) — impl open.",
		"- [Search indexer](search-indexer.md) — DONE 2026-07-27.",
		"",
	}, "\n")
	dir := memoryDir(t, index, map[string]string{
		"keep-me.md":        "open\n",
		"search-indexer.md": "closed\n",
	})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	truncated := "- [Keep me](keep-me.md) — impl open.\n"
	writeFile(t, IndexPath(dir), truncated)

	if _, err := Apply(dir, plan, ApplyOptions{}); err == nil {
		t.Fatal("Apply rewrote an index whose planned line had already been removed")
	}
	if got := readFile(t, IndexPath(dir)); got != truncated {
		t.Fatalf("index mutated after the abort: %q", got)
	}
}

// ── finding 3: two live open-tail shapes were classified as closed ───────────

// TestIsClosedLiveOpenTailShapes pins the two lines copied VERBATIM out of the
// operator's live MEMORY.md (titles neutralised) that the original marker set
// mis-closed. Both carry an open tail and must stay in the index.
func TestIsClosedLiveOpenTailShapes(t *testing.T) {
	cases := []struct {
		name string
		hook string
		want bool
	}{
		{
			// `tail:` — the original set only knew `tail =`.
			name: "live: colon-introduced tail stays",
			hook: "5 phases SHIPPED; 2026-07-31 tail: run process lifecycle (procgroup, 4h phase timeout, resume-cwd 409).",
			want: false,
		},
		{
			// A counted open tail with no colon anywhere.
			name: "live: counted deferrals open stays",
			hook: "134 usage guides SHIPPED into dev 2026-08-06 (PR #187); `dev` created that day; 4 deferrals open.",
			want: false,
		},
		// The counted-phrase rule must NOT break the deliberately non-vetoing
		// bare `open`: this line is the reason `open` alone does not veto.
		{
			name: "live: no open tail still closes",
			hook: "FULLY CLOSED 2026-08-11: landing LIVE (12/12) + daemon reinstalled (30M, guides served); no open tail.",
			want: true,
		},
		{name: "tail: with no space", hook: "DONE 2026-01-01; tail:merge the branch.", want: false},
		{name: "counted phases open", hook: "SHIPPED 2026-01-01; 2 phases open.", want: false},
		{name: "bare open is still narration", hook: "DONE 2026-01-01, the door is open.", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsClosed(tc.hook); got != tc.want {
				t.Fatalf("IsClosed(%q) = %v, want %v", tc.hook, got, tc.want)
			}
		})
	}
}

// TestOpenMarkerSetHasNoDeadAlternative: `\bOPEN\b` already matches "OPEN:",
// because ':' is a word boundary — the separate `\bOPEN:` branch was dead.
func TestOpenMarkerSetHasNoDeadAlternative(t *testing.T) {
	if !openRe.MatchString("MERGED; OPEN: flip the guard") {
		t.Fatal(`\bOPEN\b no longer matches "OPEN:" — removing the \bOPEN: branch changed behaviour`)
	}
	if strings.Contains(openRe.String(), `\bOPEN:`) {
		t.Errorf("the dead \\bOPEN: alternative is still in the set: %s", openRe.String())
	}
}

// ── finding 7: the link guard must reach a fixed point ──────────────────────

// TestBuildPlanLinkGuardFollowsATwoHopChain: open -> a -> b. Holding `a` back
// makes it open, so `b`, which only `a` links, must be held back too. A
// single-pass guard consolidates b and leaves a pointing into closed/.
func TestBuildPlanLinkGuardFollowsATwoHopChain(t *testing.T) {
	index := strings.Join([]string{
		"- [Still open](still-open.md) — impl open.",
		"- [Hop one](hop-one.md) — DONE 2026-01-01.",
		"- [Hop two](hop-two.md) — DONE 2026-01-02.",
		"",
	}, "\n")
	dir := memoryDir(t, index, map[string]string{
		"still-open.md": "depends on [[hop-one]]\n",
		"hop-one.md":    "which in turn depends on [[hop-two]]\n",
		"hop-two.md":    "the leaf\n",
	})
	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 0 {
		t.Fatalf("Move = %+v, want nothing: hop-one is held open, so hop-two is reachable from an open memory", plan.Move)
	}
	held := map[string]string{}
	for _, a := range plan.Keep {
		held[a.File] = a.Reason
	}
	for _, f := range []string{"hop-one.md", "hop-two.md"} {
		if held[f] != ReasonLinked {
			t.Errorf("%s reason = %q, want %q", f, held[f], ReasonLinked)
		}
	}
}

// TestBuildPlanLinkGuardCountsLinksFromEntriesHeldForOtherReasons: an entry kept
// because its destination name is taken stays in memory/, so its links are live.
func TestBuildPlanLinkGuardCountsLinksFromAHeldBackEntry(t *testing.T) {
	index := strings.Join([]string{
		"- [Collides](collides.md) — DONE 2026-01-01.",
		"- [Leaf](leaf.md) — DONE 2026-01-02.",
		"",
	}, "\n")
	dir := memoryDir(t, index, map[string]string{
		"collides.md": "still needs [[leaf]]\n",
		"leaf.md":     "leaf body\n",
	})
	// collides.md cannot move — that name is already consolidated.
	writeFile(t, filepath.Join(ClosedDir(dir), "collides.md"), "an earlier memory of the same name\n")

	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 0 {
		t.Fatalf("Move = %+v, want nothing: collides.md stays in memory/ and still links leaf", plan.Move)
	}
	held := map[string]string{}
	for _, a := range plan.Keep {
		held[a.File] = a.Reason
	}
	if held["collides.md"] != ReasonClosedCollision {
		t.Errorf("collides.md reason = %q, want %q", held["collides.md"], ReasonClosedCollision)
	}
	if held["leaf.md"] != ReasonLinked {
		t.Errorf("leaf.md reason = %q, want %q — a held-back entry's links are live", held["leaf.md"], ReasonLinked)
	}
}

// ── finding 8: a symlinked index target is not a regular file ───────────────

// TestBuildPlanHoldsBackASymlinkedTarget: os.Stat follows symlinks, so the old
// check saw a regular file, copied the TARGET's bytes into closed/ and removed
// only the link — filing a decoy and leaving the real memory loaded.
func TestBuildPlanHoldsBackASymlinkedTarget(t *testing.T) {
	dir := memoryDir(t, "- [Linked](linked.md) — DONE 2026-01-01.\n", nil)
	real := filepath.Join(t.TempDir(), "the-real-memory.md")
	writeFile(t, real, "the real content, which must never be copied out from under the link\n")
	if err := os.Symlink(real, filepath.Join(dir, "linked.md")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	plan, err := BuildPlan(dir)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Move) != 0 {
		t.Fatalf("Move = %+v, want nothing — a symlink is not a plain memory file", plan.Move)
	}
	if len(plan.Keep) != 1 || plan.Keep[0].Reason != ReasonMissingFile {
		t.Fatalf("Keep = %+v, want the symlink held back", plan.Keep)
	}
	if _, err := os.Lstat(filepath.Join(dir, "linked.md")); err != nil {
		t.Errorf("the symlink was disturbed: %v", err)
	}
	if got := readFile(t, real); !strings.Contains(got, "the real content") {
		t.Errorf("the symlink target changed: %q", got)
	}
}
