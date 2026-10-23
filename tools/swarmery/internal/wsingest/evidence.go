package wsingest

// Decision evidence (verify-not-trust phase 12, D19, migration 0132). Every
// decision (D-n) and spec criterion (SC-n) has to survive to proof:
//
//   - a phase's `## Completion Report` cites the ids it satisfies — any `D-n` or
//     `SC-n` token is evidence (kind 'report');
//   - a decision the phase deliberately does not implement is written as
//     `not implemented: D-n — <why>` — status 'not-implemented' with the why as
//     its reason, never counted as remaining;
//   - the plan review's `round: plan` block maps each id to the phase/commit that
//     implements it, or to "missing" (kind 'review'); its drift entries become
//     plan-conformance review findings (applyReviewBlocks / ApplyPlanReview).
//
// md stays the truth: every scan re-derives a doc's rows and prunes the ones its
// doc no longer states. A daemon plan review is written into no doc, so its rows
// live under PlanReviewDocPath and are replaced whole by the next one.

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

// Evidence kinds — decision_evidence.kind.
const (
	EvidenceKindReport = "report"
	EvidenceKindPR     = "pr" // reserved: landing PR bodies are rendered from the report, not stored
	EvidenceKindReview = "review"
)

// Evidence statuses — decision_evidence.status.
const (
	EvidenceStatusEvidence       = "evidence"
	EvidenceStatusNotImplemented = "not-implemented"
	EvidenceStatusMissing        = "missing"
)

// evidenceExcerptMax bounds an excerpt, in runes.
const evidenceExcerptMax = 240

var (
	// evidenceIDRe is the id token: D-n or SC-n, upper case, on word boundaries.
	evidenceIDRe = regexp.MustCompile(`\b(D|SC)-(\d+)\b`)
	// notImplementedRe is `not implemented: D-5 — <why>`; the separator after the
	// id may be an em/en dash, a hyphen run or a colon, or absent.
	notImplementedRe = regexp.MustCompile(`(?i)\bnot\s+implemented\s*:\s*\**\s*\b(D|SC)-(\d+)\b\**\s*(?:—|–|-{1,2}|:)?\s*(.*)$`)
	// refIDRe validates a stand-alone id (a review block's ids[].id).
	refIDRe = regexp.MustCompile(`(?i)^\s*(D|SC)-0*(\d+)\s*$`)
	// evidencePhaseRe reads the phase seq out of a review's evidence ("phase 3 @ abc1234").
	evidencePhaseRe = regexp.MustCompile(`(?i)\bphase\s+(\d+)\b`)
)

// EvidenceMention is one id a text cites.
type EvidenceMention struct {
	ID      string // "D-2", "SC-4"
	Status  string // EvidenceStatusEvidence | EvidenceStatusNotImplemented
	Reason  string // the why of a `not implemented:` line; "" otherwise
	Excerpt string // the line it was cited on, trimmed and capped
}

// refID renders a normalized id ("D-05" → "D-5").
func refID(prefix, digits string) string {
	n, err := strconv.Atoi(digits)
	if err != nil {
		return ""
	}
	return strings.ToUpper(prefix) + "-" + strconv.Itoa(n)
}

// NormalizeRefID returns s as a D-n / SC-n id, or "" when it is not one.
func NormalizeRefID(s string) string {
	m := refIDRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return refID(m[1], m[2])
}

// excerpt collapses whitespace and caps s at evidenceExcerptMax runes.
func excerpt(s string) string {
	s = oneLine(s)
	if utf8.RuneCountInString(s) <= evidenceExcerptMax {
		return s
	}
	r := []rune(s)
	return string(r[:evidenceExcerptMax-1]) + "…"
}

// ScanEvidence returns the ids text cites, one mention per id in first-seen
// order. Fenced blocks are skipped (a quoted prompt is not a claim). A
// `not implemented: D-n — <why>` line marks D-n not-implemented — it wins over
// any plain mention of the same id — and the other ids on that line count for
// nothing (the why may name the decision that superseded it). Pure.
func ScanEvidence(text string) []EvidenceMention {
	var out []EvidenceMention
	at := map[string]int{}
	forEachLineOutsideFences(text, func(_ int, line string) {
		if m := notImplementedRe.FindStringSubmatch(line); m != nil {
			id := refID(m[1], m[2])
			if id == "" {
				return
			}
			mention := EvidenceMention{ID: id, Status: EvidenceStatusNotImplemented,
				Reason: excerpt(strings.TrimSpace(m[3])), Excerpt: excerpt(line)}
			if i, ok := at[id]; ok {
				if out[i].Status != EvidenceStatusNotImplemented {
					out[i] = mention
				}
				return
			}
			at[id] = len(out)
			out = append(out, mention)
			return
		}
		for _, m := range evidenceIDRe.FindAllStringSubmatch(line, -1) {
			id := refID(m[1], m[2])
			if id == "" {
				continue
			}
			if _, ok := at[id]; ok {
				continue
			}
			at[id] = len(out)
			out = append(out, EvidenceMention{ID: id, Status: EvidenceStatusEvidence, Excerpt: excerpt(line)})
		}
	})
	return out
}

