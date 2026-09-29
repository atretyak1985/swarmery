package phaserun

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// A phase whose account is below the quota floor is refused as itself — the
// *runcore.LowQuotaError the API renders 429 — and like a full pool it leaves
// nothing behind: no slot held, run_state untouched, nothing spawned.
func TestStart_RefusedWhenAccountQuotaIsLow(t *testing.T) {
	db, _, p1, _ := fixture(t)
	s := newTestService(db, &stubRunner{}, &stubWt{})
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error {
		return &runcore.LowQuotaError{Account: "work", Window: "five_hour", ResetsAt: "2027-01-01T00:00:00Z",
			PercentLeft: 2, Floor: 10}
	}

	_, err := s.Start(p1, "", "")
	if !errors.Is(err, runcore.ErrLowQuota) {
		t.Fatalf("err = %v, want runcore.ErrLowQuota", err)
	}
	var low *runcore.LowQuotaError
	if !errors.As(err, &low) || low.ResetsAt == "" {
		t.Errorf("refusal does not carry its evidence unwrapped: %v", err)
	}
	if s.Slots.IsActive(s.slotKey(p1)) || s.Slots.Count() != 0 {
		t.Errorf("slot held after a quota refusal (count=%d) — the gate must run before TryAcquire", s.Slots.Count())
	}
	if got := phaseRunState(t, db, p1); got != "idle" {
		t.Errorf("run_state = %q, want the untouched 'idle' — low quota is not a failed phase", got)
	}

	// Headroom is back: the very same Start succeeds.
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error { return nil }
	if _, err := s.Start(p1, "", ""); err != nil {
		t.Fatalf("Start after headroom returned: %v", err)
	}
}
