package handoff

// The `## Rejected` section of a handoff brief (verify-not-trust phase 19, D26):
// a fresh session started from a brief must not walk a path the work already
// ruled out. Its structured input is read from the DB here — never from the
// transcript — out of three sources:
//
//   - interview: the unchosen options of every answered Planning Mode question
//     of the plan the session belongs to (planning_turns);
//   - decision:  the unchosen variants of the plan's decided phase_decisions
//     rows (verify-not-trust phase 2). A decision row names what was chosen; its
//     variants are the options of the interview question it was minted from;
//   - correction: operator_corrections tied to the session or to its plan.
//
// The generator also lets the model extend the list from the capped turn text
// the digest already carries; this file only builds the structured half.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
)

const (
	// rejectedHeading is the canonical brief section heading.
	rejectedHeading = "## Rejected"
	// maxRejectedLines caps the section body, the truncation marker included.
	maxRejectedLines = 10
	// rejectedTextCap bounds one Option / Why so a long description or a
	// pasted correction cannot eat the section.
	rejectedTextCap = 160
)

// Rejected is one path the work already ruled out.
type Rejected struct {
	Option string // what was not taken
	Why    string // why it is off the table
	Source string // "interview D-2" | "decision D-3" | "correction revision:5"
}

// Line renders the form the digest hands the model and the brief keeps
// verbatim: "- <option> — <why> [<source>]".
func (r Rejected) Line() string {
	return fmt.Sprintf("- %s — %s [%s]", r.Option, r.Why, r.Source)
}

