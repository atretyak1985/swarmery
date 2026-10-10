package landpoll

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The [LAND] tick on merge (phase-run outcomes plan, phase 3, D3), driven by the
// same fake provider as landpoll_test.go: a phase's PR goes open → merged and the
// doc on disk is checked line by line.

// landDoc is a phase doc with one ticked criterion, one open executable
// criterion, two open [LAND] criteria, one open [MANUAL] one and a fenced
// [LAND] example that is not a criterion.
const landDoc = "# Phase 1\n\n## Acceptance Criteria\n\n" +
	"- [x] the endpoint exists\n" +
	"- [ ] the test covers the 404\n" +
	"- [ ] [LAND] push the branch and open the PR\n" +
	"- [ ] [LAND] merge the PR\n" +
	"- [ ] [MANUAL] check the production console\n\n" +
	"```md\n- [ ] [LAND] an example inside a fence\n```\n"

// landDocMerged is landDoc after the merge: only the two unfenced [LAND] lines flip.
const landDocMerged = "# Phase 1\n\n## Acceptance Criteria\n\n" +
	"- [x] the endpoint exists\n" +
	"- [ ] the test covers the 404\n" +
	"- [x] [LAND] push the branch and open the PR\n" +
	"- [x] [LAND] merge the PR\n" +
	"- [ ] [MANUAL] check the production console\n\n" +
	"```md\n- [ ] [LAND] an example inside a fence\n```\n"

// docPhase is h.phase with a real doc on disk at the row's doc_path.
func (h *harness) docPhase(seq int, state string, n int, body string) (int64, string) {
	h.t.Helper()
	id := h.phase(seq, state, "github", n, "")
	path := filepath.Join(h.dir, fmt.Sprintf("phase-%d.md", seq))
	if err := os.WriteFile(path, []byte(body), 0o640); err != nil {
		h.t.Fatal(err)
	}
	h.exec(`UPDATE epic_phases SET doc_path = ? WHERE id = ?`, path, id)
	return id, path
}

func readDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunOnceMergedTicksLandCriteriaOnly(t *testing.T) {
	h := newHarness(t)
	id, path := h.docPhase(1, StatePROpen, 14, landDoc)

	// Still open: nothing is ticked.
	h.reply(14, reply{out: prJSON("OPEN", "", "REVIEW_REQUIRED", runPending)})
	h.runOnce()
	if got := readDoc(t, path); got != landDoc {
		t.Fatalf("an open PR touched the doc:\n%s", got)
	}

	// The open → merged transition ticks exactly the two unfenced [LAND] lines.
	h.now = t0.Add(time.Minute)
	h.reply(14, reply{out: prJSON("MERGED", "2026-10-09T12:00:30Z", "APPROVED", runOK)})
	if _, changed := h.runOnce(); changed != 1 {
		t.Fatalf("merge: changed = %d", changed)
	}
	if l := h.landing(id); l.state != StateMerged {
		t.Fatalf("landing_state = %q, want merged", l.state)
	}
	if got := readDoc(t, path); got != landDocMerged {
		t.Fatalf("doc after merge:\n%s\nwant:\n%s", got, landDocMerged)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o640 {
		t.Errorf("doc mode after the tick: %v %v", st.Mode().Perm(), err)
	}
	wantLog := fmt.Sprintf("landpoll: phase %d (task %d) merged: ticked 2 [LAND] criteria", id, h.taskID)
	if len(h.logs) != 1 || h.logs[0] != wantLog {
		t.Fatalf("logs = %q, want [%q]", h.logs, wantLog)
	}
	// One publish for the first read, one for the merge — the merge's
	// plan_updated is what makes the scanner fold the new count in.
	if h.publishedCount() != 2 {
		t.Errorf("published = %v, want 2", h.published)
	}
}

func TestRefreshOneMergedTicksLandCriteriaOnce(t *testing.T) {
	h := newHarness(t)
	id, path := h.docPhase(1, StatePROpen, 15, landDoc)
	h.reply(15, reply{out: prJSON("MERGED", "x", "APPROVED", runOK)})
	if _, state, err := h.poller.RefreshOne(context.Background(), id); err != nil || state != StateMerged {
		t.Fatalf("RefreshOne = %q %v", state, err)
	}
	if got := readDoc(t, path); got != landDocMerged {
		t.Fatalf("doc after a refresh merge:\n%s", got)
	}
	// A merged phase re-read later never ticks again: the tick belongs to the
	// transition, and the operator may have unticked a [LAND] line on purpose.
	if err := os.WriteFile(path, []byte(landDoc), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.poller.RefreshOne(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if got := readDoc(t, path); got != landDoc {
		t.Fatalf("a merged re-read ticked again:\n%s", got)
	}
	if len(h.logs) != 1 {
		t.Errorf("logs = %q, want the one merge line", h.logs)
	}
}

func TestRunOnceMergedWithoutDocIsQuiet(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 17, "") // doc_path /plan/phase-1.md does not exist
	h.reply(17, reply{out: prJSON("MERGED", "x", "", runOK)})
	h.runOnce()
	if l := h.landing(id); l.state != StateMerged {
		t.Fatalf("landing_state = %q", l.state)
	}
	if len(h.logs) != 0 {
		t.Errorf("a missing doc logged: %q", h.logs)
	}
}

func TestRunOnceMergedTickFailureIsLoggedNotFatal(t *testing.T) {
	h := newHarness(t)
	id := h.phase(1, StatePROpen, "github", 16, "")
	// doc_path is a directory: stat succeeds, the read fails.
	h.exec(`UPDATE epic_phases SET doc_path = ? WHERE id = ?`, h.dir, id)
	h.reply(16, reply{out: prJSON("MERGED", "x", "", runOK)})
	if checked, changed := h.runOnce(); checked != 1 || changed != 1 {
		t.Fatalf("checked=%d changed=%d", checked, changed)
	}
	if l := h.landing(id); l.state != StateMerged || l.landingError.Valid {
		t.Fatalf("a failed tick undid the merge: %+v", l)
	}
	if len(h.logs) != 1 || !strings.Contains(h.logs[0], "ticking its [LAND] criteria failed") {
		t.Fatalf("logs = %q", h.logs)
	}
}
