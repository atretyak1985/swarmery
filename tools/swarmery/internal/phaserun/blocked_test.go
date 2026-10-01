package phaserun

import (
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// The blocked re-run guard. Every test drives the REAL service: a run that ends
// blocked (the stub executor writes the `PHASE BLOCKED:` ending the completion
// loop reads), then a second Start whose only difference from the first is the
// one thing the test changes.

const blockedReason = "contracts exist only on the unmerged swarm/phase-26498"

// blockingRunner is an executor that reads the code and reports it cannot go on.
func blockingRunner(t *testing.T, db *sql.DB) *stubRunner {
	t.Helper()
	r := &stubRunner{}
	r.runFn = func(spec RunSpec) (*Run, error) {
		seedTranscript(t, db, spec.SessionUUID, "I read the code.\n\nPHASE BLOCKED: "+blockedReason)
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	return r
}

// blockPhase runs phase p1 once so that it settles blocked, and returns the
// service, its runner and its worktree stub for the follow-up Start.
func blockPhase(t *testing.T, db *sql.DB, p1 int64) (*Service, *stubRunner, *stubWt) {
	t.Helper()
	r, wt := blockingRunner(t, db), &stubWt{}
	s := newTestService(db, r, wt)
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "blocked" {
		t.Fatalf("run_state = %q, want blocked (test premise)", state)
	}
	if !storedFingerprint(t, db, p1).Valid {
		t.Fatal("no fingerprint was stamped for a blocked run (test premise)")
	}
	return s, r, wt
}

func storedFingerprint(t *testing.T, db *sql.DB, phaseID int64) sql.NullString {
	t.Helper()
	var fp sql.NullString
	if err := db.QueryRow(`SELECT run_blocked_fingerprint FROM epic_phases WHERE id=?`, phaseID).Scan(&fp); err != nil {
		t.Fatalf("read run_blocked_fingerprint: %v", err)
	}
	return fp
}

// TestBlockedUnchangedRefused: nothing changed since the run blocked, so the
// re-run is refused before anything is acquired, spawned or stamped — and the
// refusal carries the blocked run's own reason and when it ends.
func TestBlockedUnchangedRefused(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s, r, wt := blockPhase(t, db, p1)
	before := storedFingerprint(t, db, p1)

	_, err := s.Start(p1, "", "")
	if !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("err = %v, want ErrBlockedUnchanged", err)
	}
	var unchanged *BlockedUnchangedError
	if !errors.As(err, &unchanged) {
		t.Fatalf("err = %v, want a *BlockedUnchangedError", err)
	}
	if unchanged.Reason != blockedReason {
		t.Errorf("Reason = %q, want the blocked run's reason", unchanged.Reason)
	}
	since, perr := time.Parse(time.RFC3339, unchanged.Since)
	if perr != nil {
		t.Fatalf("Since = %q: %v", unchanged.Since, perr)
	}
	if want := since.Add(24 * time.Hour).UTC().Format(time.RFC3339); unchanged.RetryAfter != want {
		t.Errorf("RetryAfter = %q, want Since + the default 24h cooldown = %q", unchanged.RetryAfter, want)
	}
	for _, want := range []string{blockedReason, unchanged.Since, unchanged.RetryAfter, "force"} {
		if !strings.Contains(unchanged.Error(), want) {
			t.Errorf("message %q does not carry %q", unchanged.Error(), want)
		}
	}

	// An admission verdict: no second spawn, no second worktree, no slot held, and
	// the row is exactly as the blocked run left it.
	if n := r.specCount(); n != 1 {
		t.Errorf("spawned %d times, want 1 — the refused re-run must not start", n)
	}
	if n := wt.acquiredCount(); n != 1 {
		t.Errorf("acquired %d worktrees, want 1", n)
	}
	if s.Slots.IsActive(s.slotKey(p1)) {
		t.Error("the refused re-run is holding the slot")
	}
	if state, _, _, runErr := phaseRow(t, db, p1); state != "blocked" || runErr.String != blockedReason {
		t.Errorf("row after the refusal = %q / %q, want it untouched", state, runErr.String)
	}
	if after := storedFingerprint(t, db, p1); after != before {
		t.Errorf("fingerprint changed across a refusal: %v → %v", before, after)
	}

	// What a blocked run rewrites itself — its report and its forecast — is not a
	// change to the phase, so the refusal survives both being edited.
	doc := phaseDocPath(t, db, p1)
	body, rerr := os.ReadFile(doc)
	if rerr != nil {
		t.Fatal(rerr)
	}
	mustWriteDoc(t, doc, string(body)+
		"\n## Forecast\n\n```yaml\nkind: posterior\nconfidence: 0.2\n```\n\n## Completion Report\n\nBlocked: the dependency is not merged.\n")
	if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
		t.Errorf("after a report + forecast rewrite: err = %v, want still ErrBlockedUnchanged", err)
	}
}

