package wsingest

// Decision evidence (verify-not-trust phase 12, D19, migration 0131). Every
// decision (`D-n`, phase_decisions) and spec criterion (`SC-n`, spec_criteria)
// has to survive to proof. Three sources say an id holds:
//
//   - a phase doc's `## Completion Report` cites it (kind 'report', this scan);
//     a line `not implemented: D-5 — <why>` records a deliberate omission instead
//     (status 'not-implemented', never "remaining");
//   - the plan-level review — a `kind: review` block with `round: plan` — maps it
//     to evidence or `missing` in its `ids:` list (kind 'review'); its `drift:`
//     list becomes `plan-conformance` review findings (review_blocks.go);
//   - kind 'pr' is reserved for landed change-request bodies, which landing does
//     not store yet (the body's report section IS the doc's report).
//
// LoadDecisionTable folds the declared ids, the phases that claim them and the
// evidence rows into the decision → phase → evidence table the plan tab, the
// change-request body and the plan review prompt all read. A plan without
// `## Decisions` and without a spec has an empty table: old plans are not
// rewritten.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
)

// Evidence statuses — decision_evidence.status.
const (
	EvidenceHeld           = "evidence"
	EvidenceNotImplemented = "not-implemented"
	EvidenceMissing        = "missing"
)

// Evidence kinds — decision_evidence.kind.
const (
	EvidenceKindReport = "report"
	EvidenceKindPR     = "pr"
	EvidenceKindReview = "review"
)

// Decision-table row statuses: the four chips of the plan tab.
const (
	RowEvidence       = "evidence"
	RowRemaining      = "remaining"
	RowNotImplemented = "not-implemented"
	RowNotVerified    = "not-verified"
)

// evidenceExcerptCap bounds one stored excerpt (a report line), in runes.
const evidenceExcerptCap = 240

var (
	// evidenceIDRe is one D-n / SC-n mention.
	evidenceIDRe = regexp.MustCompile(`\b(D|SC)-(\d+)\b`)
	// notImplementedRe is `not implemented: D-5 — <why>` (bold, dash or colon
	// variants tolerated: reports are hand-written).
	notImplementedRe = regexp.MustCompile(`(?i)not[ -]implemented:?\s*\**\s*\b(D|SC)-(\d+)\b\**\s*(?:—|–|-{1,2}|:)?\s*(.*)$`)
	// evidenceIDExactRe is a whole value that is one id (a plan review's ids[].id).
	evidenceIDExactRe = regexp.MustCompile(`^\s*\**\s*(D|SC)-(\d+)\s*\**\s*$`)
)

// EvidenceMention is one id a report evidences or deliberately skips.
type EvidenceMention struct {
	ID      string // canonical: "D-2", "SC-4"
	Status  string // EvidenceHeld | EvidenceNotImplemented
	Excerpt string // the line that says so; for not-implemented, the reason
}

// canonicalID renders prefix + number without leading zeros ("D-02" → "D-2");
// "" for a zero.
func canonicalID(prefix, digits string) string {
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return ""
	}
	return strings.ToUpper(prefix) + "-" + strconv.Itoa(n)
}

// CanonicalEvidenceID returns s as a canonical D-n / SC-n id, or "" when s is
// not exactly one id.
func CanonicalEvidenceID(s string) string {
	m := evidenceIDExactRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return canonicalID(m[1], m[2])
}

func capRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ScanEvidence reads a Completion Report (or a change-request body) for D-n /
// SC-n mentions, outside fenced blocks, in first-mention order. A `not
// implemented:` line wins over any other mention of the same id: the report
// says it was deliberately skipped, so it is never "remaining" and never
// "evidence". Pure; unit-tested.
func ScanEvidence(report string) []EvidenceMention {
	var out []EvidenceMention
	at := map[string]int{}
	forEachLineOutsideFences(report, func(_ int, line string) {
		if m := notImplementedRe.FindStringSubmatch(line); m != nil {
			if id := canonicalID(m[1], m[2]); id != "" {
				reason := capRunes(strings.Trim(strings.TrimSpace(m[3]), "*_"), evidenceExcerptCap)
				mention := EvidenceMention{ID: id, Status: EvidenceNotImplemented, Excerpt: reason}
				if i, ok := at[id]; ok {
					out[i] = mention
				} else {
					at[id] = len(out)
					out = append(out, mention)
				}
			}
		}
		for _, m := range evidenceIDRe.FindAllStringSubmatch(line, -1) {
			id := canonicalID(m[1], m[2])
			if id == "" {
				continue
			}
			if _, ok := at[id]; ok {
				continue
			}
			at[id] = len(out)
			out = append(out, EvidenceMention{ID: id, Status: EvidenceHeld, Excerpt: capRunes(line, evidenceExcerptCap)})
		}
	})
	return out
}