// ReviewIDEvidence is one `round: plan` ids[] entry, normalized.
type ReviewIDEvidence struct {
	ID       string
	Status   string // EvidenceStatusEvidence | EvidenceStatusMissing
	Excerpt  string // the reviewer's evidence text
	PhaseSeq int    // the phase the evidence names; 0 when none
}

// PlanReviewIDs maps a plan-round block's ids[] onto evidence. Entries whose id
// is not a D-n/SC-n are dropped (a phase round lists symbols there); an evidence
// of "missing" (or none) is status missing. The first entry per id wins. Pure.
func PlanReviewIDs(r mdfence.Review) []ReviewIDEvidence {
	if !strings.EqualFold(r.Round, mdfence.ReviewRoundPlan) {
		return nil
	}
	var out []ReviewIDEvidence
	seen := map[string]bool{}
	for _, e := range r.IDs {
		id := NormalizeRefID(e.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ev := strings.TrimSpace(e.Evidence)
		row := ReviewIDEvidence{ID: id, Status: EvidenceStatusEvidence, Excerpt: excerpt(ev)}
		if ev == "" || strings.EqualFold(strings.Trim(ev, " .`*_"), "missing") {
			row.Status = EvidenceStatusMissing
		}
		if m := evidencePhaseRe.FindStringSubmatch(ev); m != nil {
			row.PhaseSeq, _ = strconv.Atoi(m[1])
		}
		out = append(out, row)
	}
	return out
}

// PlanReviewDocPath is the synthetic doc_path a daemon plan review's rows carry:
// the review is written into no doc, and review_findings / decision_evidence are
// keyed by doc.
func PlanReviewDocPath(taskID int64) string {
	return "plan-review:" + strconv.FormatInt(taskID, 10)
}

// planDriftFindingID is the review_findings.finding_id of a plan round's i-th
// drift entry (0-based); distinct from any finding id the block carries.
func planDriftFindingID(i int) string { return fmt.Sprintf("drift-%d", i+1) }

// evidenceRow is one decision_evidence row to write.
type evidenceRow struct {
	refID, status, reason, excerpt string
	phaseID                        any // int64 or nil
}

// execer is the part of *sql.Tx / *sql.DB the writers need.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// putEvidence replaces the (task, doc, kind) row set with rows: upserts each on
// its unique key (created_at keeps its first value) and deletes what rows no
// longer state. A rescan of an unchanged doc changes nothing.
func putEvidence(tx execer, taskID int64, docPath, kind string, rows []evidenceRow) error {
	now := time.Now().UTC().Format(time.RFC3339)
	keep := map[string]bool{}
	for _, r := range rows {
		if _, err := tx.Exec(`
			INSERT INTO decision_evidence
				(workspace_task_id, ref_id, phase_id, doc_path, kind, status, reason, excerpt, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(workspace_task_id, doc_path, kind, ref_id) DO UPDATE SET
				phase_id = excluded.phase_id,
				status   = excluded.status,
				reason   = excluded.reason,
				excerpt  = excluded.excerpt`,
			taskID, r.refID, r.phaseID, docPath, kind, r.status, r.reason, r.excerpt, now); err != nil {
			return fmt.Errorf("decision evidence %s %s %s: %w", docPath, kind, r.refID, err)
		}
		keep[r.refID] = true
	}
	q, err := tx.Query(`SELECT ref_id FROM decision_evidence WHERE workspace_task_id = ? AND doc_path = ? AND kind = ?`,
		taskID, docPath, kind)
	if err != nil {
		return err
	}
	var stale []string
	for q.Next() {
		var id string
		if err := q.Scan(&id); err != nil {
			q.Close()
			return err
		}
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.Exec(`DELETE FROM decision_evidence WHERE workspace_task_id = ? AND doc_path = ? AND kind = ? AND ref_id = ?`,
			taskID, docPath, kind, id); err != nil {
			return err
		}
	}
	return nil
}

// phaseIDBySeq resolves a plan's phase seq to its epic_phases id; nil when none.
func phaseIDBySeq(tx execer, taskID int64, seq int) any {
	if seq <= 0 {
		return nil
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND seq = ? ORDER BY id LIMIT 1`,
		taskID, seq).Scan(&id); err != nil {
		return nil
	}
	return id
}

// reviewEvidenceRows turns a plan block's ids[] into rows.
func reviewEvidenceRows(tx execer, taskID int64, r mdfence.Review) []evidenceRow {
	var rows []evidenceRow
	for _, e := range PlanReviewIDs(r) {
		rows = append(rows, evidenceRow{refID: e.ID, status: e.Status, excerpt: e.Excerpt,
			phaseID: phaseIDBySeq(tx, taskID, e.PhaseSeq)})
	}
	return rows
}

// applyEvidence projects one phase doc's Completion Report mentions (kind
// report) and its `round: plan` blocks' ids[] (kind review) into
// decision_evidence. Runs after the phase's upsert, in applyEpics' transaction.
// A doc that did not resolve this scan is left alone, like applyReviewBlocks.
func applyEvidence(tx *sql.Tx, taskID int64, p epicPhase) error {
	if !p.docRead {
		return nil
	}
	var phaseID int64
	if err := tx.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND doc_path = ?`,
		taskID, p.docPath).Scan(&phaseID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	var report []evidenceRow
	for _, m := range ScanEvidence(p.completionReport) {
		report = append(report, evidenceRow{refID: m.ID, status: m.Status, reason: m.Reason, excerpt: m.Excerpt, phaseID: phaseID})
	}
	if err := putEvidence(tx, taskID, p.docPath, EvidenceKindReport, report); err != nil {
		return err
	}
	var review []evidenceRow
	seen := map[string]bool{}
	// The latest plan round in the doc wins for an id it states twice.
	for i := len(p.reviews) - 1; i >= 0; i-- {
		for _, row := range reviewEvidenceRows(tx, taskID, p.reviews[i]) {
			if !seen[row.refID] {
				seen[row.refID] = true
				review = append(review, row)
			}
		}
	}
	return putEvidence(tx, taskID, p.docPath, EvidenceKindReview, review)
}

// ApplyPlanReview stores a daemon plan review's `round: plan` block: its ids[]
// as decision_evidence (kind review) and its drift[] as plan-conformance
// review_findings, both under PlanReviewDocPath(taskID). A nil block (an
// inconclusive review, a reviewer that wrote none) clears the previous review's
// rows: the table must not keep claiming evidence the latest review did not
// confirm. One transaction.
func ApplyPlanReview(db *sql.DB, taskID int64, r *mdfence.Review) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	docPath := PlanReviewDocPath(taskID)
	var rows []evidenceRow
	if r != nil {
		rows = reviewEvidenceRows(tx, taskID, *r)
	}
	if err := putEvidence(tx, taskID, docPath, EvidenceKindReview, rows); err != nil {
		return err
	}
	// Upsert then prune, never delete-and-reinsert: a review lesson cites findings
	// by their row id (lessons.FindingRef), and an unchanged drift must keep it.
	keep := []any{docPath}
	marks := []string{}
	if r != nil {
		now := time.Now().UTC().Format(time.RFC3339)
		for i, d := range r.Drift {
			if err := insertDriftFinding(tx, taskID, nil, docPath, mdfence.ReviewRoundPlan, i, d, ReviewSourceDaemon, now); err != nil {
				return err
			}
			keep = append(keep, planDriftFindingID(i))
			marks = append(marks, "?")
		}
	}
	q := `DELETE FROM review_findings WHERE doc_path = ?`
	if len(marks) > 0 {
		q += ` AND finding_id NOT IN (` + strings.Join(marks, ",") + `)`
	}
	if _, err := tx.Exec(q, keep...); err != nil {
		return err
	}
	return tx.Commit()
}

// insertDriftFinding upserts one drift entry as a plan-conformance finding: the
// decision id in criterion, the where in file, the how in note.
func insertDriftFinding(tx execer, taskID int64, phaseID any, docPath, round string, i int, d mdfence.Drift, source, now string) error {
	_, err := tx.Exec(`
		INSERT INTO review_findings
			(workspace_task_id, phase_id, doc_path, round, finding_id, type, file, line,
			 criterion, test_caught, evidence, note, resolved_by, source, created_at)
		VALUES (?, ?, ?, ?, ?, 'plan-conformance', ?, NULL, ?, 0, '', ?, NULL, ?, ?)
		ON CONFLICT(doc_path, round, finding_id) DO UPDATE SET
			workspace_task_id = excluded.workspace_task_id,
			phase_id          = excluded.phase_id,
			type              = excluded.type,
			file              = excluded.file,
			criterion         = excluded.criterion,
			note              = excluded.note,
			source            = excluded.source`,
		taskID, phaseID, docPath, round, planDriftFindingID(i), d.File, d.Criterion, d.Note, source, now)
	if err != nil {
		return fmt.Errorf("drift finding %s round %s #%d: %w", docPath, round, i+1, err)
	}
	return nil
}

// sweepOrphanEvidence drops this task's evidence rows whose doc is no longer a
// phase of it — the daemon plan review's synthetic doc excepted.
func sweepOrphanEvidence(tx *sql.Tx, taskID int64) error {
	_, err := tx.Exec(`DELETE FROM decision_evidence
		 WHERE workspace_task_id = ?
		   AND doc_path <> ?
		   AND doc_path NOT IN (SELECT doc_path FROM epic_phases WHERE workspace_task_id = ?)`,
		taskID, PlanReviewDocPath(taskID), taskID)
	return err
}
