package api

import (
	"database/sql"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/modelid"
)

// sessionModelCols is the "did this session change model?" tail of a session
// row, declared beside the scan that reads it (same convention as
// sessionPlanGroupCols / sessionTerminalCols).
//
// sessions.model is the FIRST assistant model of the transcript and nothing
// else recorded the model over time, so a session an Opus 5.5 safeguard moved
// onto an older model reported the model it STARTED on, for as long as the row
// existed — on the session card, in epics.go's "the model the run actually
// used", and in every cost and eval surface that trusted the column. The two
// facts that make the change visible are computed here from turns, which does
// carry the per-message model.
const sessionModelCols = `,
	       (SELECT tr.model FROM turns tr
	         WHERE tr.session_id = s.id AND tr.role = 'assistant'
	           AND tr.model IS NOT NULL AND tr.model <> ''
	         ORDER BY tr.seq DESC LIMIT 1),
	       (SELECT COUNT(DISTINCT tr.model) FROM turns tr
	         WHERE tr.session_id = s.id AND tr.role = 'assistant'
	           AND tr.model IS NOT NULL AND tr.model <> '')`

// sessionModelScan holds that tail.
type sessionModelScan struct {
	last     sql.NullString
	distinct sql.NullInt64
}

func (m *sessionModelScan) dest() []any { return []any{&m.last, &m.distinct} }

// apply fills sessionDTO.ModelLast / ModelChanged.
//
// ModelChanged is NOT `COUNT(DISTINCT model) > 1`. That count is over raw id
// strings, and `claude-opus-5-5` and `claude-opus-5-5[1m]` are two of those for
// one model — a 1M-context session would wear a "fell back" chip having never
// fallen back. The count is used only as the cheap gate that says "more than one
// id appeared at all"; the CLAIM is then made by comparing the first and last
// ids by family and generation, which is what a reader means by "it changed".
//
// KNOWN LIMIT, deliberate: a session that went opus → sonnet → opus reads as
// unchanged. Both endpoints agree, the chip's whole job is to explain output
// that came from a weaker model, and the alternative — grouping every distinct
// id by family in SQL — is a per-row scan on the busiest list in the product for
// a case nothing has yet observed.
func (m *sessionModelScan) apply(s *sessionDTO) {
	if m.last.Valid && m.last.String != "" {
		v := m.last.String
		s.ModelLast = &v
	}
	if m.distinct.Int64 <= 1 || s.Model == nil || s.ModelLast == nil {
		return
	}
	s.ModelChanged = !modelid.SameTier(*s.Model, *s.ModelLast)
	// The chip renders on this, not on ModelChanged: only a move to a weaker
	// model is a fallback. See handlers.go's field comment.
	s.ModelFellBack = modelid.IsFallback(*s.Model, *s.ModelLast)
}
