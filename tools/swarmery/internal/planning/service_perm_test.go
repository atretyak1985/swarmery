package planning

import (
	"path/filepath"
	"reflect"
	"testing"
)

// A plan run may write under the workspace root the daemon scans — and nowhere
// else. The service names that dir; the runner turns it into the one Edit rule
// of the planner's allow-list (runner_perm_test.go).
func TestStart_WritableDirIsTheWorkspaceRoot(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newInlineService(t, db, r)
	s.WorkspaceRoot = "/ws/root"

	if _, err := s.Start(1, "add a widget", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().WritableDirs; !reflect.DeepEqual(got, []string{"/ws/root"}) {
		t.Errorf("WritableDirs = %q, want the workspace root only", got)
	}
}

// No configured root: nothing is writable. The plan write is then denied (and
// the wizard reports no PLAN SAVED dir) rather than the planner being handed
// the whole disk.
func TestStart_NoWorkspaceRootNoWritableDir(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newInlineService(t, db, r)

	if _, err := s.Start(1, "add a widget", "", ""); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := r.lastSpec().WritableDirs; len(got) != 0 {
		t.Errorf("WritableDirs = %q, want none without a workspace root", got)
	}
}

// A revise run stages into its scratch dir; the plan dir and the workspace root
// stay read-only for it (Apply is the daemon's job, later).
func TestStartRevise_WritableDirIsTheScratchDir(t *testing.T) {
	db := testDB(t)
	_ = revisePlanFixture(t, db)
	r := &stubRunner{}
	s := newInlineService(t, db, r)
	s.ScratchRoot = filepath.Join(t.TempDir(), "revisions")
	s.WorkspaceRoot = "/ws/root"

	uuid, err := s.StartRevise(reviseTaskID, "r", nil)
	if err != nil {
		t.Fatalf("StartRevise: %v", err)
	}
	want := []string{filepath.Join(s.ScratchRoot, uuid)}
	if got := r.lastSpec().WritableDirs; !reflect.DeepEqual(got, want) {
		t.Errorf("WritableDirs = %q, want the scratch dir %q only", got, want)
	}
	if got := s.WritableDirs(uuid); !reflect.DeepEqual(got, want) {
		t.Errorf("WritableDirs(%s) for the resume = %q, want %q", uuid, got, want)
	}
}

// The PROCEED turn — the one that writes the plan — is a resume, so the resume
// site has to recover the same writable dir from the wizard row.
func TestWritableDirs_PlanModeResumeGetsTheWorkspaceRoot(t *testing.T) {
	db := testDB(t)
	r := &stubRunner{}
	s := newInlineService(t, db, r)
	s.WorkspaceRoot = "/ws/root"
	uuid, err := s.Start(1, "idea", "", "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := s.WritableDirs(uuid); !reflect.DeepEqual(got, []string{"/ws/root"}) {
		t.Errorf("WritableDirs(%s) = %q, want the workspace root", uuid, got)
	}
	if got := s.WritableDirs("not-a-wizard"); len(got) != 0 {
		t.Errorf("WritableDirs(unknown) = %q, want none", got)
	}
}
