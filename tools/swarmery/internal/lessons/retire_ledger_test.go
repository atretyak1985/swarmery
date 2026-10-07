package lessons

import (
	"testing"
	"time"
)

// The verifier's auto-retire goes through Retire, the same function the
// operator endpoint calls — and it must NOT leave an operator_corrections row
// (memory-engineering phase 3): the ledger is written at the endpoint, never
// inside Retire, so a daemon retiring its own lesson is not counted as a
// person correcting it. This pins that contract against a future "helpful"
// recorder inside Retire.
func TestAutoRetireWritesNoOperatorCorrection(t *testing.T) {
	db := openDB(t)
	ph := seedLessonPhase(t, db)
	c := seedVerifyActive(t, db, ph, "c", "internal/c/**", fixedNow.Add(-90*24*time.Hour))
	if _, err := newVerifier(db, fixedNow).Run(); err != nil {
		t.Fatal(err)
	}
	st, err := newVerifier(db, fixedNow.Add(15*24*time.Hour)).Run()
	if err != nil || st.AutoRetired != 1 {
		t.Fatalf("day 15 = %+v, %v (want one auto-retire)", st, err)
	}
	if l, _ := Get(db, c); l.Status != StatusRetired {
		t.Fatalf("lesson after auto-retire = %+v, want retired", l)
	}
	// The direct domain call the endpoint wraps writes none either.
	d := seedVerifyActive(t, db, ph, "d", "internal/d/**", fixedNow)
	if _, err := Retire(db, d, "by hand", fixedNow); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operator_corrections`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("operator_corrections rows after auto-retire + Retire = %d, want 0 (the ledger is the endpoint's job)", n)
	}
}
