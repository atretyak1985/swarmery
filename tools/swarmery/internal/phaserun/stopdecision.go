package phaserun

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// The agent-declared stop (verify-not-trust phase 10, D4/D28). The prompt hands
// the executor its guard's `stop_if` list and a contract (stopContract): when a
// trigger fires — an out-of-scope change, a protected path, an access or login
// error, a criterion that cannot be met — it stops and asks instead of working
// around it, by writing a `kind: stop` block with two options and ending
// `PHASE BLOCKED: <trigger>`.
//
// A run that settles blocked with such a block raises a source='stop'
// phase_decisions row (the block rides in guard_json under "stop" — see
// store.UpsertAgentStopDecision), which the needs-you queue lists as a
// phase_decision with the two options as buttons plus free text. The answer
// appends `- **D-n** (stop: <trigger>) — <reason> → <answer>` to the phase doc's
// `## Decisions` (and, for a protected-path stop answered with an allow option,
// the paths to the guard's `allow:` list), which changes the blocked fingerprint;
// the re-run continues on the phase's own branch and its prompt states the
// decision (promptDecisions).

// StopOptionOther is the free-text answer every agent stop offers beside the
// executor's own options.
const StopOptionOther = "other"

// stopPayload is guard_json's shape for an agent-declared stop.
type stopPayload struct {
	Stop *mdfence.Stop `json:"stop"`
}

// AgentStop returns the stop block a decision row carries, or nil when the row
// is not an agent-declared stop (a guard or review decision).
func AgentStop(d store.Decision) *mdfence.Stop {
	if d.Source != store.DecisionSourceStop {
		return nil
	}
	var p stopPayload
	if json.Unmarshal([]byte(d.GuardJSON), &p) != nil {
		return nil
	}
	return p.Stop
}

// stopTranscriptTurns bounds how far back the stop block is looked for: the
// contract puts it in the final message, but a run that wrote it and then said
// one more line still asked.
const stopTranscriptTurns = 3

// stopBlockOf reads the stop block the run's last assistant turns carry, or nil.
// An unparsable block is logged and read as none: the run is still blocked, and
// its reason still reaches the operator through run_error.
func (s *Service) stopBlockOf(phaseID int64, uuid string) *mdfence.Stop {
	if s.DB == nil || uuid == "" {
		return nil
	}
	rows, err := s.DB.Query(`
		SELECT tr.text FROM turns tr JOIN sessions se ON se.id = tr.session_id
		 WHERE se.session_uuid = ? AND tr.role = 'assistant' AND tr.text IS NOT NULL
		 ORDER BY tr.seq DESC LIMIT ?`, uuid, stopTranscriptTurns)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d stop block unread: %v", phaseID, err)
		return nil
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var t string
		if rows.Scan(&t) == nil {
			texts = append(texts, t)
		}
	}
	for _, t := range texts { // newest first
		st, perr := mdfence.ParseStop(t)
		if perr != nil {
			log.Printf("warning: phaserun: phase=%d the run's stop block is unparsable: %v", phaseID, perr)
			return nil
		}
		if st != nil {
			return st
		}
	}
	return nil
}

// raiseStopDecision writes (or refreshes) the phase's open agent-stop decision.
// guardPaths are the paths the run's guard check flagged: a protected-path stop
// that named none of its own takes them, so the allow answer has something to
// allow. Best-effort like raiseGuardDecision.
func (s *Service) raiseStopDecision(phaseID int64, info phaseInfo, st *mdfence.Stop, guardPaths []string) {
	if st.Trigger == mdfence.StopProtectedPath && len(st.Paths) == 0 {
		st.Paths = guardPaths
	}
	for _, l := range st.Lints {
		log.Printf("warning: phaserun: phase=%d stop block lint %s (trigger %q, %d option(s))", phaseID, l, st.Trigger, len(st.Options))
	}
	body, err := json.Marshal(stopPayload{Stop: st})
	if err != nil {
		log.Printf("error: phaserun: phase=%d stop decision not recorded: %v", phaseID, err)
		return
	}
	ids := make([]string, 0, len(st.Options)+1)
	for _, o := range st.Options {
		ids = append(ids, o.ID)
	}
	ids = append(ids, StopOptionOther)
	d, created, err := store.UpsertAgentStopDecision(s.DB, info.WorkspaceTaskID, phaseID, st.Reason, ids, string(body), s.ts())
	if err != nil {
		log.Printf("error: phaserun: phase=%d stop decision not recorded: %v", phaseID, err)
		return
	}
	verb := "refreshed"
	if created {
		verb = "raised"
	}
	log.Printf("phaserun: phase=%d stop decision %s %s (%s): %s", phaseID, d.DecisionID, verb, st.Trigger, d.Question)
	s.notify(info.WorkspaceTaskID)
}

