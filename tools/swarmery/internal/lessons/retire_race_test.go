package lessons

import (
	"testing"
	"time"
)

// An operator Keep that lands after the auto-retire pass read its due list must
// win: the lesson stays active and the pass does not count a retirement.
func TestKeepDuringTheAutoRetirePassWins(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	c := seedVerifyActive(t, db, ph, "c", "internal/c/**", fixedNow.Add(-90*24*time.Hour))
	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultVerifyConfig()
	q, _ := ListProposals(db, cfg, false)
	if len(q) != 1 {
		t.Fatalf("queue = %+v", q)
	}
	v := newVerifier(db, fixedNow.Add(15*24*time.Hour))
	v.afterDueList = func() {
		if _, err := KeepLesson(db, cfg, q[0].ID, fixedNow.Add(15*24*time.Hour)); err != nil {
			t.Fatalf("keep: %v", err)
		}
	}
	st, err := v.Run()
	if err != nil {
		t.Fatal(err)
	}
	if st.AutoRetired != 0 {
		t.Fatalf("auto-retired %d after the operator kept it", st.AutoRetired)
	}
	if l, _ := Get(db, c); l.Status != StatusActive {
		t.Fatalf("kept lesson was retired: %+v", l)
	}
}
