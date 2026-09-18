package phaserun

import (
	"errors"
	"testing"
)

// Gate order: a phase that is ALREADY RUNNING answers ErrRunning even when its
// doc's **Model:** line (or the request's model) is broken. A live run is the
// operator's real blocker; telling them to edit a document instead would send
// them chasing the wrong thing while the run keeps going.
func TestStart_AlreadyRunningOutranksABadModel(t *testing.T) {
	db, _, p1, _ := fixture(t)
	if _, err := db.Exec(`UPDATE epic_phases SET run_state='running', doc_model='gpt-9' WHERE id=?`, p1); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})

	for _, req := range []string{"", "gpt-9"} {
		if _, err := s.Start(p1, req); !errors.Is(err, ErrRunning) {
			t.Errorf("Start(model=%q) err = %v, want ErrRunning — the bad model masked the live run", req, err)
		}
	}
	if len(r.specs) != 0 {
		t.Errorf("runner was invoked %d time(s), want 0", len(r.specs))
	}
}
