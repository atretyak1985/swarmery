package phaserun

// Rung 2 of the phase-run model ladder: the model the phase DOCUMENT declares
// (`**Model:** opus`, parsed by wsingest.ParseModel into epic_phases.doc_model).
//
//	request model           → VALIDATED; unknown ⇒ 400, no run
//	DOC model               → VALIDATED; unknown ⇒ *DocModelError naming the doc, no run
//	SWARMERY_PHASERUN_MODEL → VERBATIM, never validated
//	neither                 → "" ⇒ no --model flag
//
// The property these tests exist to pin is that rung 2 is never SILENTLY IGNORED
// (internal/dispatch/service.go:979 records that bug for playbook `model:` chips)
// while rungs 1, 3 and 4 keep behaving exactly as phase 1 left them.
//
// They set epic_phases.doc_model directly rather than writing a header into the
// doc file: the column is what Start reads, and going through the parser here
// would be testing wsingest (TestParseModel does that) instead of the ladder.

import (
	"errors"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

func TestStart_DocModelUsedWhenRequestHasNone(t *testing.T) {
	db, _, p1, _ := fixture(t)
	// The env knob is set to something ELSE: rung 2 must outrank rung 3, not merge
	// with it. Without this the test would still pass on a ladder that skipped the doc.
	t.Setenv(modelEnv, "claude-opus-5[1m]")
	mustExec(t, db, `UPDATE epic_phases SET doc_model='sonnet' WHERE id=?`, p1)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := r.lastSpec().Model, planning.Models["sonnet"]; got != want {
		t.Errorf("RunSpec.Model = %q, want the doc's declaration resolved to %q", got, want)
	}
}

// A full ID in the doc is as valid as a short name — planning.ResolveModel accepts
// both, and the doc is the one place an author might paste a pinned ID.
func TestStart_DocModelAcceptsFullID(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	mustExec(t, db, `UPDATE epic_phases SET doc_model='claude-fable-5-1' WHERE id=?`, p1)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := r.lastSpec().Model, planning.Models["fable"]; got != want {
		t.Errorf("RunSpec.Model = %q, want %q", got, want)
	}
}

func TestStart_RequestModelOverridesDocModel(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	mustExec(t, db, `UPDATE epic_phases SET doc_model='sonnet' WHERE id=?`, p1)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "opus", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Model; got != planning.DefaultModel {
		t.Errorf("RunSpec.Model = %q, want the REQUEST's %q — rung 1 outranks rung 2",
			got, planning.DefaultModel)
	}
}

// A request model must win even when the doc's declaration is GARBAGE: the
// operator is overriding exactly the broken line, and refusing there would leave a
// plan with one bad header unrunnable until someone edits the file.
func TestStart_RequestModelWinsOverAnUnknownDocModel(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	mustExec(t, db, `UPDATE epic_phases SET doc_model='gpt-9' WHERE id=?`, p1)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "sonnet", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got, want := r.lastSpec().Model, planning.Models["sonnet"]; got != want {
		t.Errorf("RunSpec.Model = %q, want %q", got, want)
	}
}

// The rung-2 failure, asserted on the SEAMS the way phase 1's
// TestStart_UnknownRequestModelStartsNothing is: an error alone would also be
// returned by a rejection that had already acquired a worktree and stamped the row.
func TestStart_UnknownDocModelStartsNothing(t *testing.T) {
	db, _, p1, _ := fixture(t)
	// Set, so the test proves the run is REFUSED rather than quietly falling through
	// to the env knob — falling through IS the silent-ignore bug.
	t.Setenv(modelEnv, "claude-opus-5[1m]")
	mustExec(t, db, `UPDATE epic_phases SET doc_model='gpt-9' WHERE id=?`, p1)
	r := &stubRunner{}
	wt := &stubWt{}
	s := newTestService(db, r, wt)

	_, err := s.Start(p1, "", "")
	var docErr *DocModelError
	if !errors.As(err, &docErr) {
		t.Fatalf("Start err = %v (%T), want *DocModelError", err, err)
	}
	// Naming the DOCUMENT is the whole contract of this rung: the defect is in a
	// file the author owns, not in the request.
	var docPath string
	if qerr := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id=?`, p1).Scan(&docPath); qerr != nil {
		t.Fatal(qerr)
	}
	if docErr.Doc != docPath {
		t.Errorf("DocModelError.Doc = %q, want the phase doc %q", docErr.Doc, docPath)
	}
	if docErr.Declared != "gpt-9" {
		t.Errorf("DocModelError.Declared = %q, want the verbatim declaration %q", docErr.Declared, "gpt-9")
	}
	if !strings.Contains(docErr.Error(), docPath) {
		t.Errorf("error text %q does not name the document", docErr.Error())
	}
	// Still an unknown model underneath, so callers that only care about that keep
	// matching through the wrap.
	if !errors.Is(err, planning.ErrUnknownModel) {
		t.Errorf("errors.Is(err, planning.ErrUnknownModel) = false, want true through the wrap")
	}
	if len(r.specs) != 0 {
		t.Errorf("runner was invoked %d time(s), want 0", len(r.specs))
	}
	if len(wt.acquired) != 0 {
		t.Errorf("worktrees acquired = %v, want none", wt.acquired)
	}
	state, uuid, started, _ := phaseRow(t, db, p1)
	if state != "idle" || uuid.Valid || started.Valid {
		t.Errorf("phase row stamped: state=%q uuid=%v started=%v, want an untouched idle row", state, uuid, started)
	}
}

// Rung 3, unchanged: a phase whose doc declares nothing — every phase that exists
// today — still reaches the env knob byte-for-byte, `[1m]` suffix included.
func TestStart_NoDocModelFallsThroughToEnvVerbatim(t *testing.T) {
	db, _, p1, _ := fixture(t)
	const pinned = "claude-opus-5[1m]"
	t.Setenv(modelEnv, pinned)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	// doc_model deliberately left at its NULL default.
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Model; got != pinned {
		t.Errorf("RunSpec.Model = %q, want the env value verbatim %q", got, pinned)
	}
}

// A whitespace-only declaration is "no opinion", so the ladder falls THROUGH it
// to rung 4 — which is now planning.DefaultModel rather than no flag at all (see
// TestStart_NoModelAnywhereUsesTheHouseDefault for why). What this test still
// pins is the fall-through itself: a blank line must not be refused as "unknown
// model ''", and must not be treated as a declaration.
func TestStart_EmptyDocModelIsNoOpinion(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	// Whitespace, not NULL: an author who left the line in with nothing after it
	// stated no opinion, and must not be refused as "unknown model ''".
	mustExec(t, db, `UPDATE epic_phases SET doc_model='   ' WHERE id=?`, p1)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Model; got != planning.DefaultModel {
		t.Errorf("RunSpec.Model = %q, want the rung-4 default %q — a blank declaration must fall through, not declare",
			got, planning.DefaultModel)
	}
}
