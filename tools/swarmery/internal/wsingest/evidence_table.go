package wsingest

// The decision → phase → evidence table (verify-not-trust phase 12, D19): one
// row per declared decision (phase_decisions) and spec criterion (spec_criteria)
// of a plan, the phases whose docs claim it (`**Covers:**`, `**Depends on
// decisions:**`, a stop/review decision's own phase) and the evidence
// decision_evidence holds for it. The plan review's prompt and the dashboard read
// the same table, so the reviewer is asked about exactly what the operator sees.
//
// A row's chip, strongest first:
//
//  1. the latest plan review's word on it — evidence, or remaining when it says
//     missing: the review reads the code, the report is the executor's claim.
//     Review evidence beats review missing for the same id. A missing on an id
//     a report declared `not implemented:` stays not-implemented: the review
//     confirms the declared gap.
//  2. evidence — a Completion Report of a phase that did not itself mark the id
//     `not implemented:` cites it (a deferral is overtaken by the later phase
//     that did the work).
//  3. not-implemented — a report says `not implemented: <id> — <why>` and
//     nothing above proves it was done; a deliberate, explained gap is never
//     "remaining".
//  4. not-verified — nothing cites it and the latest plan review was
//     INCONCLUSIVE: "nobody could check", not "nobody did it".
//  5. remaining.
//
// A plan with neither decisions nor spec criteria has an empty table; old plans
// are not backfilled.

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Decision table chips.
const (
	ChipEvidence       = "evidence"
	ChipRemaining      = "remaining"
	ChipNotImplemented = "not-implemented"
	ChipNotVerified    = "not-verified"
)

// Plan review states on the table (PlanReviewState.Status).
const (
	PlanReviewNone        = "none"
	PlanReviewPass        = "pass"
	PlanReviewFail        = "fail"
	PlanReviewNotVerified = "not-verified"
)

// Row kinds.
const (
	TableKindDecision  = "decision"
	TableKindCriterion = "criterion"
)

// EvidenceItem is one decision_evidence row as the table shows it.
type EvidenceItem struct {
	Kind     string `json:"kind"`   // report | pr | review
	Status   string `json:"status"` // evidence | not-implemented | missing
	Source   string `json:"source"` // doc | plan-review (the daemon's, written into no doc)
	PhaseID  *int64 `json:"phaseId"`
	PhaseSeq *int   `json:"phaseSeq"`
	Excerpt  string `json:"excerpt"`
	Reason   string `json:"reason"`
}

// DecisionTableRow is one id of the plan.
type DecisionTableRow struct {
	ID     string `json:"id"`     // D-2 | SC-4
	Kind   string `json:"kind"`   // decision | criterion
	Text   string `json:"text"`   // the decision's question / the criterion's text
	Answer string `json:"answer"` // the decision's answer; "" for a criterion
	// Status is the declared state: decided | undecided | recommended for a
	// decision, done | open for a criterion.
	Status   string         `json:"status"`
	Chip     string         `json:"chip"`   // evidence | remaining | not-implemented | not-verified
	Reason   string         `json:"reason"` // the not-implemented why, or the plan review's reason class
	Phases   []int          `json:"phases"` // seqs of the phases claiming it, ascending
	Evidence []EvidenceItem `json:"evidence"`
}

// PlanReviewState is the latest plan review of the plan, either engine's.
type PlanReviewState struct {
	Status string `json:"status"` // none | pass | fail | not-verified
	Reason string `json:"reason"` // the inconclusive class (reviewer-timed-out, …); "" otherwise
	Detail string `json:"detail"`
	At     string `json:"at"` // when it finished; "" for none
}

// DecisionTableCounts counts rows per chip.
type DecisionTableCounts struct {
	Evidence       int `json:"evidence"`
	Remaining      int `json:"remaining"`
	NotImplemented int `json:"notImplemented"`
	NotVerified    int `json:"notVerified"`
}

// DecisionTable is GET /api/epics/{taskId}/evidence and the epic payload's
// `evidence` field.
type DecisionTable struct {
	PlanReview PlanReviewState     `json:"planReview"`
	Rows       []DecisionTableRow  `json:"rows"`
	Counts     DecisionTableCounts `json:"counts"`
}

