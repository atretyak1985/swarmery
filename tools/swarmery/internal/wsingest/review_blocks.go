// Review findings as data (migration 0123). Every `kind: review` block of a phase
// doc's `## Review` section (mdfence.ParseReviews, D7/D42) — whoever wrote it: the
// daemon's review stage, run-plan's orchestrator, or an operator backfilling an
// old plan — is projected into one review_blocks row per (doc, round) and one
// review_findings row per finding. md stays the truth: a block removed from the
// doc removes its rows on the next scan.
//
// D21 ("legacy review"): a phase finished before this projection existed carries
// no block, and that is not a defect. A Done phase with a filled Completion Report
// dated before the migration's applied_at and no review block gets
// epic_phases.legacy_review = 1 — a badge on the Review tab, nothing more: no
// status change, no needs-you item.

package wsingest

import (
	"database/sql"
	"fmt"
	"log"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// Review block sources — review_blocks.source / review_findings.source.
const (
	ReviewSourceDaemon      = "daemon"
	ReviewSourceInteractive = "interactive"
	ReviewSourceBackfill    = "backfill"
)

// reviewSource normalizes a block's `source:`. Absent means interactive: the
// daemon always writes its own, and a backfill is told to.
func reviewSource(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case ReviewSourceDaemon, ReviewSourceInteractive, ReviewSourceBackfill:
		return v
	case "":
		return ReviewSourceInteractive
	default:
		log.Printf("warn: wsingest: unknown review block source %q — stored as %s", s, ReviewSourceInteractive)
		return ReviewSourceInteractive
	}
}

// findingID is the finding's own id, else r<round>-<index> (1-based, document
// order) — deterministic, so a rescan of an unchanged doc touches the same rows.
func findingID(round string, index int, id string) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	return fmt.Sprintf("r%s-%d", round, index+1)
}

// reportWrittenAtRe finds a `written_at:` stamp inside a Completion Report.
var reportWrittenAtRe = regexp.MustCompile(`(?mi)written_at:\s*["']?(\d{4}-\d{2}-\d{2}(?:[T ][0-9:.]+(?:Z|[+-]\d{2}:?\d{2})?)?)`)

// parseStamp reads an RFC3339 timestamp or a bare date; ok=false otherwise.
func parseStamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// reportDate is when the phase's Completion Report was written: its own
// `written_at:` when it carries one, else the doc's mtime (docUpdatedAt).
func reportDate(p epicPhase) (time.Time, bool) {
	if m := reportWrittenAtRe.FindStringSubmatch(p.completionReport); m != nil {
		if t, ok := parseStamp(m[1]); ok {
			return t, true
		}
	}
	return parseStamp(p.docUpdatedAt)
}

// phaseDone is D21's "Done": the doc says so, or every criterion is ticked.
func phaseDone(p epicPhase) bool {
	return p.docStatus == "done" || (p.checkboxesTotal > 0 && p.checkboxesDone == p.checkboxesTotal)
}

// reviewCutoff is D21's cutoff — migration 0123's applied_at. ok=false when the
// migration is not recorded (a test database built below it): then no phase is
// legacy, because nothing can be dated before a cutoff that does not exist.
func reviewCutoff(tx *sql.Tx) (time.Time, bool) {
	at, ok, err := store.MigrationAppliedAt(tx, store.ReviewFindingsMigration)
	if err != nil {
		log.Printf("warn: wsingest: review cutoff unreadable: %v — no phase is marked legacy this scan", err)
		return time.Time{}, false
	}
	return at, ok
}

// isLegacyReview applies D21. prev is the row's current legacy_review: once a
// phase is legacy it stays legacy while it has no block, so a later edit of an
// old doc (which moves its mtime past the cutoff) does not turn a finished phase
// into one that "lacks a review".
func isLegacyReview(p epicPhase, prev bool, cutoff time.Time, haveCutoff bool) bool {
	if len(p.reviews) > 0 || p.reviewErrs > 0 || !phaseDone(p) || p.completionReport == "" {
		return false
	}
	if prev {
		return true
	}
	if !haveCutoff {
		return false
	}
	at, ok := reportDate(p)
	return ok && at.Before(cutoff)
}

