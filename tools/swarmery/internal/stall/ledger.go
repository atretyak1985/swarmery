package stall

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// tsFormat is the store's millisecond-Z timestamp layout.
const tsFormat = "2006-01-02T15:04:05.000Z"

// DefaultInterval is the detector's tick (D35).
const DefaultInterval = 60 * time.Second

// noticeKey is the ledger's identity: one row per (phase, reason).
type noticeKey struct {
	phaseID int64
	reason  Reason
}

// Result is what one Reconcile changed.
type Result struct {
	// Notified are the stalls newly inserted this tick — each emitted once.
	Notified []Stalled
	// Cleared counts ledger rows removed because their condition no longer holds.
	Cleared int
	// Tasks are the plans whose phase payload changed (a stall appeared or
	// cleared), for a plan_updated nudge. Context refreshes of a standing stall
	// are not listed: its minutes are computed by the reader from `since`.
	Tasks []int64
}

// Reconcile brings stall_notices in line with one tick's derivation:
//
//   - a stall with no row is INSERTed and handed to emit — the ONLY place the
//     phase_stalled webhook fires, so a standing stall notifies exactly once;
//   - a stall with a row keeps first_seen/notified_at and refreshes its context
//     (the stage, session or request behind the same reason may move on);
//   - a row whose (phase, reason) was not derived this tick is DELETEd, so the
//     same stall recurring later is a new insert and notifies again.
//
// emit may be nil (no notifier configured); notified_at then stays NULL.
func Reconcile(db *sql.DB, now time.Time, current []Stalled, emit func(Stalled)) (Result, error) {
	var res Result
	existing, err := ledgerKeys(db)
	if err != nil {
		return res, err
	}
	stamp := now.UTC().Format(tsFormat)
	touched := map[int64]bool{}
	derived := make(map[noticeKey]bool, len(current))

	for _, st := range current {
		k := noticeKey{st.PhaseID, st.Reason}
		derived[k] = true
		var req any
		if st.RequestID != 0 {
			req = st.RequestID
		}
		since := st.Since.UTC().Format(tsFormat)
		if _, ok := existing[k]; ok {
			if _, err := db.Exec(`
				UPDATE stall_notices SET stage = ?, session_uuid = ?, request_id = ?, since = ?
				 WHERE phase_id = ? AND reason = ?`,
				string(st.Stage), st.SessionUUID, req, since, st.PhaseID, string(st.Reason)); err != nil {
				return res, fmt.Errorf("stall: refresh notice phase=%d: %w", st.PhaseID, err)
			}
			continue
		}
		ins, err := db.Exec(`
			INSERT INTO stall_notices (phase_id, reason, stage, session_uuid, request_id, since, first_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (phase_id, reason) DO NOTHING`,
			st.PhaseID, string(st.Reason), string(st.Stage), st.SessionUUID, req, since, stamp)
		if err != nil {
			return res, fmt.Errorf("stall: insert notice phase=%d: %w", st.PhaseID, err)
		}
		if n, _ := ins.RowsAffected(); n == 0 {
			continue // another writer inserted it first; it owns the notification
		}
		res.Notified = append(res.Notified, st)
		touched[st.TaskID] = true
		if emit != nil {
			emit(st)
			if _, err := db.Exec(`UPDATE stall_notices SET notified_at = ? WHERE phase_id = ? AND reason = ?`,
				stamp, st.PhaseID, string(st.Reason)); err != nil {
				return res, fmt.Errorf("stall: stamp notified phase=%d: %w", st.PhaseID, err)
			}
		}
	}

	for k, taskID := range existing {
		if derived[k] {
			continue
		}
		if _, err := db.Exec(`DELETE FROM stall_notices WHERE phase_id = ? AND reason = ?`,
			k.phaseID, string(k.reason)); err != nil {
			return res, fmt.Errorf("stall: clear notice phase=%d: %w", k.phaseID, err)
		}
		res.Cleared++
		if taskID != 0 {
			touched[taskID] = true
		}
	}
	for id := range touched {
		res.Tasks = append(res.Tasks, id)
	}
	return res, nil
}

// ledgerKeys reads every ledger row with its plan (0 when the phase row is gone).
// Drained before Reconcile writes: the store runs on a single connection.
func ledgerKeys(db *sql.DB) (map[noticeKey]int64, error) {
	rows, err := db.Query(`
		SELECT n.phase_id, n.reason, COALESCE(e.workspace_task_id, 0)
		  FROM stall_notices n LEFT JOIN epic_phases e ON e.id = n.phase_id`)
	if err != nil {
		return nil, fmt.Errorf("stall: read ledger: %w", err)
	}
	defer rows.Close()
	out := map[noticeKey]int64{}
	for rows.Next() {
		var k noticeKey
		var reason string
		var taskID int64
		if err := rows.Scan(&k.phaseID, &reason, &taskID); err != nil {
			return nil, fmt.Errorf("stall: scan ledger: %w", err)
		}
		k.reason = Reason(reason)
		out[k] = taskID
	}
	return out, rows.Err()
}

// Ticker runs Detect + Reconcile on an interval (the daemon's 60 s tick).
type Ticker struct {
	DB     *sql.DB
	Config Config
	// Interval defaults to DefaultInterval.
	Interval time.Duration
	// Emit is called once per newly noticed stall (the phase_stalled webhook and
	// any live frame). nil: the ledger still tracks, nothing is announced.
	Emit func(Stalled)
	// Changed is told which plans gained or lost a stall, so their pages refetch.
	Changed func(taskID int64)
	// Now is the clock seam; nil means time.Now.
	Now func() time.Time
}

// Tick runs one detection pass.
func (t *Ticker) Tick() (Result, error) {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	at := now().UTC()
	stalls, err := Detect(t.DB, at, t.Config)
	if err != nil {
		return Result{}, err
	}
	res, err := Reconcile(t.DB, at, stalls, t.Emit)
	if err != nil {
		return res, err
	}
	if t.Changed != nil {
		for _, id := range res.Tasks {
			t.Changed(id)
		}
	}
	return res, nil
}

// Run ticks until ctx is cancelled; the first pass runs immediately, so a
// restart re-derives the ledger at once.
func (t *Ticker) Run(ctx context.Context) {
	interval := t.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	tick := func() {
		res, err := t.Tick()
		switch {
		case err != nil:
			log.Printf("error: stall detector: %v", err)
		case len(res.Notified)+res.Cleared > 0:
			log.Printf("stall detector: %d new stall(s), %d cleared", len(res.Notified), res.Cleared)
		}
	}
	tick()
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			tick()
		}
	}
}
