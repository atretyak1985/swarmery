package api

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
)

// Review findings as data (migration 0123): the `kind: review` blocks of a phase
// doc, projected by wsingest (review_blocks.go) into review_blocks and
// review_findings. Read-only here — md is the truth and the scan owns the rows.
//
//	GET /api/epics/{taskId}/phases/{phaseId}/findings → 200 phaseFindingsDTO

// reviewFindingDTO is one review_findings row. Mirrors ReviewFinding in
// web/src/api/types.ts.
type reviewFindingDTO struct {
	ID string `json:"id"` // the block's finding id, else r<round>-<n>
	// Type is one of the five lenses; "" when the doc named another (kept, verbatim,
	// at the head of Note).
	Type       string `json:"type"`
	File       string `json:"file"`
	Line       *int   `json:"line"`
	Criterion  string `json:"criterion"`
	TestCaught bool   `json:"testCaught"`
	Evidence   string `json:"evidence"`
	Note       string `json:"note"`
	// ResolvedBy is the round whose `resolves:` closed the finding; null while open.
	ResolvedBy *string `json:"resolvedBy"`
}

// reviewBlockDTO is one review round of a phase doc with its findings. Mirrors
// ReviewBlock in web/src/api/types.ts.
type reviewBlockDTO struct {
	Round          string             `json:"round"`   // "1", "2", … or "plan"
	Verdict        string             `json:"verdict"` // PASS | FAIL | INCONCLUSIVE
	Complete       bool               `json:"complete"`
	ScopeLeft      []string           `json:"scopeLeft"`
	CheckedCallers []string           `json:"checkedCallers"`
	Resolves       []string           `json:"resolves"`
	Unparsable     bool               `json:"unparsable"`
	Source         string             `json:"source"` // daemon | interactive | backfill
	WrittenAt      string             `json:"writtenAt"`
	Findings       []reviewFindingDTO `json:"findings"`
}

// phaseFindingsDTO is the findings endpoint's answer.
type phaseFindingsDTO struct {
	LegacyReview bool             `json:"legacyReview"`
	Blocks       []reviewBlockDTO `json:"blocks"`
}

// roundOrder sorts numbered rounds by value and "plan" after them all, the way
// mdfence.Latest ranks them.
const roundOrder = `CASE WHEN round = 'plan' THEN 1000000000 ELSE CAST(round AS INTEGER) END`

// loadReviewBlocks reads the review rounds matching where (a predicate over the
// columns both tables share), keyed by doc path, oldest round first, with their
// findings. Two queries, whatever the number of docs, run one after the other —
// the store has a single connection, so they must never nest.
func loadReviewBlocks(db *sql.DB, where string, args ...any) (map[string][]reviewBlockDTO, error) {
	out := map[string][]reviewBlockDTO{}
	rows, err := db.Query(`
		SELECT doc_path, round, verdict, complete, scope_left_json, checked_callers_json, resolves_json,
		       unparsable, source, written_at
		  FROM review_blocks WHERE `+where+` ORDER BY doc_path, `+roundOrder, args...)
	if err != nil {
		return nil, err
	}
	type key struct{ doc, round string }
	index := map[key]int{}
	for rows.Next() {
		var (
			b                       reviewBlockDTO
			doc, scope, callers, rs string
		)
		if err := rows.Scan(&doc, &b.Round, &b.Verdict, &b.Complete, &scope, &callers, &rs,
			&b.Unparsable, &b.Source, &b.WrittenAt); err != nil {
			rows.Close()
			return nil, err
		}
		b.ScopeLeft, b.CheckedCallers, b.Resolves = jsonStrings(scope), jsonStrings(callers), jsonStrings(rs)
		b.Findings = []reviewFindingDTO{}
		index[key{doc, b.Round}] = len(out[doc])
		out[doc] = append(out[doc], b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = db.Query(`
		SELECT doc_path, round, finding_id, COALESCE(type, ''), file, line, criterion, test_caught,
		       evidence, note, resolved_by
		  FROM review_findings WHERE `+where+` ORDER BY doc_path, `+roundOrder+`, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			f          reviewFindingDTO
			doc, round string
			line       sql.NullInt64
			resolvedBy sql.NullString
		)
		if err := rows.Scan(&doc, &round, &f.ID, &f.Type, &f.File, &line, &f.Criterion, &f.TestCaught,
			&f.Evidence, &f.Note, &resolvedBy); err != nil {
			return nil, err
		}
		if line.Valid {
			v := int(line.Int64)
			f.Line = &v
		}
		if resolvedBy.Valid {
			v := resolvedBy.String
			f.ResolvedBy = &v
		}
		i, ok := index[key{doc, round}]
		if !ok {
			continue // a finding whose block row is gone; the next scan sweeps it
		}
		out[doc][i].Findings = append(out[doc][i].Findings, f)
	}
	return out, rows.Err()
}

// jsonStrings decodes a stored JSON string list; [] for anything unreadable.
func jsonStrings(raw string) []string {
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []string{}
	}
	return out
}

// phaseReviewBlocks loads every review round of taskID's phases for the epic
// payload, keyed by phase id via docs (phase id → doc path). Best-effort, like
// phaseReviews: a read error yields an empty map.
func (h *Handler) phaseReviewBlocks(taskID int64, docs map[int64]string) map[int64][]reviewBlockDTO {
	out := map[int64][]reviewBlockDTO{}
	byDoc, err := loadReviewBlocks(h.DB, `workspace_task_id = ?`, taskID)
	if err != nil {
		log.Printf("warning: epics: review blocks unreadable (task %d): %v", taskID, err)
		return out
	}
	for id, doc := range docs {
		if bs := byDoc[doc]; len(bs) > 0 {
			out[id] = bs
		}
	}
	return out
}

// listPhaseFindings — GET /api/epics/{taskId}/phases/{phaseId}/findings. 200
// phaseFindingsDTO (blocks oldest round first, never null); 404 for an unknown
// phase or one outside that epic.
func (h *Handler) listPhaseFindings(w http.ResponseWriter, r *http.Request) {
	phaseID, ok := h.parsePhaseRunParams(w, r)
	if !ok {
		return
	}
	var (
		doc    string
		legacy bool
	)
	if err := h.DB.QueryRow(`SELECT doc_path, legacy_review FROM epic_phases WHERE id = ?`, phaseID).
		Scan(&doc, &legacy); err != nil {
		writeErr(w, err)
		return
	}
	byDoc, err := loadReviewBlocks(h.DB, `doc_path = ?`, doc)
	if err != nil {
		writeErr(w, err)
		return
	}
	blocks := byDoc[doc]
	if blocks == nil {
		blocks = []reviewBlockDTO{}
	}
	writeJSON(w, phaseFindingsDTO{LegacyReview: legacy, Blocks: blocks}, nil)
}