// applyReviewBlocks projects one phase doc's review blocks into review_blocks /
// review_findings and sets epic_phases.legacy_review. Runs after the phase's
// upsert, in applyEpics' transaction.
//
// A doc that did not resolve this scan (p.docRead false) is left alone: "could
// not read the doc" is not "the doc has no review", and deleting its rows on a
// transient read error would lose them until the next change.
func applyReviewBlocks(tx *sql.Tx, taskID int64, p epicPhase, cutoff time.Time, haveCutoff bool) error {
	if !p.docRead {
		return nil
	}
	var (
		phaseID int64
		prev    int
	)
	if err := tx.QueryRow(`SELECT id, legacy_review FROM epic_phases WHERE workspace_task_id = ? AND doc_path = ?`,
		taskID, p.docPath).Scan(&phaseID, &prev); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// resolvedBy: finding id → the round whose `resolves:` cites it. Ids are stable
	// across a doc's rounds (R1-1 is cited as R1-1 by round 2), so the map is keyed
	// by id alone; a round never resolves its own finding.
	resolvedBy := map[string]string{}
	for _, r := range p.reviews {
		for _, id := range r.Resolves {
			if _, seen := resolvedBy[id]; !seen {
				resolvedBy[id] = r.Round
			}
		}
	}

	keepBlocks := map[string]bool{}
	keepFindings := map[[2]string]bool{}
	for _, r := range p.reviews {
		source := reviewSource(r.Source)
		written := now
		if t, ok := parseStamp(r.WrittenAt); ok {
			written = t.Format(time.RFC3339)
		}
		scopeLeft, err := jsonList(r.ScopeLeft)
		if err != nil {
			return err
		}
		callers, err := jsonList(r.CheckedCallers)
		if err != nil {
			return err
		}
		resolves, err := jsonList(r.Resolves)
		if err != nil {
			return err
		}
		// written_at keeps its first value unless the block states its own.
		if _, err := tx.Exec(`
			INSERT INTO review_blocks
				(workspace_task_id, phase_id, doc_path, round, verdict, complete,
				 scope_left_json, checked_callers_json, resolves_json, unparsable, source, written_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(doc_path, round) DO UPDATE SET
				workspace_task_id    = excluded.workspace_task_id,
				phase_id             = excluded.phase_id,
				verdict              = excluded.verdict,
				complete             = excluded.complete,
				scope_left_json      = excluded.scope_left_json,
				checked_callers_json = excluded.checked_callers_json,
				resolves_json        = excluded.resolves_json,
				unparsable           = excluded.unparsable,
				source               = excluded.source,
				written_at           = CASE WHEN ? THEN excluded.written_at ELSE review_blocks.written_at END`,
			taskID, phaseID, p.docPath, r.Round, r.Verdict, boolInt(r.Complete),
			scopeLeft, callers, resolves, boolInt(r.Unparsable), source, written,
			r.WrittenAt != ""); err != nil {
			return fmt.Errorf("review block %s round %s: %w", p.docPath, r.Round, err)
		}
		keepBlocks[r.Round] = true

		for i, f := range r.Findings {
			fid := findingID(r.Round, i, f.ID)
			// The type column is CHECKed against the five lenses; a value outside
			// them is kept verbatim in the note rather than failing the whole scan.
			var typ any
			note := f.Note
			if slices.Contains(mdfence.FindingTypes, f.Type) {
				typ = f.Type
			} else if f.Type != "" {
				note = strings.TrimSpace("type: " + f.Type + "\n" + note)
			}
			var line any
			if f.Line > 0 {
				line = f.Line
			}
			var resolved any
			if by, ok := resolvedBy[fid]; ok && by != r.Round {
				resolved = by
			}
			// resolved_by is re-derived from the doc but never CLEARED by a scan: a
			// value set outside the doc (a later stage closing a finding) survives.
			if _, err := tx.Exec(`
				INSERT INTO review_findings
					(workspace_task_id, phase_id, doc_path, round, finding_id, type, file, line,
					 criterion, test_caught, evidence, note, resolved_by, source, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(doc_path, round, finding_id) DO UPDATE SET
					workspace_task_id = excluded.workspace_task_id,
					phase_id          = excluded.phase_id,
					type              = excluded.type,
					file              = excluded.file,
					line              = excluded.line,
					criterion         = excluded.criterion,
					test_caught       = excluded.test_caught,
					evidence          = excluded.evidence,
					note              = excluded.note,
					resolved_by       = COALESCE(excluded.resolved_by, review_findings.resolved_by),
					source            = excluded.source`,
				taskID, phaseID, p.docPath, r.Round, fid, typ, f.File, line,
				f.Criterion, boolInt(f.TestCaught), f.Evidence, note, resolved, source, now); err != nil {
				return fmt.Errorf("review finding %s round %s %s: %w", p.docPath, r.Round, fid, err)
			}
			keepFindings[[2]string{r.Round, fid}] = true
		}
	}

	if err := deleteMissingReviewRows(tx, p.docPath, keepBlocks, keepFindings); err != nil {
		return err
	}

	legacy := isLegacyReview(p, prev == 1, cutoff, haveCutoff)
	if legacy != (prev == 1) {
		if _, err := tx.Exec(`UPDATE epic_phases SET legacy_review = ? WHERE id = ?`, boolInt(legacy), phaseID); err != nil {
			return err
		}
	}
	return nil
}