// TestBlockedChangedAdmitted: each thing the fingerprint hashes, changed on its
// own, lets the re-run through.
func TestBlockedChangedAdmitted(t *testing.T) {
	// A criterion was ticked.
	t.Run("a tick", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		s, r, _ := blockPhase(t, db, p1)
		mustWriteDoc(t, phaseDocPath(t, db, p1), "# Phase 1 — Schema\n\n- [x] a\n- [ ] b\n")

		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("Start after a tick: %v", err)
		}
		if n := r.specCount(); n != 2 {
			t.Errorf("spawned %d times, want the re-run admitted (2)", n)
		}
	})

	// The doc changed outside its Completion Report and Forecast — the premise the
	// run blocked on was rewritten.
	t.Run("a doc edit outside the report", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		s, r, _ := blockPhase(t, db, p1)
		mustWriteDoc(t, phaseDocPath(t, db, p1),
			"# Phase 1 — Schema\n\nUse the `orders` table, not `order_items`.\n\n- [ ] a\n- [ ] b\n")

		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("Start after a doc edit: %v", err)
		}
		if n := r.specCount(); n != 2 {
			t.Errorf("spawned %d times, want the re-run admitted (2)", n)
		}
	})

	// The base moved: a commit landed on the repo's branch, so the next run starts
	// on a different tree. Real git, in a temp repo.
	t.Run("a moved base", func(t *testing.T) {
		repo := newTempRepo(t)
		db, _, p1, _ := fixture(t)
		mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)

		r, wt := blockingRunner(t, db), &stubWt{}
		s := newTestService(db, r, wt)
		s.Git = repo.git
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		if state, _, _, _ := phaseRow(t, db, p1); state != "blocked" {
			t.Fatalf("run_state = %q, want blocked (test premise)", state)
		}
		// Same base ⇒ refused: the git half of the fingerprint is stable.
		if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
			t.Fatalf("unchanged base: err = %v, want ErrBlockedUnchanged", err)
		}

		repo.commit("the dependency's PR merged")

		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("Start after the base moved: %v", err)
		}
		if n := r.specCount(); n != 2 {
			t.Errorf("spawned %d times, want the re-run admitted (2)", n)
		}
	})

	// A dependency's run branch gained a commit: where the run starts is the same
	// (the dependency is merged), yet the situation is not.
	t.Run("a dependency branch moved", func(t *testing.T) {
		repo := newTempRepo(t)
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
		depBranch := "swarm/phase-" + itoa64(p1)
		repo.branch(depBranch, "main", 1)
		repo.run("merge", "-q", "--ff-only", depBranch)
		mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=2, run_branch=? WHERE id=?`, depBranch, p1)

		r, wt := blockingRunner(t, db), &stubWt{}
		s := newTestService(db, r, wt)
		s.Git = repo.git
		if _, err := s.Start(p2, "", ""); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		if _, err := s.Start(p2, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
			t.Fatalf("unchanged dependency: err = %v, want ErrBlockedUnchanged", err)
		}

		repo.run("checkout", "-q", depBranch)
		repo.commit("a fix on the dependency branch")
		repo.run("checkout", "-q", "main")

		if _, err := s.Start(p2, "", ""); err != nil {
			t.Fatalf("Start after the dependency branch moved: %v", err)
		}
	})
}

// TestBlockedForceAdmitted: the operator says run it anyway. Force overrides this
// one refusal — and the forced run, if it ends differently, clears the fingerprint.
func TestBlockedForceAdmitted(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s, r, _ := blockPhase(t, db, p1)

	if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
		t.Fatalf("unforced: err = %v, want ErrBlockedUnchanged", err)
	}

	// The forced run finishes the work this time.
	r.mu.Lock()
	r.runFn = func(spec RunSpec) (*Run, error) {
		mustWriteDoc(t, phaseDocPath(t, db, p1), "# Phase 1 — Schema\n\n- [x] a\n- [x] b\n")
		seedTranscript(t, db, spec.SessionUUID, "Done.\n\nPHASE DONE")
		return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
	}
	r.mu.Unlock()

	if _, err := s.StartWith(p1, StartOptions{Force: true}); err != nil {
		t.Fatalf("forced Start: %v", err)
	}
	if n := r.specCount(); n != 2 {
		t.Errorf("spawned %d times, want the forced run admitted (2)", n)
	}
	if state, _, _, _ := phaseRow(t, db, p1); state != "done" {
		t.Errorf("run_state = %q, want done", state)
	}
	if fp := storedFingerprint(t, db, p1); fp.Valid {
		t.Errorf("run_blocked_fingerprint = %q after a run that ended done, want NULL", fp.String)
	}
}

// TestBlockedForceDoesNotBypassOtherGates: force is the blocked guard's override
// and nothing else's.
func TestBlockedForceDoesNotBypassOtherGates(t *testing.T) {
	db, _, _, p2 := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	if _, err := s.StartWith(p2, StartOptions{Force: true}); !errors.Is(err, ErrDepsUnmet) {
		t.Fatalf("forced Start with an unmet dependency: err = %v, want ErrDepsUnmet", err)
	}
}

// TestBlockedCooldownLapses: the refusal is for a while, not for ever. Some blocks
// lift with nothing in the repo or the doc changing (a date gate, another plan's
// dependency), and the daily routine has to be able to try again.
func TestBlockedCooldownLapses(t *testing.T) {
	endedAt := func(t *testing.T, db *sql.DB, p int64) time.Time {
		t.Helper()
		_, ended := phaseOutcome(t, db, p)
		at, err := time.Parse(time.RFC3339, ended.String)
		if err != nil {
			t.Fatalf("run_ended_at = %q: %v", ended.String, err)
		}
		return at
	}

	t.Run("default 24h", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		s, r, _ := blockPhase(t, db, p1)
		blockedAt := endedAt(t, db, p1)

		s.now = func() time.Time { return blockedAt.Add(24*time.Hour - time.Second) }
		if _, err := s.Start(p1, "", ""); !errors.Is(err, ErrBlockedUnchanged) {
			t.Fatalf("one second inside the cooldown: err = %v, want ErrBlockedUnchanged", err)
		}
		s.now = func() time.Time { return blockedAt.Add(24 * time.Hour) }
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("at the cooldown's end: %v", err)
		}
		if n := r.specCount(); n != 2 {
			t.Errorf("spawned %d times, want the lapsed re-run admitted (2)", n)
		}
	})

	t.Run("a shorter cooldown from the env", func(t *testing.T) {
		t.Setenv(blockedCooldownEnv, "1h")
		db, _, p1, _ := fixture(t)
		s, _, _ := blockPhase(t, db, p1)
		blockedAt := endedAt(t, db, p1)

		s.now = func() time.Time { return blockedAt.Add(59 * time.Minute) }
		var unchanged *BlockedUnchangedError
		if _, err := s.Start(p1, "", ""); !errors.As(err, &unchanged) {
			t.Fatalf("inside 1h: err = %v, want a *BlockedUnchangedError", err)
		}
		if want := blockedAt.Add(time.Hour).UTC().Format(time.RFC3339); unchanged.RetryAfter != want {
			t.Errorf("RetryAfter = %q, want %q", unchanged.RetryAfter, want)
		}
		s.now = func() time.Time { return blockedAt.Add(61 * time.Minute) }
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("after 1h: %v", err)
		}
	})

	t.Run("0 disables the guard", func(t *testing.T) {
		t.Setenv(blockedCooldownEnv, "0")
		db, _, p1, _ := fixture(t)
		s, _, _ := blockPhase(t, db, p1)
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("cooldown 0: %v — the guard must be off", err)
		}
	})

	t.Run("a typo keeps the default", func(t *testing.T) {
		for _, bad := range []string{"tomorrow", "-5m"} {
			t.Setenv(blockedCooldownEnv, bad)
			if got := blockedCooldown(); got != defaultBlockedCooldown {
				t.Errorf("%s=%q ⇒ %s, want the default — a typo must not switch the guard off",
					blockedCooldownEnv, bad, got)
			}
		}
	})

	t.Run("an unparseable end time has no provable age", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		s, _, _ := blockPhase(t, db, p1)
		mustExec(t, db, `UPDATE epic_phases SET run_ended_at='yesterday-ish' WHERE id=?`, p1)
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("unparseable run_ended_at: %v — the guard must admit what it cannot date", err)
		}
	})
}

// fingerprintDoc is a phase doc shaped like the real ones: header, body, criteria,
// a fenced agent prompt that QUOTES both volatile headings, then the two sections.
const fingerprintDoc = "# Phase 4 — Stacking\n" +
	"\n" +
	"Status: In progress\n" +
	"\n" +
	"## Goal\n" +
	"\n" +
	"Start on a tree that contains the dependency.\n" +
	"\n" +
	"## Copy-paste Agent Prompt\n" +
	"\n" +
	"```\n" +
	"Before your first edit add a block to\n" +
	"## Forecast\n" +
	"and when you finish fill\n" +
	"## Completion Report\n" +
	"QUOTED-TAIL\n" +
	"```\n" +
	"\n" +
	"## Acceptance Criteria\n" +
	"\n" +
	"- [ ] **4.1** first\n" +
	"- [ ] **4.2** second\n" +
	"\n" +
	"## Forecast\n" +
	"\n" +
	"```yaml\n" +
	"kind: prior\n" +
	"confidence: 0.5\n" +
	"```\n" +
	"\n" +
	"## Notes\n" +
	"\n" +
	"NOTES-BODY\n" +
	"\n" +
	"## Completion Report\n"

// TestFingerprintIgnoresReportAndForecast: a blocked run rewrites its own report
// and adds a posterior forecast. Neither is a change to the phase's situation —
// and everything else in the doc is.
func TestFingerprintIgnoresReportAndForecast(t *testing.T) {
	fp := func(doc string) string { return blockedFingerprint("base000", 0, []string{"dep111"}, doc) }
	base := fp(fingerprintDoc)

	same := map[string]string{
		"the report filled in": fingerprintDoc + "\nBlocked after reading the code.\n\n**Blocked calls** none\n",
		"a posterior forecast added": strings.Replace(fingerprintDoc,
			"confidence: 0.5\n```\n",
			"confidence: 0.5\n```\n\n```yaml\nkind: posterior\nconfidence: 0.2\n```\n", 1),
		"the prior forecast rewritten": strings.Replace(fingerprintDoc, "confidence: 0.5", "confidence: 0.9", 1),
		"both at once": strings.Replace(fingerprintDoc, "confidence: 0.5", "confidence: 0.1", 1) +
			"\n### Where reality diverged\n\nEverything.\n",
		"trailing whitespace only": strings.Replace(fingerprintDoc, "NOTES-BODY\n", "NOTES-BODY  \n", 1) + "\n\n",
		"the report section with a sub-heading and a fence": fingerprintDoc +
			"\n### Verification\n\n```\n## Notes\nnot a real heading\n```\n",
	}
	// The heading match is case-insensitive, like wsingest's own. Built from the
	// LAST occurrence: the first one is the heading quoted inside the fenced prompt.
	reportAt := strings.LastIndex(fingerprintDoc, "## Completion Report\n")
	same["a differently cased heading"] = fingerprintDoc[:reportAt] + "## completion report\n\nfilled\n"

	for name, doc := range same {
		if got := fp(doc); got != base {
			t.Errorf("%s: fingerprint changed — a blocked run would look like a change to itself", name)
		}
	}

	changed := map[string]string{
		"a tick in the body":                  strings.Replace(fingerprintDoc, "- [ ] **4.1**", "- [x] **4.1**", 1),
		"the goal rewritten":                  strings.Replace(fingerprintDoc, "contains the dependency", "contains BOTH dependencies", 1),
		"the status line":                     strings.Replace(fingerprintDoc, "Status: In progress", "Status: Pending", 1),
		"a section AFTER the forecast":        strings.Replace(fingerprintDoc, "NOTES-BODY", "a new note about the premise", 1),
		"text inside the fenced agent prompt": strings.Replace(fingerprintDoc, "QUOTED-TAIL", "and merge the dependency first", 1),
		"a criterion added":                   strings.Replace(fingerprintDoc, "- [ ] **4.2** second\n", "- [ ] **4.2** second\n- [ ] **4.3** third\n", 1),
	}
	for name, doc := range changed {
		if got := fp(doc); got == base {
			t.Errorf("%s: fingerprint did NOT change — a real change to the phase would be refused as unchanged", name)
		}
	}

	// The other three inputs each move the fingerprint on their own.
	if blockedFingerprint("base999", 0, []string{"dep111"}, fingerprintDoc) == base {
		t.Error("a moved base did not change the fingerprint")
	}
	if blockedFingerprint("base000", 1, []string{"dep111"}, fingerprintDoc) == base {
		t.Error("a ticked criterion count did not change the fingerprint")
	}
	if blockedFingerprint("base000", 0, []string{"dep222"}, fingerprintDoc) == base {
		t.Error("a moved dependency tip did not change the fingerprint")
	}
	// Length-prefixed parts: shifting bytes between two inputs is not a collision.
	if blockedFingerprint("ab", 0, []string{"c"}, "") == blockedFingerprint("a", 0, []string{"bc"}, "") {
		t.Error("two different (base, deps) splits hash the same")
	}

	// What is removed is exactly the two sections, and what is quoted stays.
	stable := stableDocBody(fingerprintDoc + "\nthe report body\n")
	for _, gone := range []string{"kind: prior", "the report body"} {
		if strings.Contains(stable, gone) {
			t.Errorf("stable body still carries %q", gone)
		}
	}
	for _, kept := range []string{"QUOTED-TAIL", "NOTES-BODY", "- [ ] **4.1** first", "## Notes", "## Goal"} {
		if !strings.Contains(stable, kept) {
			t.Errorf("stable body lost %q", kept)
		}
	}
}

// TestBlockedFingerprintFailsOpen: a fingerprint that cannot be computed is stored
// as NULL, and a NULL never refuses. The guard saves a run; it must not strand a
// phase because its own bookkeeping could not be read.
func TestBlockedFingerprintFailsOpen(t *testing.T) {
	t.Run("the doc is unreadable when the run blocks", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		doc := phaseDocPath(t, db, p1)
		body, err := os.ReadFile(doc)
		if err != nil {
			t.Fatal(err)
		}
		r := &stubRunner{}
		r.runFn = func(spec RunSpec) (*Run, error) {
			seedTranscript(t, db, spec.SessionUUID, "PHASE BLOCKED: "+blockedReason)
			if err := os.Remove(doc); err != nil {
				t.Errorf("remove doc: %v", err)
			}
			return &Run{SessionUUID: spec.SessionUUID, ExitCode: 0}, nil
		}
		s := newTestService(db, r, &stubWt{})
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("first Start: %v", err)
		}
		if state, _, _, _ := phaseRow(t, db, p1); state != "blocked" {
			t.Fatalf("run_state = %q, want blocked", state)
		}
		if fp := storedFingerprint(t, db, p1); fp.Valid {
			t.Fatalf("fingerprint = %q for an unreadable doc, want NULL", fp.String)
		}
		mustWriteDoc(t, doc, string(body))
		r.mu.Lock()
		r.runFn = nil
		r.mu.Unlock()
		if _, err := s.Start(p1, "", ""); err != nil {
			t.Fatalf("re-run with no recorded fingerprint: %v — it must be admitted", err)
		}
	})

	t.Run("nothing to fingerprint", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		if fp := s.fingerprintForStamp(p1, ""); fp != "" {
			t.Errorf("no doc path ⇒ %q, want none", fp)
		}
		if fp := s.fingerprintForStamp(99999, phaseDocPath(t, db, p1)); fp != "" {
			t.Errorf("unknown phase ⇒ %q, want none", fp)
		}
		mustExec(t, db, `UPDATE projects SET path='' WHERE id=1`)
		if fp := s.fingerprintForStamp(p1, phaseDocPath(t, db, p1)); fp != "" {
			t.Errorf("pathless project ⇒ %q, want none", fp)
		}
	})

	t.Run("the run repository does not resolve", func(t *testing.T) {
		db, _, p1, _ := fixture(t)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		s.RepoRoot = func(string, ...string) (string, error) { return "", ErrNoRepoRoot }
		if fp := s.fingerprintForStamp(p1, phaseDocPath(t, db, p1)); fp != "" {
			t.Errorf("unresolved repository ⇒ %q, want none", fp)
		}
	})

	t.Run("the base cannot be measured", func(t *testing.T) {
		db, _, p1, p2 := fixture(t)
		mustExec(t, db, `UPDATE epic_phases SET run_branch='swarm/phase-1' WHERE id=?`, p1)
		s := newTestService(db, &stubRunner{}, &stubWt{})
		s.Git = &stubGit{err: errors.New("fatal: not a git repository")}
		if fp := s.fingerprintForStamp(p2, phaseDocPath(t, db, p2)); fp != "" {
			t.Errorf("unmeasurable base ⇒ %q, want none", fp)
		}
	})
}

