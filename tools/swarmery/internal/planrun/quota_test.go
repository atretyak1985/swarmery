package planrun

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

// A plan whose account is below the quota floor is refused as itself — the
// *runcore.LowQuotaError the API renders 429 — and like a full pool it leaves
// nothing behind: no slot held, no plan_runs row, nothing spawned.
func TestStart_RefusedWhenAccountQuotaIsLow(t *testing.T) {
	db, taskID, _ := fixture(t)
	r := &stubRunner{}
	s := newTestService(db, r, &stubWt{})
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error {
		return &runcore.LowQuotaError{Account: "work", Window: "seven_day", ResetsAt: "2027-01-05T00:00:00Z",
			PercentLeft: 3, Floor: 10}
	}

	_, err := s.Start(taskID, "", "")
	if !errors.Is(err, runcore.ErrLowQuota) {
		t.Fatalf("err = %v, want runcore.ErrLowQuota", err)
	}
	var low *runcore.LowQuotaError
	if !errors.As(err, &low) || low.ResetsAt == "" {
		t.Errorf("refusal does not carry its evidence unwrapped: %v", err)
	}
	if s.Slots.IsActive(s.slotKey(taskID)) || s.Slots.Count() != 0 {
		t.Errorf("slot held after a quota refusal (count=%d) — the gate must run before TryAcquire", s.Slots.Count())
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0 — a quota refusal must not stamp a run", n)
	}

	// Headroom is back: the very same Start succeeds.
	s.QuotaCheck = func(*sql.DB, claudeacct.Resolution, time.Time) error { return nil }
	if _, err := s.Start(taskID, "", ""); err != nil {
		t.Fatalf("Start after headroom returned: %v", err)
	}
}