// Querier is the read half of *sql.DB / *sql.Tx.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// LoadDecisionTable builds the plan's table. Reads only; each query's cursor is
// closed before the next opens (the daemon's pool is single-connection).
func LoadDecisionTable(db Querier, taskID int64) (DecisionTable, error) {
	t := DecisionTable{Rows: []DecisionTableRow{}}
	var err error
	if t.PlanReview, err = latestPlanReview(db, taskID); err != nil {
		return t, err
	}
	seqOf, claims, err := phaseClaims(db, taskID)
	if err != nil {
		return t, err
	}

	index := map[string]int{}
	add := func(r DecisionTableRow) {
		if _, dup := index[r.ID]; dup {
			return
		}
		index[r.ID] = len(t.Rows)
		t.Rows = append(t.Rows, r)
	}
	rows, err := db.Query(`SELECT decision_id, question, answer, status, phase_id FROM phase_decisions
		 WHERE workspace_task_id = ? ORDER BY seq, id`, taskID)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var (
			r       DecisionTableRow
			phaseID sql.NullInt64
		)
		if err := rows.Scan(&r.ID, &r.Text, &r.Answer, &r.Status, &phaseID); err != nil {
			rows.Close()
			return t, err
		}
		r.Kind = TableKindDecision
		if phaseID.Valid {
			if seq, ok := seqOf[phaseID.Int64]; ok {
				claims[r.ID] = append(claims[r.ID], seq)
			}
		}
		add(r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}
	rows, err = db.Query(`SELECT cid, text, done FROM spec_criteria WHERE workspace_task_id = ? ORDER BY pos, id`, taskID)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var (
			r    DecisionTableRow
			done bool
		)
		if err := rows.Scan(&r.ID, &r.Text, &done); err != nil {
			rows.Close()
			return t, err
		}
		r.Kind, r.Status = TableKindCriterion, "open"
		if done {
			r.Status = "done"
		}
		add(r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	rows, err = db.Query(`SELECT ref_id, kind, status, doc_path, phase_id, excerpt, reason FROM decision_evidence
		 WHERE workspace_task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return t, err
	}
	planDoc := PlanReviewDocPath(taskID)
	for rows.Next() {
		var (
			id, docPath string
			e           EvidenceItem
			phaseID     sql.NullInt64
		)
		if err := rows.Scan(&id, &e.Kind, &e.Status, &docPath, &phaseID, &e.Excerpt, &e.Reason); err != nil {
			rows.Close()
			return t, err
		}
		i, ok := index[id]
		if !ok {
			continue // an id the plan never declared: not a row of its table
		}
		e.Source = "doc"
		if docPath == planDoc {
			e.Source = "plan-review"
		}
		if phaseID.Valid {
			pid := phaseID.Int64
			e.PhaseID = &pid
			if seq, ok := seqOf[pid]; ok {
				e.PhaseSeq = &seq
			}
		}
		t.Rows[i].Evidence = append(t.Rows[i].Evidence, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}

	for i := range t.Rows {
		r := &t.Rows[i]
		r.Phases = uniqueSorted(claims[r.ID])
		if r.Evidence == nil {
			r.Evidence = []EvidenceItem{}
		}
		r.Chip, r.Reason = rowChip(r.Evidence, t.PlanReview)
		switch r.Chip {
		case ChipEvidence:
			t.Counts.Evidence++
		case ChipNotImplemented:
			t.Counts.NotImplemented++
		case ChipNotVerified:
			t.Counts.NotVerified++
		default:
			t.Counts.Remaining++
		}
	}
	return t, nil
}

// rowChip applies the precedence in the file comment. A `not implemented:` line
// only says the id was not done in the phase that wrote it, so it yields to proof
// from elsewhere: a review's evidence, or a report of another phase citing the id
// (phase 3 defers D-5 to phase 6, phase 6 does it).
func rowChip(ev []EvidenceItem, pr PlanReviewState) (chip, reason string) {
	review := ""
	notImpl, notImplReason := false, ""
	deferredIn := map[int64]bool{} // phases whose report marked the id not-implemented
	for _, e := range ev {
		switch {
		case e.Status == EvidenceStatusNotImplemented:
			if !notImpl {
				notImpl, notImplReason = true, e.Reason
			}
			if e.PhaseID != nil {
				deferredIn[*e.PhaseID] = true
			}
		case e.Kind == EvidenceKindReview && review != ChipEvidence:
			// Any review saying evidence beats one saying missing for the same id
			// (a doc's interactive round and the daemon's may both exist).
			if e.Status == EvidenceStatusEvidence {
				review = ChipEvidence
			} else {
				review = ChipRemaining
			}
		}
	}
	// A report's evidence counts unless its own phase also marked the id
	// not-implemented; a report row with no phase cannot be told apart from the
	// deferring one, so it counts only when nothing deferred the id.
	report := false
	for _, e := range ev {
		if e.Kind == EvidenceKindReview || e.Status != EvidenceStatusEvidence {
			continue
		}
		if (e.PhaseID != nil && !deferredIn[*e.PhaseID]) || (e.PhaseID == nil && !notImpl) {
			report = true
			break
		}
	}
	switch {
	case review == ChipEvidence:
		return ChipEvidence, ""
	case review == ChipRemaining:
		// The review found it missing: a declared deferral stays not-implemented
		// (the review confirms the absence), never "remaining".
		if notImpl {
			return ChipNotImplemented, notImplReason
		}
		return ChipRemaining, ""
	case report:
		return ChipEvidence, ""
	case notImpl:
		return ChipNotImplemented, notImplReason
	case pr.Status == PlanReviewNotVerified:
		return ChipNotVerified, pr.Reason
	}
	return ChipRemaining, ""
}

// phaseClaims maps each phase id to its seq, and each id to the seqs of the
// phases whose docs claim it (covers ∪ decision_deps).
func phaseClaims(db Querier, taskID int64) (map[int64]int, map[string][]int, error) {
	rows, err := db.Query(`SELECT id, seq, covers, decision_deps FROM epic_phases WHERE workspace_task_id = ?`, taskID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	seqOf := map[int64]int{}
	claims := map[string][]int{}
	for rows.Next() {
		var (
			id            int64
			seq           int
			covers, decis string
		)
		if err := rows.Scan(&id, &seq, &covers, &decis); err != nil {
			return nil, nil, err
		}
		seqOf[id] = seq
		for _, js := range []string{covers, decis} {
			var ids []string
			if json.Unmarshal([]byte(js), &ids) != nil {
				continue
			}
			for _, raw := range ids {
				if ref := NormalizeRefID(raw); ref != "" {
					claims[ref] = append(claims[ref], seq)
				}
			}
		}
	}
	return seqOf, claims, rows.Err()
}

func uniqueSorted(v []int) []int {
	out := []int{}
	seen := map[int]bool{}
	for _, n := range v {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// latestPlanReview is the most recent plan review of either engine: the
// daemon's phase_reviews scope='plan' row or a doc's `round: plan` block
// (review_blocks). INCONCLUSIVE is not-verified, with the daemon's reason class
// (the detail's prefix before ':') or "incomplete"/"inconclusive" for a block.
func latestPlanReview(db Querier, taskID int64) (PlanReviewState, error) {
	st := PlanReviewState{Status: PlanReviewNone}
	var verdict, detail, at string
	err := db.QueryRow(`SELECT verdict, COALESCE(detail, ''), COALESCE(finished_at, started_at, '') FROM phase_reviews
		 WHERE scope = 'plan' AND workspace_task_id = ? ORDER BY id DESC LIMIT 1`,
		strconv.FormatInt(taskID, 10)).Scan(&verdict, &detail, &at)
	switch {
	case err == nil:
		st = planReviewState(verdict, detail, at, true)
	case err != sql.ErrNoRows:
		return st, err
	}
	var complete bool
	err = db.QueryRow(`SELECT verdict, complete, written_at FROM review_blocks
		 WHERE workspace_task_id = ? AND round = 'plan' ORDER BY written_at DESC, id DESC LIMIT 1`, taskID).
		Scan(&verdict, &complete, &at)
	switch {
	case err == nil:
		if st.Status == PlanReviewNone || at > st.At {
			reason := ""
			if !complete {
				reason = "incomplete"
			}
			st = planReviewState(verdict, reason, at, false)
			if !complete && st.Status != PlanReviewNotVerified {
				st.Status, st.Reason = PlanReviewNotVerified, "incomplete"
			}
		}
	case err != sql.ErrNoRows:
		return st, err
	}
	return st, nil
}

func planReviewState(verdict, detail, at string, daemon bool) PlanReviewState {
	st := PlanReviewState{Detail: detail, At: at}
	switch strings.ToLower(verdict) {
	case "pass":
		st.Status = PlanReviewPass
	case "fail":
		st.Status = PlanReviewFail
	default:
		st.Status = PlanReviewNotVerified
		st.Reason = "inconclusive"
		if daemon {
			if i := strings.Index(detail, ":"); i > 0 {
				st.Reason = strings.TrimSpace(detail[:i])
			}
		} else if detail != "" {
			st.Reason = detail
		}
	}
	return st
}

// decisionTableCellMax bounds a table cell in the plan review prompt, in runes.
const decisionTableCellMax = 160

// RenderDecisionTable renders rows as the plan review prompt's markdown table,
// capped at maxBytes: rows past the cap are dropped with a marker naming how
// many. "" for no rows.
func RenderDecisionTable(rows []DecisionTableRow, maxBytes int) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| id | text | status | phases |\n|---|---|---|---|\n")
	for i, r := range rows {
		status := r.Status
		if r.Kind == TableKindDecision && r.Answer != "" {
			status += " → " + r.Answer
		}
		phases := "—"
		if len(r.Phases) > 0 {
			ps := make([]string, len(r.Phases))
			for j, p := range r.Phases {
				ps[j] = strconv.Itoa(p)
			}
			phases = strings.Join(ps, ", ")
		}
		line := "| " + r.ID + " | " + tableCell(r.Text) + " | " + tableCell(status) + " | " + phases + " |\n"
		if b.Len()+len(line) > maxBytes {
			b.WriteString("\n[… " + strconv.Itoa(len(rows)-i) + " more row(s) truncated: the prompt's decision-table budget is spent]\n")
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// tableCell makes s safe inside one markdown table cell, capped.
func tableCell(s string) string {
	s = strings.ReplaceAll(oneLine(s), "|", `\|`)
	if utf8.RuneCountInString(s) > decisionTableCellMax {
		s = string([]rune(s)[:decisionTableCellMax-1]) + "…"
	}
	return s
}