// deleteMissingReviewRows removes the rows of blocks and findings the doc no
// longer carries.
func deleteMissingReviewRows(tx *sql.Tx, docPath string, keepBlocks map[string]bool, keepFindings map[[2]string]bool) error {
	type key struct{ round, fid string }
	var stale []key
	rows, err := tx.Query(`SELECT round, finding_id FROM review_findings WHERE doc_path = ?`, docPath)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.round, &k.fid); err != nil {
			rows.Close()
			return err
		}
		if !keepFindings[[2]string{k.round, k.fid}] {
			stale = append(stale, k)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, k := range stale {
		if _, err := tx.Exec(`DELETE FROM review_findings WHERE doc_path = ? AND round = ? AND finding_id = ?`,
			docPath, k.round, k.fid); err != nil {
			return err
		}
	}

	var staleRounds []string
	rows, err = tx.Query(`SELECT round FROM review_blocks WHERE doc_path = ?`, docPath)
	if err != nil {
		return err
	}
	for rows.Next() {
		var round string
		if err := rows.Scan(&round); err != nil {
			rows.Close()
			return err
		}
		if !keepBlocks[round] {
			staleRounds = append(staleRounds, round)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, round := range staleRounds {
		if _, err := tx.Exec(`DELETE FROM review_blocks WHERE doc_path = ? AND round = ?`, docPath, round); err != nil {
			return err
		}
		// A finding closed by a round that left the doc is open again.
		if _, err := tx.Exec(`UPDATE review_findings SET resolved_by = NULL WHERE doc_path = ? AND resolved_by = ?`,
			docPath, round); err != nil {
			return err
		}
	}
	return nil
}

// sweepOrphanReviewRows drops review rows of this task whose doc is no longer a
// phase of it — the prune in applyEpics is the one place a phase disappears.
func sweepOrphanReviewRows(tx *sql.Tx, taskID int64) error {
	for _, table := range []string{"review_findings", "review_blocks"} {
		if _, err := tx.Exec(`DELETE FROM `+table+`
			 WHERE workspace_task_id = ?
			   AND doc_path NOT IN (SELECT doc_path FROM epic_phases WHERE workspace_task_id = ?)`,
			taskID, taskID); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
