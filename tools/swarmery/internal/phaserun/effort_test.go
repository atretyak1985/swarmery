package phaserun

import (
	"errors"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
)

// The effort ladder, rung by rung. It mirrors resolveModel's, with one
// structural difference: rung 2 reads the doc BODY rather than a stamped column,
// because nothing renders an effort chip and a second doc-derived column would
// be storage for its own sake.

const docWithEffort = `# Phase 3 — something

**Covers:** SC-1
**Effort:** low

## Context

Body text. The agent prompt below quotes another phase's header, which must NOT
be read as this phase's declaration:

> **Effort:** max
`

func TestResolveEffort_RequestWins(t *testing.T) {
	t.Setenv(effortEnv, "medium")
	t.Setenv(claudeflags.EffortEnv, "")

	got, err := resolveEffort("max", docWithEffort, "/plan/phase-3.md")
	if err != nil {
		t.Fatalf("resolveEffort: %v", err)
	}
	if got != "max" {
		t.Errorf("effort = %q, want the request's %q", got, "max")
	}
}

func TestResolveEffort_DocBeatsEnv(t *testing.T) {
	t.Setenv(effortEnv, "medium")
	t.Setenv(claudeflags.EffortEnv, "")

	got, err := resolveEffort("", docWithEffort, "/plan/phase-3.md")
	if err != nil {
		t.Fatalf("resolveEffort: %v", err)
	}
	// "low" from the header block, NOT "max" from the quoted agent prompt below
	// it: every phase doc embeds a copy-paste prompt, and a header line quoted
	// inside one is describing someone else's phase.
	if got != "low" {
		t.Errorf("effort = %q, want the doc's header %q", got, "low")
	}
}

func TestResolveEffort_EnvBeatsDefault(t *testing.T) {
	t.Setenv(effortEnv, "medium")
	t.Setenv(claudeflags.EffortEnv, "")

	got, err := resolveEffort("", "# Phase 3\n\nno declaration here\n", "/plan/phase-3.md")
	if err != nil {
		t.Fatalf("resolveEffort: %v", err)
	}
	if got != "medium" {
		t.Errorf("effort = %q, want the env knob %q", got, "medium")
	}
}

func TestResolveEffort_FallsBackToDefault(t *testing.T) {
	t.Setenv(effortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	got, err := resolveEffort("", "# Phase 3\n\nnothing\n", "/plan/phase-3.md")
	if err != nil {
		t.Fatalf("resolveEffort: %v", err)
	}
	if got != DefaultEffort {
		t.Errorf("effort = %q, want %q", got, DefaultEffort)
	}
}

// A typo on the REQUEST is the operator's, so it names the closed set and is a
// 400 upstream. Nothing is started: resolveEffort runs before the slot, the
// worktree and any stamp.
func TestResolveEffort_UnknownRequestValueIsRejected(t *testing.T) {
	t.Setenv(effortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	_, err := resolveEffort("ludicrous", docWithEffort, "/plan/phase-3.md")
	if !errors.Is(err, planning.ErrUnknownEffort) {
		t.Fatalf("err = %v, want ErrUnknownEffort", err)
	}
	for _, want := range claudeflags.ValidEfforts() {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name the valid value %q", err, want)
		}
	}
}

// A typo in a DOCUMENT is the plan author's, so it names the FILE rather than
// the closed set — the same split DocModelError makes, and for the same reason:
// the fix is an edit to a document the operator never typed.
//
// It cannot be survived by falling back either: `claude --effort bogus` rejects
// the flag and the process dies before the run starts, so an unvalidated
// declaration would surface as an unexplained dead phase.
func TestResolveEffort_UnknownDocValueNamesTheDoc(t *testing.T) {
	t.Setenv(effortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	doc := "# Phase 3\n\n**Effort:** ludicrous\n\n## Context\n"
	_, err := resolveEffort("", doc, "/plan/phase-3.md")

	var docErr *DocEffortError
	if !errors.As(err, &docErr) {
		t.Fatalf("err = %v, want *DocEffortError", err)
	}
	if docErr.Doc != "/plan/phase-3.md" || docErr.Declared != "ludicrous" {
		t.Errorf("DocEffortError = %+v, want the doc path and the verbatim declaration", docErr)
	}
	if !strings.Contains(docErr.Error(), "/plan/phase-3.md") {
		t.Errorf("message %q does not name the document the author has to edit", docErr.Error())
	}
}

// A blank declaration is "no opinion", not a declaration: it must fall through
// to the env/default rung rather than be refused as "unknown effort ''".
func TestResolveEffort_BlankDocDeclarationFallsThrough(t *testing.T) {
	t.Setenv(effortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	got, err := resolveEffort("", "# Phase 3\n\n**Effort:** ``\n\n## Context\n", "/plan/phase-3.md")
	if err != nil {
		t.Fatalf("resolveEffort: %v", err)
	}
	if got != DefaultEffort {
		t.Errorf("effort = %q, want %q", got, DefaultEffort)
	}
}

// The spawn must actually receive it — a ladder that resolves correctly into a
// field nobody forwards is the shape of bug this phase exists to remove.
func TestStart_EffortReachesTheSpawn(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	t.Setenv(effortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", "low"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Effort; got != "low" {
		t.Errorf("RunSpec.Effort = %q, want %q", got, "low")
	}
}

// And the default case: an un-picked run must still carry a flag, because the
// alternative is not "cheap" — it is the CLI's xhigh for up to four hours.
func TestStart_UnpickedRunStillCarriesAnEffort(t *testing.T) {
	db, _, p1, _ := fixture(t)
	t.Setenv(modelEnv, "")
	t.Setenv(effortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().Effort; got != DefaultEffort {
		t.Errorf("RunSpec.Effort = %q, want %q", got, DefaultEffort)
	}
}
