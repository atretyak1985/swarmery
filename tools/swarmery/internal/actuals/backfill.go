package actuals

import (
	"errors"
	"fmt"
	"io"
)

// BackfillStats counts what a backfill did with every phase that has a run.
// Every run lands in exactly one bucket, so the counters sum to Scanned.
type BackfillStats struct {
	Scanned         int // phases with a recorded run session
	Recorded        int // rows written (or, with DryRun, that would have been)
	AlreadyRecorded int // a row for this run exists and Force was off
	InFlight        int // run still running / never finished
	NoBranch        int // no run_branch recorded
	NoStartPoint    int // no run_start_point (run predates migration 0057)
	NoRepo          int // the run's repository could not be resolved
	BranchGone      int // the run branch no longer exists (merged and deleted, or discarded)
	Failed          int // measurement failed for another reason
}

// Total is the sum of every bucket — equal to Scanned by construction.
func (s BackfillStats) Total() int {
	return s.Recorded + s.AlreadyRecorded + s.InFlight + s.NoBranch + s.NoStartPoint +
		s.NoRepo + s.BranchGone + s.Failed
}

// BackfillOptions steers a backfill.
type BackfillOptions struct {
	// RepoRoot resolves the repository a phase's run executed in (the daemon
	// passes phaserun.Service.RunRoot so the answer is the one the run got).
	RepoRoot func(phaseID int64) (string, error)
	// Force recomputes runs that already have a row.
	Force bool
	// DryRun computes but stores nothing.
	DryRun bool
	// Log, when set, receives one line per skipped or failed run.
	Log io.Writer
}

// Backfill measures, best effort, every past phase run the store still knows
// about. Only a phase's LATEST run is reachable — epic_phases keeps one run per
// phase, and each new run overwrites the last — and only a run whose branch
// still exists can be measured, because the branch is the diff. Everything else
// is skipped and counted, never guessed.
//
// A backfilled run was not observed ending, so its continuations are recorded
// only when run_events still hold rows for it (see Record's eventsRecorded).
func (r *Recorder) Backfill(opts BackfillOptions) (BackfillStats, error) {
	var st BackfillStats
	rows, err := r.DB.Query(`
		SELECT e.id, e.run_session_uuid, COALESCE(e.run_state, ''),
		       COALESCE(e.run_branch, ''), COALESCE(e.run_start_point, ''),
		       EXISTS (SELECT 1 FROM phase_actuals a WHERE a.session_uuid = e.run_session_uuid)
		  FROM epic_phases e
		 WHERE e.run_session_uuid IS NOT NULL AND e.run_session_uuid <> ''
		 ORDER BY e.id`)
	if err != nil {
		return st, err
	}
	type cand struct {
		id                         int64
		uuid, state, branch, start string
		recorded                   bool
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.uuid, &c.state, &c.branch, &c.start, &c.recorded); err != nil {
			rows.Close()
			return st, err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return st, err
	}

	logf := func(format string, args ...any) {
		if opts.Log != nil {
			fmt.Fprintf(opts.Log, format+"\n", args...)
		}
	}
	for _, c := range cands {
		st.Scanned++
		switch {
		case c.recorded && !opts.Force:
			st.AlreadyRecorded++
			continue
		case c.state == "" || c.state == "idle" || c.state == "running":
			st.InFlight++
			continue
		case c.branch == "":
			st.NoBranch++
			logf("phase %d: skipped, no run branch recorded", c.id)
			continue
		case c.start == "":
			st.NoStartPoint++
			logf("phase %d: skipped, no start point recorded for %s", c.id, c.branch)
			continue
		}
		var (
			root    string
			rootErr = errors.New("no resolver")
		)
		if opts.RepoRoot != nil {
			root, rootErr = opts.RepoRoot(c.id)
		}
		if root == "" || rootErr != nil {
			st.NoRepo++
			logf("phase %d: skipped, run repository unresolved: %v", c.id, rootErr)
			continue
		}
		if !BranchExists(r.Git, root, c.branch) {
			st.BranchGone++
			logf("phase %d: skipped, branch %s no longer exists in %s", c.id, c.branch, root)
			continue
		}
		a, err := r.Compute(c.id, c.uuid, root, false)
		if err != nil {
			if errors.Is(err, ErrNotFinished) {
				st.InFlight++
				continue
			}
			st.Failed++
			logf("phase %d: failed: %v", c.id, err)
			continue
		}
		if a.Files == nil {
			st.Failed++
			logf("phase %d: failed, diff not measurable: %s", c.id, a.DiffNote)
			continue
		}
		a.Source = SourceBackfill
		if !opts.DryRun {
			if err := r.Store(a); err != nil {
				st.Failed++
				logf("phase %d: store failed: %v", c.id, err)
				continue
			}
		}
		st.Recorded++
	}
	return st, nil
}