// TestBlockedFingerprintSurvivesDivergedDeps: the dependency branches diverged
// while the run was going. There is no start point any more, but there is still a
// situation to describe — and the next Start answers deps-unmerged, not a stale
// "unchanged".
func TestBlockedFingerprintSurvivesDivergedDeps(t *testing.T) {
	repo := newTempRepo(t)
	db, taskID, p1, p2 := fixture(t)
	mustExec(t, db, `UPDATE projects SET path=? WHERE id=1`, repo.dir)
	repo.branch("swarm/phase-a", "main", 1)
	repo.branch("swarm/phase-b", "main", 1)
	mustExec(t, db, `UPDATE epic_phases SET checkboxes_done=2, run_branch='swarm/phase-a' WHERE id=?`, p1)
	mustExec(t, db, `INSERT INTO epic_phases
		(workspace_task_id, seq, name, doc_path, depends_on, checkboxes_total, checkboxes_done, run_branch)
		VALUES (?, 3, 'Phase 3', '/plan/phase-3.md', '[]', 1, 1, 'swarm/phase-b')`, taskID)
	mustExec(t, db, `UPDATE epic_phases SET depends_on='[1,3]' WHERE id=?`, p2)

	s := newTestService(db, &stubRunner{}, &stubWt{})
	s.Git = repo.git
	if fp := s.fingerprintForStamp(p2, phaseDocPath(t, db, p2)); fp == "" {
		t.Error("no fingerprint for diverged dependencies — the base tip and the dependency tips are still known")
	}
	if _, err := s.Start(p2, "", ""); !errors.Is(err, ErrDepsUnmerged) {
		t.Errorf("Start = %v, want ErrDepsUnmerged", err)
	}
}

// TestBlockedGuardIgnoresOtherStates: only a BLOCKED row is guarded. A failed or
// partial run re-runs as it always did, fingerprint or not.
func TestBlockedGuardIgnoresOtherStates(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s, _, _ := blockPhase(t, db, p1)
	for _, state := range []string{"failed", "partial", "done", "idle"} {
		mustExec(t, db, `UPDATE epic_phases SET run_state=? WHERE id=?`, state, p1)
		info, err := s.loadPhase(p1)
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := os.ReadFile(info.DocPath)
		if err := s.checkBlockedUnchanged(info, baseResolution{}, string(doc), false); err != nil {
			t.Errorf("run_state=%s: guard refused with %v, want it to stay out of the way", state, err)
		}
	}
}

// compile-time: the real manager is what the daemon wires, and it can stack.
var _ StackingWorktrees = (*worktree.Manager)(nil)