// answerStopDecision records the operator's answer to an agent-declared stop.
// The caller (AnswerGuardDecision) holds answerMu and has checked the row is an
// open stop decision of this phase.
//
// The option must be one the block offered, or `other` with the free text in
// Note. The doc line reads `- **D-n** (stop: <trigger>) — <reason> → <answer>`,
// where answer is `<id>: <label>` (plus ` — <note>` when one was given) or
// `other: <text>`. A protected-path stop answered with an allow option also adds
// its paths to the guard block's `allow:` list. Doc first, row second — the same
// order and reason as the guard decision.
func answerStopDecision(db *sql.DB, d store.Decision, st *mdfence.Stop, docPath string, ans GuardAnswer, now time.Time) (store.Decision, error) {
	note := strings.Join(strings.Fields(ans.Note), " ")
	var answer string
	var chosen *mdfence.StopOption
	if ans.Option == StopOptionOther {
		if note == "" {
			return store.Decision{}, fmt.Errorf("%w %q: the free-text answer needs its text in note", ErrDecisionBadOption, ans.Option)
		}
		answer = StopOptionOther + ": " + note
	} else {
		ids := make([]string, 0, len(st.Options))
		for i := range st.Options {
			ids = append(ids, st.Options[i].ID)
			if st.Options[i].ID == ans.Option {
				chosen = &st.Options[i]
			}
		}
		if chosen == nil {
			return store.Decision{}, fmt.Errorf("%w %q (want %s or %s)", ErrDecisionBadOption, ans.Option,
				strings.Join(ids, ", "), StopOptionOther)
		}
		answer = chosen.ID + ": " + chosen.Label
		if note != "" {
			answer += " — " + note
		}
	}

	body, err := os.ReadFile(docPath)
	if err != nil {
		return store.Decision{}, fmt.Errorf("read phase doc: %w", err)
	}
	doc := wsingest.AppendPhaseDecision(string(body),
		wsingest.FormatPhaseDecisionLine(d.Seq, "stop: "+st.Trigger, st.Reason, answer))
	if chosen != nil && st.Trigger == mdfence.StopProtectedPath && mdfence.AllowsScope(chosen.ID) && len(st.Paths) > 0 {
		widened, perr := mdfence.AddGuardAllow(doc, st.Paths)
		if perr != nil {
			return store.Decision{}, fmt.Errorf("the guard block cannot take an allow list: %v", perr)
		}
		doc = widened
	}
	if err := wsingest.WriteDocAtomic(docPath, doc); err != nil {
		return store.Decision{}, fmt.Errorf("write phase doc: %w", err)
	}
	ok, err := store.DecideDecision(db, d.ID, answer, now.UTC().Format(time.RFC3339))
	if err != nil {
		return store.Decision{}, err
	}
	if !ok {
		return store.Decision{}, ErrDecisionNotOpen
	}
	return store.GetDecision(db, d.ID)
}

// stopAnsweredSince reports whether one of the phase's agent stops was answered
// at or after since (the blocked run's run_ended_at). Such a phase is not the
// one that blocked: the blocked re-run guard admits it, and the run continues on
// the phase's own branch so the blocked run's commits survive.
func (s *Service) stopAnsweredSince(info phaseInfo, phaseID int64) bool {
	if info.RunState != "blocked" || info.RunEndedAt == "" {
		return false
	}
	since, err := time.Parse(time.RFC3339, info.RunEndedAt)
	if err != nil {
		return false
	}
	stops, err := store.ListPhaseAgentStops(s.DB, info.WorkspaceTaskID, phaseID)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d stop decisions unreadable: %v", phaseID, err)
		return false
	}
	for _, d := range stops {
		if d.Status != store.DecisionDecided || d.DecidedAt == nil {
			continue
		}
		if at, err := time.Parse(time.RFC3339, *d.DecidedAt); err == nil && !at.Before(since) {
			return true
		}
	}
	return false
}

// promptDecisions are the decisions a run of this phase inherits, for its
// prompt (decisionsNote): every answered agent stop of the phase, then each
// decision its `**Depends on decisions:**` names that was skipped as cheap and
// taken at the planner's recommendation. Best-effort: a failed read renders
// none — the doc's `## Decisions` still carries the stop answers.
func (s *Service) promptDecisions(info phaseInfo, phaseID int64) []PromptDecision {
	var out []PromptDecision
	stops, err := store.ListPhaseAgentStops(s.DB, info.WorkspaceTaskID, phaseID)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d stop decisions unreadable: %v", phaseID, err)
	}
	for _, d := range stops {
		if d.Status != store.DecisionDecided {
			continue
		}
		pd := PromptDecision{ID: d.DecisionID, Question: d.Question, Answer: d.Answer}
		if st := AgentStop(d); st != nil {
			pd.Trigger = st.Trigger
		}
		out = append(out, pd)
	}
	rec, err := recommendedDeps(s.DB, info.WorkspaceTaskID, phaseID)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d recommended decisions unreadable: %v", phaseID, err)
	}
	return append(out, rec...)
}

// recommendedDeps are the decisions a phase declares it depends on that nobody
// answered and that were taken at the planner's recommended option (status
// `recommended` — cheap to change, so they never held the phase).
func recommendedDeps(q *sql.DB, taskID, phaseID int64) ([]PromptDecision, error) {
	var depsJSON string
	if err := q.QueryRow(`SELECT decision_deps FROM epic_phases WHERE id = ?`, phaseID).Scan(&depsJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var deps []string
	if json.Unmarshal([]byte(depsJSON), &deps) != nil || len(deps) == 0 {
		return nil, nil
	}
	ds, err := store.ListTaskDecisions(q, taskID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.Decision, len(ds))
	for _, d := range ds {
		byID[strings.ToUpper(d.DecisionID)] = d
	}
	var out []PromptDecision
	for _, id := range deps {
		d, ok := byID[strings.ToUpper(strings.TrimSpace(id))]
		if !ok || d.Status != store.DecisionRecommended {
			continue
		}
		out = append(out, PromptDecision{ID: d.DecisionID, Question: d.Question, Answer: d.Answer, Recommended: true})
	}
	return out, nil
}