// applyEvidence folds every resolved phase doc's Completion Report into
// decision_evidence (kind 'report'): upsert on (task, id, kind, doc), drop the
// ids a report no longer cites, and drop the report rows of docs that left the
// plan. A doc that did not resolve this scan keeps its rows ("could not read" is
// not "cites nothing"). created_at keeps its first value.
func applyEvidence(tx *sql.Tx, taskID int64, phases []epicPhase) error {
	now := time.Now().UTC().Format(time.RFC3339)
	inPlan := map[string]bool{}
	for _, p := range phases {
		inPlan[p.docPath] = true
		if !p.docRead {
			continue
		}
		var phaseID any
		var id int64
		switch err := tx.QueryRow(`SELECT id FROM epic_phases WHERE workspace_task_id = ? AND doc_path = ?`,
			taskID, p.docPath).Scan(&id); err {
		case nil:
			phaseID = id
		case sql.ErrNoRows:
		default:
			return err
		}
		keep := map[string]bool{}
		for _, m := range ScanEvidence(p.completionReport) {
			if _, err := tx.Exec(`
				INSERT INTO decision_evidence
					(workspace_task_id, decision_id, phase_id, doc_path, kind, status, excerpt, created_at)
				VALUES (?, ?, ?, ?, 'report', ?, ?, ?)
				ON CONFLICT(workspace_task_id, decision_id, kind, doc_path) DO UPDATE SET
					phase_id = excluded.phase_id,
					status   = excluded.status,
					excerpt  = excluded.excerpt`,
				taskID, m.ID, phaseID, p.docPath, m.Status, m.Excerpt, now); err != nil {
				return fmt.Errorf("decision evidence %s %s: %w", p.docPath, m.ID, err)
			}
			keep[m.ID] = true
		}
		if err := deleteEvidenceExcept(tx, taskID, EvidenceKindReport, p.docPath, keep); err != nil {
			return err
		}
	}
	if err := sweepEvidence(tx, taskID, EvidenceKindReview, inPlan); err != nil {
		return err
	}
	return sweepEvidence(tx, taskID, EvidenceKindReport, inPlan)
}

// deleteEvidenceExcept drops the (task, kind, doc) rows whose id is not in keep.
func deleteEvidenceExcept(tx *sql.Tx, taskID int64, kind, docPath string, keep map[string]bool) error {
	rows, err := tx.Query(`SELECT decision_id FROM decision_evidence
		WHERE workspace_task_id = ? AND kind = ? AND doc_path = ?`, taskID, kind, docPath)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keep[id] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.Exec(`DELETE FROM decision_evidence
			WHERE workspace_task_id = ? AND kind = ? AND doc_path = ? AND decision_id = ?`,
			taskID, kind, docPath, id); err != nil {
			return err
		}
	}
	return nil
}

// sweepEvidence drops the task's kind rows that came from a phase doc no longer
// in the plan. Rows without a phase (the daemon's plan review, keyed by the
// README) are not phase-doc rows and are left alone.
func sweepEvidence(tx *sql.Tx, taskID int64, kind string, inPlan map[string]bool) error {
	rows, err := tx.Query(`SELECT DISTINCT doc_path FROM decision_evidence
		WHERE workspace_task_id = ? AND kind = ? AND phase_id IS NOT NULL`, taskID, kind)
	if err != nil {
		return err
	}
	var gone []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			return err
		}
		if !inPlan[d] {
			gone = append(gone, d)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range gone {
		if _, err := tx.Exec(`DELETE FROM decision_evidence WHERE workspace_task_id = ? AND kind = ? AND doc_path = ?`,
			taskID, kind, d); err != nil {
			return err
		}
	}
	return nil
}

// planRoundBlock is the latest `round: plan` block of reviews, or nil.
func planRoundBlock(reviews []mdfence.Review) *mdfence.Review {
	var out *mdfence.Review
	for i := range reviews {
		if reviews[i].Round == mdfence.ReviewRoundPlan {
			out = &reviews[i]
		}
	}
	return out
}