// StructuredRejected returns the rejected paths recorded in the DB for the
// session: interview options and decision variants of its plan (when it has
// one), then operator corrections of the session or its plan. A session with
// no plan yields corrections only; an unknown session yields nil.
func StructuredRejected(db *sql.DB, sessionUUID string) ([]Rejected, error) {
	var (
		projectID int64
		branch    string
	)
	err := db.QueryRow(`SELECT project_id, COALESCE(git_branch, '') FROM sessions WHERE session_uuid = ?`,
		sessionUUID).Scan(&projectID, &branch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// No sessions row is not fatal: the link mod can record session_plan before
	// the session's first event minted the row.

	taskID, hasPlan, err := planTaskOf(db, sessionUUID, projectID, branch)
	if err != nil {
		return nil, err
	}

	var out []Rejected
	if hasPlan {
		turns, err := interviewTurns(db, taskID)
		if err != nil {
			return nil, err
		}
		decided, err := decidedVariants(db, taskID, turns)
		if err != nil {
			return nil, err
		}
		// A decision row is the plan's truth once it exists (the README's
		// `## Decisions` is upserted onto it), so it supersedes the raw interview
		// answer of the same question.
		for _, t := range turns {
			if lines, ok := decided[t.key()]; ok {
				out = append(out, lines...)
				continue
			}
			out = append(out, t.rejected()...)
		}
	}

	corr, err := correctionsRejected(db, sessionUUID, taskID, hasPlan)
	if err != nil {
		return nil, err
	}
	return dedupRejected(append(out, corr...)), nil
}

// ---------------------------------------------------------------------------
// Session → plan
// ---------------------------------------------------------------------------

// planTaskOf resolves the workspace task (plan) a session works on: the
// session_plan link first (the operator's pick in the terminal, or the link's
// automatic resolution), then the run branch a plan/phase run stamped on its
// own sessions. ok=false means the session belongs to no plan.
func planTaskOf(db *sql.DB, uuid string, projectID int64, branch string) (int64, bool, error) {
	var (
		taskRef  string
		phaseRef sql.NullString
	)
	err := db.QueryRow(`SELECT task_id, phase_id FROM session_plan WHERE session_id = ?`, uuid).Scan(&taskRef, &phaseRef)
	switch {
	case err == nil:
		id, ok, err := taskOfLink(db, taskRef, phaseRef.String, projectID)
		if err != nil || ok {
			return id, ok, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	return taskOfRunBranch(db, branch)
}

// taskOfLink reads a session_plan row: a pinned phase names its plan outright;
// otherwise task_id is the numeric tasks.id or the external id, which is unique
// per workspace only — the session's own project wins a collision.
func taskOfLink(db *sql.DB, taskRef, phaseRef string, projectID int64) (int64, bool, error) {
	if phaseID, err := strconv.ParseInt(strings.TrimSpace(phaseRef), 10, 64); err == nil {
		if id, ok, err := scanTaskID(db, `SELECT workspace_task_id FROM epic_phases WHERE id = ?`, phaseID); err != nil || ok {
			return id, ok, err
		}
	}
	taskRef = strings.TrimSpace(taskRef)
	if n, err := strconv.ParseInt(taskRef, 10, 64); err == nil {
		return scanTaskID(db, `SELECT id FROM tasks WHERE id = ? AND source = 'workspace'`, n)
	}
	if taskRef == "" {
		return 0, false, nil
	}
	return scanTaskID(db, `
		SELECT id FROM tasks
		 WHERE source = 'workspace' AND external_id = ?
		 ORDER BY (project_id = ?) DESC, id DESC
		 LIMIT 1`, taskRef, projectID)
}

// taskOfRunBranch maps the deterministic branch a headless run stamps on every
// session it spawns (swarm/plan-<taskId>, swarm/phase-<phaseId>) to its plan.
// The phase lookup prefers epic_phases.run_branch, which survives a re-index
// that mints a new phase id (see api/session_plan_group.go).
func taskOfRunBranch(db *sql.DB, branch string) (int64, bool, error) {
	if rest, ok := strings.CutPrefix(branch, runcore.PlanBranchPrefix); ok {
		if n, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return scanTaskID(db, `SELECT id FROM tasks WHERE id = ? AND source = 'workspace'`, n)
		}
		return 0, false, nil
	}
	rest, ok := strings.CutPrefix(branch, runcore.PhaseBranchPrefix)
	if !ok {
		return 0, false, nil
	}
	if id, ok, err := scanTaskID(db, `SELECT workspace_task_id FROM epic_phases WHERE run_branch = ? ORDER BY id LIMIT 1`, branch); err != nil || ok {
		return id, ok, err
	}
	if n, err := strconv.ParseInt(rest, 10, 64); err == nil {
		return scanTaskID(db, `SELECT workspace_task_id FROM epic_phases WHERE id = ?`, n)
	}
	return 0, false, nil
}

// scanTaskID runs a one-column id query; no row is ok=false, not an error.
func scanTaskID(db *sql.DB, q string, args ...any) (int64, bool, error) {
	var id int64
	err := db.QueryRow(q, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ---------------------------------------------------------------------------
// Plan → interview
// ---------------------------------------------------------------------------

// interviewTurn is one Planning Mode question of the plan, with its answer.
type interviewTurn struct {
	planningID int64
	revise     bool
	seq        int
	question   planning.PlanningQuestion
	answered   bool            // an "answer" (not a "refine") was stamped
	chosen     map[string]bool // selected option ids
	otherText  string          // the operator's own answer, when they wrote one
}

// turnKey identifies an interview question across both sources.
type turnKey struct {
	planningID int64
	seq        int
}

func (t interviewTurn) key() turnKey { return turnKey{t.planningID, t.seq} }

// label names the question in a Source: a plan-mode question is the plan's
// D-n (phase 2 numbers decisions by planning_turns.seq); a revise-mode
// question has no D-n of its own.
func (t interviewTurn) label() string {
	if t.revise {
		return fmt.Sprintf("revise interview Q-%d", t.seq)
	}
	return fmt.Sprintf("D-%d", t.seq)
}

// wizardAnswerJSON is the subset of planning_turns.answer this file reads.
type wizardAnswerJSON struct {
	Kind              string   `json:"kind"` // answer | refine
	SelectedOptionIDs []string `json:"selectedOptionIds"`
	OtherText         string   `json:"otherText"`
}

// rejected turns an answered question's unchosen options into lines. Why is
// the option's own description (first sentence) plus what was chosen instead.
func (t interviewTurn) rejected() []Rejected {
	if !t.answered {
		return nil
	}
	instead := t.chosenText()
	var out []Rejected
	for _, o := range t.unchosen("") {
		out = append(out, Rejected{
			Option: clip(o.Label),
			Why:    clip(joinWhy(firstSentence(o.Description), instead)),
			Source: "interview " + t.label(),
		})
	}
	return out
}

// unchosen returns the selectable options the answer did not pick. answer is a
// decision row's chosen text: an option whose label equals it counts as chosen
// even when no option id was recorded.
func (t interviewTurn) unchosen(answer string) []planning.PlanningOption {
	answer = strings.TrimSpace(answer)
	var out []planning.PlanningOption
	for _, o := range t.question.Options {
		if o.IsOther || strings.TrimSpace(o.Label) == "" || t.chosen[o.ID] {
			continue
		}
		if answer != "" && strings.EqualFold(strings.TrimSpace(o.Label), answer) {
			continue
		}
		out = append(out, o)
	}
	return out
}

// chosenText is what the operator picked, as a reader would name it.
func (t interviewTurn) chosenText() string {
	var labels []string
	for _, o := range t.question.Options {
		if t.chosen[o.ID] && !o.IsOther {
			labels = append(labels, strings.TrimSpace(o.Label))
		}
	}
	if s := strings.TrimSpace(t.otherText); s != "" {
		labels = append(labels, s)
	}
	return strings.Join(labels, " + ")
}

// interviewTurns loads the questions of every planning session that produced
// or revised the task's plan, in session then question order. A malformed
// question is skipped: it has no options to reject.
func interviewTurns(db *sql.DB, taskID int64) ([]interviewTurn, error) {
	sessions, err := planningSessionsOf(db, taskID)
	if err != nil || len(sessions) == 0 {
		return nil, err
	}
	ids := make([]any, 0, len(sessions))
	for id := range sessions {
		ids = append(ids, id)
	}
	rows, err := db.Query(`
		SELECT planning_session_id, seq, question, answer
		  FROM planning_turns
		 WHERE planning_session_id IN (`+placeholders(len(ids))+`)
		 ORDER BY planning_session_id, seq`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []interviewTurn
	for rows.Next() {
		var (
			t        interviewTurn
			qJSON    string
			answerJS sql.NullString
		)
		if err := rows.Scan(&t.planningID, &t.seq, &qJSON, &answerJS); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(qJSON), &t.question) != nil {
			continue
		}
		t.revise = sessions[t.planningID]
		t.chosen = map[string]bool{}
		var a wizardAnswerJSON
		if answerJS.Valid && json.Unmarshal([]byte(answerJS.String), &a) == nil && a.Kind == "answer" {
			for _, id := range a.SelectedOptionIDs {
				t.chosen[id] = true
			}
			t.otherText = a.OtherText
			// An answer that picked nothing and wrote nothing rejected nothing.
			t.answered = len(t.chosen) > 0 || strings.TrimSpace(a.OtherText) != ""
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// planningSessionsOf returns the planning sessions behind a plan, id → revise
// mode: the wizard whose PLAN SAVED dir is the plan's dir, and every revise
// session that targeted the plan. planning_sessions carries no task id, so the
// plan-mode link is the dir itself.
func planningSessionsOf(db *sql.DB, taskID int64) (map[int64]bool, error) {
	var planPath string
	err := db.QueryRow(`SELECT path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`, taskID).Scan(&planPath)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	want := planDirKey(planPath)

	rows, err := db.Query(`
		SELECT id, mode, COALESCE(plan_dir, ''), COALESCE(revise_task_id, 0)
		  FROM planning_sessions
		 WHERE (mode = 'revise' AND revise_task_id = ?) OR COALESCE(plan_dir, '') <> ''
		 ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var (
			id, reviseTask int64
			mode, dir      string
		)
		if err := rows.Scan(&id, &mode, &dir, &reviseTask); err != nil {
			return nil, err
		}
		switch {
		case mode == "revise" && reviseTask == taskID:
			out[id] = true
		case mode != "revise" && want != "" && planDirKey(dir) == want:
			out[id] = false
		}
	}
	return out, rows.Err()
}

// workspaceZoneRe matches the zone segment wsingest walks, so an archived plan
// still matches the working-zone path its wizard recorded.
var workspaceZoneRe = regexp.MustCompile(`/workspace/(?:working|archive)/`)

// planDirKey normalises a task dir or its plan/ leaf to one comparable key:
// slash-separated, cleaned, without the plan/ leaf, archive folded onto working.
// "" stays "" (no dir, no match).
func planDirKey(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(p))
	p = strings.TrimSuffix(p, "/plan")
	return workspaceZoneRe.ReplaceAllString(p, "/workspace/working/")
}

// ---------------------------------------------------------------------------
// Plan → phase_decisions
// ---------------------------------------------------------------------------

// decidedVariants returns, per interview question, the rejected lines of the
// plan's decided phase_decisions rows. phase_decisions arrives with
// verify-not-trust phase 2; until its migration is applied the table is absent
// and this source contributes nothing. A decision with no matching interview
// question (a stop or review decision) has no variants to name and is skipped,
// as is an undecided one: nothing was chosen, so nothing was rejected.
func decidedVariants(db *sql.DB, taskID int64, turns []interviewTurn) (map[turnKey][]Rejected, error) {
	if len(turns) == 0 {
		return nil, nil
	}
	if ok, err := tableExists(db, "phase_decisions"); err != nil || !ok {
		return nil, err
	}

	byKey := map[turnKey]interviewTurn{}
	byQuestionID := map[string]interviewTurn{}
	ids := []any{taskID}
	seenSession := map[int64]bool{}
	var plan *interviewTurn // the plan-mode wizard's turns resolve README-born rows by seq
	for i, t := range turns {
		byKey[t.key()] = t
		if t.question.ID != "" {
			byQuestionID[t.question.ID] = t
		}
		if !seenSession[t.planningID] {
			seenSession[t.planningID] = true
			ids = append(ids, t.planningID)
		}
		if !t.revise && plan == nil {
			plan = &turns[i]
		}
	}

	rows, err := db.Query(`
		SELECT seq, COALESCE(planning_session_id, 0), question_id, answer, option_ids_json
		  FROM phase_decisions
		 WHERE status IN ('decided', 'recommended')
		   AND (workspace_task_id = ? OR planning_session_id IN (`+placeholders(len(ids)-1)+`))
		 ORDER BY seq, id`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[turnKey][]Rejected{}
	for rows.Next() {
		var (
			seq                int
			psID               int64
			questionID, answer string
			optJSON            string
			t                  interviewTurn
			found              bool
			chosenIDs          []string
		)
		if err := rows.Scan(&seq, &psID, &questionID, &answer, &optJSON); err != nil {
			return nil, err
		}
		switch {
		case psID != 0:
			t, found = byKey[turnKey{psID, seq}]
		case questionID != "":
			t, found = byQuestionID[questionID]
		}
		if !found && plan != nil {
			t, found = byKey[turnKey{plan.planningID, seq}]
		}
		if !found {
			continue
		}
		if _, done := out[t.key()]; done {
			continue // the task-adopted and the session-born row of one decision
		}
		_ = json.Unmarshal([]byte(optJSON), &chosenIDs)
		d := t
		d.chosen = map[string]bool{}
		for _, id := range chosenIDs {
			d.chosen[id] = true
		}
		instead := strings.TrimSpace(answer)
		if instead == "" {
			instead = d.chosenText()
		}
		lines := []Rejected{}
		for _, o := range d.unchosen(answer) {
			lines = append(lines, Rejected{
				Option: clip(o.Label),
				Why:    clip(joinWhy("", instead)),
				Source: fmt.Sprintf("decision D-%d", seq),
			})
		}
		out[t.key()] = lines
	}
	return out, rows.Err()
}

// tableExists reports whether a table is present in the schema.
func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	return n > 0, err
}

// ---------------------------------------------------------------------------
// Operator corrections
// ---------------------------------------------------------------------------

// correctionsRejected returns the operator corrections that bear on this
// session's work. operator_corrections has no session column, so the scope is
// derived from the typed ref:
//
//   - decision:N  the classifier decision was about this session
//     (decisions.session_uuid) or about a run of its plan (subject
//     planrun:<task> / phaserun:<phase of the task>);
//   - revision:N  a staged revision of the session's plan the operator rejected.
//
// Lesson and triage corrections are left out: they correct the daemon's memory
// and inbox, not a path the work took. Option is what the agent did, Why the
// operator's correction.
func correctionsRejected(db *sql.DB, uuid string, taskID int64, hasPlan bool) ([]Rejected, error) {
	planSubject := ""
	if !hasPlan {
		taskID = -1 // matches no row
	} else {
		planSubject = "planrun:" + strconv.FormatInt(taskID, 10)
	}
	rows, err := db.Query(`
		SELECT oc.ref, oc.before, oc.after, oc.reason
		  FROM operator_corrections oc
		 WHERE (oc.ref LIKE 'decision:%' AND EXISTS (
		         SELECT 1 FROM decisions d
		          WHERE 'decision:' || d.id = oc.ref
		            AND (d.session_uuid = ?
		                 OR d.subject = ?
		                 OR d.subject IN (SELECT 'phaserun:' || e.id FROM epic_phases e WHERE e.workspace_task_id = ?))))
		    OR (oc.ref LIKE 'revision:%' AND EXISTS (
		         SELECT 1 FROM plan_revisions r
		          WHERE 'revision:' || r.id = oc.ref AND r.workspace_task_id = ?))
		 ORDER BY oc.created_at, oc.id`, uuid, planSubject, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rejected
	for rows.Next() {
		var ref, before, after, reason string
		if err := rows.Scan(&ref, &before, &after, &reason); err != nil {
			return nil, err
		}
		why := oneLine(reason)
		if why == "" && oneLine(after) != "" {
			why = "operator replaced it with: " + oneLine(after)
		}
		if why == "" {
			why = "operator rejected it"
		}
		out = append(out, Rejected{
			Option: clip(nz(oneLine(before), ref)),
			Why:    clip(why),
			Source: "correction " + ref,
		})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// dedupRejected keeps the first line per (option, source) — the same option of
// one question reached through both a task-adopted and a session-born row.
func dedupRejected(in []Rejected) []Rejected {
	seen := map[string]bool{}
	out := make([]Rejected, 0, len(in))
	for _, r := range in {
		k := strings.ToLower(r.Option) + "\x00" + r.Source
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// joinWhy builds an interview/decision Why: the option's own description, then
// what was chosen in its place.
func joinWhy(desc, instead string) string {
	desc = strings.TrimSpace(desc)
	instead = strings.TrimSpace(instead)
	switch {
	case desc != "" && instead != "":
		return strings.TrimRight(desc, ".") + "; chose " + instead + " instead"
	case instead != "":
		return "chose " + instead + " instead"
	case desc != "":
		return desc
	default:
		return "not chosen"
	}
}

// firstSentence returns s up to its first sentence terminator, on one line.
func firstSentence(s string) string {
	s = oneLine(s)
	for i, r := range s {
		if (r == '.' || r == '!' || r == '?') && (i+1 == len(s) || s[i+1] == ' ') {
			return s[:i+1]
		}
	}
	return s
}

// oneLine collapses whitespace runs, newlines included, so a value cannot break
// the one-entry-per-line section.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip bounds one Option / Why.
func clip(s string) string { return truncate(oneLine(s), rejectedTextCap) }

// placeholders returns "?, ?, …" for n arguments (n ≥ 1).
func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
