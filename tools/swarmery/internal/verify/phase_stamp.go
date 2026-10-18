package verify

import (
	"database/sql"
	"fmt"
	"log"
)

// stampPhaseVerdict writes a phase verdict onto the row it was graded for, or — when that
// row was re-keyed by wsingest.carryAcrossRenames while grading ran — onto its one
// successor: the row of the same workspace task carrying the graded run branch.
// Returns the id stamped (0 when none) so the caller can log a re-route.
//
// The lineage key is run_branch: the carry copies it 1:1 onto the successor and refuses
// ambiguous matches, so within one workspace task a branch names exactly one live row of
// a phase's lineage. The fallback still demands exactly one match — two rows claiming
// the branch, or a match only in another task, stamp nothing: a verdict on the wrong
// phase is worse than a verdict that only lives in verification_runs.
//
// Ordering against the ingest transaction needs no extra care. A stamp that lands
// before the re-key writes the old row, and the carry copies the verdict onto the
// successor; a stamp that lands after it misses the old id and follows the branch.
func stampPhaseVerdict(db *sql.DB, phaseID, workspaceTaskID int64, branch string, v Verdict, detail string) (int64, error) {
	const stamp = `UPDATE epic_phases SET verify_verdict=?, verify_detail=NULLIF(?, '') WHERE id=?`
	res, err := db.Exec(stamp, string(v), detail, phaseID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 1 {
		return phaseID, nil
	}

	if branch == "" {
		log.Printf("warn: verify: phase=%d verdict=%s not stamped — row gone and no run branch to follow",
			phaseID, v)
		return 0, nil
	}

	successor, matches, err := phaseByBranch(db, workspaceTaskID, branch)
	if err != nil {
		return 0, err
	}
	if matches != 1 {
		log.Printf("warn: verify: phase=%d verdict=%s not stamped — %d rows carry branch %s",
			phaseID, v, matches, branch)
		return 0, nil
	}
	if _, err := db.Exec(stamp, string(v), detail, successor); err != nil {
		return 0, err
	}
	log.Printf("verify: phase=%d verdict=%s re-routed to successor phase=%d (branch %s)",
		phaseID, v, successor, branch)
	return successor, nil
}

// phaseByBranch looks up the rows of one workspace task that carry a run branch. It reads
// at most two: one is the successor, and a second is all it takes to call the match
// ambiguous. The returned id is meaningful only when matches == 1.
func phaseByBranch(db *sql.DB, workspaceTaskID int64, branch string) (id int64, matches int, err error) {
	rows, err := db.Query(
		`SELECT id FROM epic_phases WHERE workspace_task_id=? AND run_branch=? LIMIT 2`,
		workspaceTaskID, branch)
	if err != nil {
		return 0, 0, fmt.Errorf("find successor of branch %s: %w", branch, err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&id); err != nil {
			return 0, 0, fmt.Errorf("find successor of branch %s: %w", branch, err)
		}
		matches++
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("find successor of branch %s: %w", branch, err)
	}
	return id, matches, nil
}