// PlanRoundBlock is planRoundBlock for a reviewer's raw output: the last
// parsable `kind: review` block with `round: plan`, or nil.
func PlanRoundBlock(output string) *mdfence.Review {
	rs, _ := mdfence.ParseReviews(output)
	return planRoundBlock(rs)
}

// driftFindings turns a plan round's drift entries into plan-conformance
// findings: the decision id as the criterion, where as the file (a trailing
// `:<line>` split off), how as the note.
func driftFindings(drift []mdfence.Drift) []mdfence.Finding {
	out := make([]mdfence.Finding, 0, len(drift))
	for i, d := range drift {
		file, line := splitFileLine(d.File)
		out = append(out, mdfence.Finding{
			ID:        fmt.Sprintf("drift-%d", i+1),
			Type:      "plan-conformance",
			File:      file,
			Line:      line,
			Criterion: d.Criterion,
			Note:      d.Note,
		})
	}
	return out
}

var fileLineRe = regexp.MustCompile(`^(.*\S):(\d+)$`)

func splitFileLine(s string) (string, int) {
	s = strings.TrimSpace(s)
	if m := fileLineRe.FindStringSubmatch(s); m != nil {
		if n, err := strconv.Atoi(m[2]); err == nil {
			return m[1], n
		}
	}
	return s, 0
}

// applyPlanReviewEvidence projects a plan round's `ids:` into decision_evidence
// (kind 'review') for docPath: an entry whose evidence is empty or `missing` is
// status 'missing', any other is 'evidence' with the evidence as excerpt. Entries
// whose id is not a D-n / SC-n are not decision evidence and are skipped. r nil
// retracts the doc's review rows.
func applyPlanReviewEvidence(tx *sql.Tx, taskID int64, phaseID any, docPath string, r *mdfence.Review) error {
	keep := map[string]bool{}
	if r != nil {
		now := time.Now().UTC().Format(time.RFC3339)
		for _, e := range r.IDs {
			id := CanonicalEvidenceID(e.ID)
			if id == "" || keep[id] {
				continue
			}
			status, excerpt := EvidenceHeld, strings.TrimSpace(e.Evidence)
			if excerpt == "" || strings.EqualFold(excerpt, EvidenceMissing) {
				status, excerpt = EvidenceMissing, ""
			}
			if _, err := tx.Exec(`
				INSERT INTO decision_evidence
					(workspace_task_id, decision_id, phase_id, doc_path, kind, status, excerpt, created_at)
				VALUES (?, ?, ?, ?, 'review', ?, ?, ?)
				ON CONFLICT(workspace_task_id, decision_id, kind, doc_path) DO UPDATE SET
					phase_id = excluded.phase_id,
					status   = excluded.status,
					excerpt  = excluded.excerpt`,
				taskID, id, phaseID, docPath, status, capRunes(excerpt, evidenceExcerptCap), now); err != nil {
				return fmt.Errorf("plan review evidence %s %s: %w", docPath, id, err)
			}
			keep[id] = true
		}
	}
	return deleteEvidenceExcept(tx, taskID, EvidenceKindReview, docPath, keep)
}

