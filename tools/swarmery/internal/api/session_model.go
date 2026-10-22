package api

import (
	"database/sql"
	"slices"
	"strings"

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
//
// The FIRST model is read from turns too, not from sessions.model: ingest
// stamps that column with the first assistant model of each BATCH, so a
// session whose later turns arrive in a tail batch (a resumed `claude -r
// --model …`, or any mid-file tail) had it overwritten with the new model and
// compared the last model against itself — the step-8.2 smoke test of plan 546
// saw sonnet-5 → haiku-4-5 reported as unchanged.
const sessionModelCols = `,
	       (SELECT tr.model FROM turns tr
	         WHERE tr.session_id = s.id AND tr.role = 'assistant'
	           AND tr.model IS NOT NULL AND tr.model <> ''
	         ORDER BY tr.seq ASC LIMIT 1),
	       (SELECT tr.model FROM turns tr
	         WHERE tr.session_id = s.id AND tr.role = 'assistant'
	           AND tr.model IS NOT NULL AND tr.model <> ''
	         ORDER BY tr.seq DESC LIMIT 1),
	       (SELECT COUNT(DISTINCT tr.model) FROM turns tr
	         WHERE tr.session_id = s.id AND tr.role = 'assistant'
	           AND tr.model IS NOT NULL AND tr.model <> ''),
	       (SELECT GROUP_CONCAT(m, char(31)) FROM (
	          SELECT tr.model AS m FROM turns tr
	           WHERE tr.session_id = s.id AND tr.role = 'assistant' AND tr.agent_name IS NULL
	             AND tr.model IS NOT NULL AND tr.model <> '' AND tr.model <> '<synthetic>'
	           GROUP BY tr.model ORDER BY MIN(tr.seq)))`

// The fourth column is the session's model LIST for the model-switch badge:
// the distinct models of the ORCHESTRATOR's assistant turns in first-use order,
// joined by the unit separator (char(31) — no model id contains it). It is a
// different, narrower question from the three above, which read every assistant
// turn including subagents: "did the session itself switch model mid-flight?".
// Two kinds of turn are left out because neither is a switch:
//
//   - subagent turns (agent_name set) — an Explore agent on haiku under an opus
//     session is delegation, not the session changing model;
//   - `<synthetic>` turns — Claude Code's own stand-ins for an interrupted or
//     failed request, which carry that literal "model".
//
// Counting either reads as a switch on most sessions: on the live store a naive
// `COUNT(DISTINCT turns.model) > 1` matched 127 sessions, of which 3 had
// actually changed model.

// sessionModelScan holds that tail.
type sessionModelScan struct {
	first    sql.NullString
	last     sql.NullString
	distinct sql.NullInt64
	list     sql.NullString
}

func (m *sessionModelScan) dest() []any { return []any{&m.first, &m.last, &m.distinct, &m.list} }

// modelListSep is the separator the list column is joined with (char(31)).
const modelListSep = "\x1f"

// switchedModels folds the raw list column into distinct models, first-use
// order, with the context-window marker stripped (modelid.Base): one model
// requested with and without `[1m]` is one model, not a switch.
func switchedModels(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	for _, id := range strings.Split(raw, modelListSep) {
		if base := modelid.Base(id); base != "" && !slices.Contains(out, base) {
			out = append(out, base)
		}
	}
	return out
}

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
	s.Models = switchedModels(m.list.String)
	s.ModelSwitched = len(s.Models) > 1
	if m.last.Valid && m.last.String != "" {
		v := m.last.String
		s.ModelLast = &v
	}
	if m.distinct.Int64 <= 1 || !m.first.Valid || m.first.String == "" || s.ModelLast == nil {
		return
	}
	s.ModelChanged = !modelid.SameTier(m.first.String, *s.ModelLast)
	// The chip renders on this, not on ModelChanged: only a move to a weaker
	// model is a fallback. See handlers.go's field comment.
	s.ModelFellBack = modelid.IsFallback(m.first.String, *s.ModelLast)
}