// ApplyPlanReview stores the daemon's plan branch review block — which the daemon
// writes into no doc — under the plan README's path: one review_blocks row
// (round plan, source daemon, no phase), its findings plus its drift as
// plan-conformance findings, and its ids as decision_evidence (kind 'review').
// A later daemon plan review of the same plan replaces it. A plan whose
// directory is not indexed is keyed by "plan-review:<taskID>".
func ApplyPlanReview(db *sql.DB, taskID int64, r mdfence.Review) error {
	var planDir string
	docPath := "plan-review:" + strconv.FormatInt(taskID, 10)
	if err := db.QueryRow(`SELECT path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`, taskID).
		Scan(&planDir); err == nil && planDir != "" {
		docPath = strings.TrimRight(planDir, "/") + "/README.md"
	}
	r.Round = mdfence.ReviewRoundPlan
	if r.Source == "" {
		r.Source = ReviewSourceDaemon
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a committed tx makes this a no-op
	now := time.Now().UTC().Format(time.RFC3339)
	keepFindings, err := upsertReviewBlock(tx, taskID, nil, docPath, r, map[string]string{}, now)
	if err != nil {
		return err
	}
	if err := deleteMissingReviewRows(tx, docPath, map[string]bool{mdfence.ReviewRoundPlan: true}, keepFindings); err != nil {
		return err
	}
	if err := applyPlanReviewEvidence(tx, taskID, nil, docPath, &r); err != nil {
		return err
	}
	return tx.Commit()
}

// ── the decision table ──────────────────────────────────────────────────────

// EvidenceRef is one decision_evidence row as the table shows it.
type EvidenceRef struct {
	Kind      string `json:"kind"`   // report | pr | review
	Status    string `json:"status"` // evidence | not-implemented | missing
	Excerpt   string `json:"excerpt"`
	PhaseSeq  int    `json:"phaseSeq"` // 0 for the daemon's plan review
	PhaseName string `json:"phaseName"`
}

// DecisionRow is one declared id with the phases that claim it and its evidence.
type DecisionRow struct {
	ID        string        `json:"id"`
	Kind      string        `json:"kind"` // decision | criterion
	Text      string        `json:"text"`
	Status    string        `json:"status"` // evidence | remaining | not-implemented | not-verified
	Reason    string        `json:"reason"` // the not-implemented reason; the not-verified class
	ClaimedBy []int         `json:"claimedBy"`
	Evidence  []EvidenceRef `json:"evidence"`
}

// PlanReviewState is the latest plan-level review of the plan, whoever ran it.
type PlanReviewState struct {
	// Status is "" (no plan review yet), "verified" (PASS or FAIL) or
	// "not-verified" (INCONCLUSIVE).
	Status      string `json:"status"`
	Verdict     string `json:"verdict"`
	ReasonClass string `json:"reasonClass"`
	At          string `json:"at"`
}

// Plan review statuses (PlanReviewState.Status).
const (
	PlanReviewVerified    = "verified"
	PlanReviewNotVerified = "not-verified"
)

// DecisionTable is GET /api/epics/{taskId}/evidence and what the change-request
// body and the plan review prompt read.
type DecisionTable struct {
	Rows       []DecisionRow   `json:"rows"`
	PlanReview PlanReviewState `json:"planReview"`
	Counts     map[string]int  `json:"counts"` // per row status
}

// LoadDecisionTable builds the plan's decision → phase → evidence table: the
// interview decisions (phase_decisions, source interview) in D-n order, then the
// spec criteria in spec order. Row status, in precedence order:
//
//  1. not-implemented — a report says so (a deliberate omission is never remaining);
//  2. the plan review's own word when it named the id: evidence, or remaining
//     for `missing` (verify, don't trust the report);
//  3. evidence — a report or change-request body cites it;
//  4. not-verified — nothing evidences it and the latest plan review was
//     INCONCLUSIVE, so "remaining" would claim a check that never happened;
//  5. remaining.
func LoadDecisionTable(db *sql.DB, taskID int64) (DecisionTable, error) {
	t := DecisionTable{Rows: []DecisionRow{}, Counts: map[string]int{}}

	rows, err := db.Query(`SELECT decision_id, question, answer FROM phase_decisions
		WHERE workspace_task_id = ? AND source = 'interview' ORDER BY seq, id`, taskID)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var id, q, a string
		if err := rows.Scan(&id, &q, &a); err != nil {
			rows.Close()
			return t, err
		}
		text := q
		if a = strings.TrimSpace(a); a != "" {
			text += " → " + a
		}
		t.Rows = append(t.Rows, DecisionRow{ID: id, Kind: "decision", Text: text})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	rows, err = db.Query(`SELECT cid, text FROM spec_criteria WHERE workspace_task_id = ? ORDER BY pos, id`, taskID)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return t, err
		}
		t.Rows = append(t.Rows, DecisionRow{ID: id, Kind: "criterion", Text: text})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	if t.PlanReview, err = LoadPlanReviewState(db, taskID); err != nil {
		return t, err
	}
	if len(t.Rows) == 0 {
		return t, nil
	}

	index := map[string]int{}
	for i := range t.Rows {
		index[t.Rows[i].ID] = i
		t.Rows[i].ClaimedBy = []int{}
		t.Rows[i].Evidence = []EvidenceRef{}
	}

	// Claims: a phase's `**Covers:**` (SC-n) and `**Depends on decisions:**` (D-n).
	rows, err = db.Query(`SELECT seq, covers, decision_deps FROM epic_phases
		WHERE workspace_task_id = ? ORDER BY seq, id`, taskID)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var (
			seq          int
			covers, deps string
		)
		if err := rows.Scan(&seq, &covers, &deps); err != nil {
			rows.Close()
			return t, err
		}
		for _, raw := range []string{covers, deps} {
			var ids []string
			_ = json.Unmarshal([]byte(raw), &ids) // a malformed list claims nothing
			for _, id := range ids {
				if i, ok := index[id]; ok && !containsInt(t.Rows[i].ClaimedBy, seq) {
					t.Rows[i].ClaimedBy = append(t.Rows[i].ClaimedBy, seq)
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	rows, err = db.Query(`SELECT de.decision_id, de.kind, de.status, de.excerpt,
			COALESCE(e.seq, 0), COALESCE(e.name, '')
		  FROM decision_evidence de
		  LEFT JOIN epic_phases e ON e.id = de.phase_id
		 WHERE de.workspace_task_id = ?
		 ORDER BY COALESCE(e.seq, 0), de.kind, de.id`, taskID)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var (
			id string
			r  EvidenceRef
		)
		if err := rows.Scan(&id, &r.Kind, &r.Status, &r.Excerpt, &r.PhaseSeq, &r.PhaseName); err != nil {
			rows.Close()
			return t, err
		}
		if i, ok := index[id]; ok {
			t.Rows[i].Evidence = append(t.Rows[i].Evidence, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	for i := range t.Rows {
		t.Rows[i].Status, t.Rows[i].Reason = rowStatus(t.Rows[i].Evidence, t.PlanReview)
		t.Counts[t.Rows[i].Status]++
	}
	return t, nil
}

// rowStatus applies LoadDecisionTable's precedence to one id's evidence.
func rowStatus(ev []EvidenceRef, pr PlanReviewState) (status, reason string) {
	var reviewSaid string
	held := false
	for _, e := range ev {
		switch {
		case e.Status == EvidenceNotImplemented:
			return RowNotImplemented, e.Excerpt
		case e.Kind == EvidenceKindReview:
			reviewSaid = e.Status
		case e.Status == EvidenceHeld:
			held = true
		}
	}
	switch {
	case reviewSaid == EvidenceHeld:
		return RowEvidence, ""
	case reviewSaid == EvidenceMissing:
		return RowRemaining, ""
	case held:
		return RowEvidence, ""
	case pr.Status == PlanReviewNotVerified:
		return RowNotVerified, pr.ReasonClass
	}
	return RowRemaining, ""
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// LoadPlanReviewState reads the newest plan-level review: the daemon's
// (phase_reviews, scope 'plan') or an interactive one (a `round: plan` block in a
// phase doc, review_blocks), whichever is later.
func LoadPlanReviewState(db *sql.DB, taskID int64) (PlanReviewState, error) {
	type cand struct{ verdict, reason, at string }
	var cs []cand

	var c cand
	switch err := db.QueryRow(`SELECT verdict, COALESCE(detail, ''), COALESCE(finished_at, started_at, '')
		  FROM phase_reviews WHERE scope = 'plan' AND workspace_task_id = ?
		 ORDER BY COALESCE(finished_at, started_at) DESC, id DESC LIMIT 1`,
		strconv.FormatInt(taskID, 10)).Scan(&c.verdict, &c.reason, &c.at); err {
	case nil:
		if i := strings.Index(c.reason, ":"); i >= 0 {
			c.reason = c.reason[:i]
		}
		cs = append(cs, c)
	case sql.ErrNoRows:
	default:
		return PlanReviewState{}, err
	}

	var scopeLeft string
	c = cand{}
	switch err := db.QueryRow(`SELECT verdict, scope_left_json, written_at FROM review_blocks
		WHERE workspace_task_id = ? AND round = 'plan' AND source <> 'daemon'
		ORDER BY written_at DESC, id DESC LIMIT 1`, taskID).Scan(&c.verdict, &scopeLeft, &c.at); err {
	case nil:
		var left []string
		_ = json.Unmarshal([]byte(scopeLeft), &left)
		if len(left) > 0 {
			c.reason = left[0]
		}
		cs = append(cs, c)
	case sql.ErrNoRows:
	default:
		return PlanReviewState{}, err
	}

	if len(cs) == 0 {
		return PlanReviewState{}, nil
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].at > cs[j].at })
	best := cs[0]
	st := PlanReviewState{Verdict: strings.ToUpper(best.verdict), At: best.at, Status: PlanReviewVerified}
	if st.Verdict == mdfence.ReviewInconclusive {
		st.Status = PlanReviewNotVerified
		st.ReasonClass = strings.TrimSpace(best.reason)
		if st.ReasonClass == "" {
			st.ReasonClass = "inconclusive"
		}
	}
	return st, nil
}
