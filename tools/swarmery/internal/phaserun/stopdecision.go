package phaserun

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// Agent-declared stops (verify-not-trust phase 10, D4/D28). The executor's prompt
// carries the phase guard's `stop_if` list (prompt.go, stopNote); when one of
// them fires, the executor stops instead of working around it: it writes a
// `kind: stop` block with two options and ends `PHASE BLOCKED: <trigger>`.
//
// A run that settles blocked with such a block becomes a source='stop'
// phase_decisions row — the same row, the same needs-you `phase_decision` item and
// the same answer endpoint as the guard decision of phase 4 (guard_decision.go).
// The agent's block rides in the row's guard_json under the `stop` key, beside the
// run's guard check, so no column was added. Answering it appends
//
//	- **D-n** (stop: <trigger>) — <reason> → <option id | other: text>
//
// to the phase doc's `## Decisions`, adds an `allow:` list to the guard block for
// a protected-path stop answered allow-add-to-scope, and so changes the blocked
// fingerprint (blocked.go). The next run of the phase continues on its own branch
// (the stop's partial work survives) and its prompt carries `Decision D-n: …`.

// StopPayload is an agent-declared stop as stored on its decision.
type StopPayload struct {
	Trigger string               `json:"trigger"`
	Reason  string               `json:"reason"`
	Options []mdfence.StopOption `json:"options"`
	// Paths are the files a protected-path stop is about: the agent's `paths:`
	// that survived vetStopPaths (literal, under a protected glob). What was
	// dropped is flagged stop_paths_rejected.
	Paths []string `json:"paths,omitempty"`
	// Flags are the block's lints (mdfence.LintStop*), shown to the operator.
	Flags []string `json:"flags,omitempty"`
}

// HasOption reports whether the stop offers option id.
func (p *StopPayload) HasOption(id string) bool {
	for _, o := range p.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}

// OptionLabel is the label of option id; "" when the stop offers none.
func (p *StopPayload) OptionLabel(id string) string {
	for _, o := range p.Options {
		if o.ID == id {
			return o.Label
		}
	}
	return ""
}

// stopRecord is a stop decision's guard_json: the run's guard check, flattened,
// plus the agent's stop when the run declared one.
type stopRecord struct {
	GuardResult
	Stop *StopPayload `json:"stop,omitempty"`
}

func decodeStopRecord(guardJSON string) stopRecord {
	var r stopRecord
	_ = json.Unmarshal([]byte(guardJSON), &r)
	return r
}

// StopFromJSON returns the agent-declared stop stored in a decision's guard_json,
// or nil when the decision carries none (a guard, review or interview decision).
func StopFromJSON(guardJSON string) *StopPayload {
	return decodeStopRecord(guardJSON).Stop
}

// StopOf is StopFromJSON for a loaded decision.
func StopOf(d store.Decision) *StopPayload { return StopFromJSON(d.GuardJSON) }

// stopQuestion is the decision's question: the agent's reason, or a sentence
// naming the trigger when the block gave none (flagged stop_reason_missing).
func stopQuestion(st *mdfence.Stop) string {
	if st.Reason != "" {
		return st.Reason
	}
	trigger := st.Trigger
	if trigger == "" {
		trigger = "no trigger"
	}
	return "The run stopped (" + trigger + ") without giving a reason."
}

// stopSource is the doc line's source tuple: `stop: <trigger>`, or plain `stop`
// for a block that named no trigger.
func stopSource(trigger string) string {
	if strings.TrimSpace(trigger) == "" {
		return "stop"
	}
	return "stop: " + trigger
}

// stopStage turns a blocked run's `kind: stop` block into its decision. Called
// from runAndHandle's defer right after the guard check, so a protected-path stop
// and the guard's own finding on the same run land on ONE open decision: the
// stop refreshes the row the guard check may just have raised, keeping the guard
// result beside it. Nothing happens for a run that did not end blocked, or ended
// blocked without a block — the plain `PHASE BLOCKED:` path is untouched. A panic
// is logged and swallowed: the review, the verifier and the worktree removal
// after it still run.
func (s *Service) stopStage(phaseID int64, info phaseInfo, endState, sessionUUID, worktreePath string, guard GuardResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("error: phaserun: phase=%d stop stage panicked: %v", phaseID, r)
		}
	}()
	if endState != "blocked" || sessionUUID == "" {
		return
	}
	st, err := mdfence.ParseStop(runcore.LastAssistantText(s.DB, sessionUUID))
	if err != nil {
		log.Printf("warning: phaserun: phase=%d the run's stop block is unreadable, no decision raised: %v", phaseID, err)
		return
	}
	if st == nil {
		return
	}
	s.raiseStopDecision(phaseID, info, st, guard, worktreeDirProbe(s.reviewGit(), worktreePath))
}

// The guard's own choices, as a stop decision offers them. Their ids are what
// the daemon acts on (allow widens the guard), so their label and consequence
// are the daemon's, never the agent's: an agent cannot label its allow option
// "Leave the test alone".
var (
	stopGuardAllowOption = mdfence.StopOption{
		ID: GuardOptionAllow, Label: "Allow (add to scope)",
		Consequence: "the files listed join the guard's allow list and the next run builds on this run's branch",
	}
	stopGuardRejectOption = mdfence.StopOption{
		ID: GuardOptionReject, Label: "Reject",
		Consequence: "the guard stays as it is; no protected edit of this run is built on",
	}
)

// stopOptions are the options a stop decision offers: the agent's, with the
// daemon's label and consequence on the reserved guard ids, plus — when the
// run's guard check flagged it — the guard's allow and reject choices the agent
// did not already offer. A stop never hides the guard's own question: an
// access-error stop on a run that edited a protected file still lets the
// operator reject that edit.
func stopOptions(agent []mdfence.StopOption, guard GuardResult) []mdfence.StopOption {
	out := make([]mdfence.StopOption, 0, len(agent)+2)
	for _, o := range agent {
		switch o.ID {
		case GuardOptionAllow:
			o = stopGuardAllowOption
		case GuardOptionReject:
			o = stopGuardRejectOption
		}
		out = append(out, o)
	}
	if !guard.Flagged() {
		return out
	}
	for _, o := range []mdfence.StopOption{stopGuardAllowOption, stopGuardRejectOption} {
		if !slices.ContainsFunc(out, func(x mdfence.StopOption) bool { return x.ID == o.ID }) {
			out = append(out, o)
		}
	}
	return out
}

// StopOptionsOf are the options the stop decision stored as guardJSON offers,
// rebuilt from the stored record (stopOptions) so a row raised before the
// guard's choices were merged in still offers them; nil for a decision without
// a stop.
func StopOptionsOf(guardJSON string) []mdfence.StopOption {
	rec := decodeStopRecord(guardJSON)
	if rec.Stop == nil {
		return nil
	}
	return stopOptions(rec.Stop.Options, rec.GuardResult)
}

// raiseStopDecision writes (or refreshes) the phase's open stop decision from an
// agent's block. isDir probes the run's worktree (worktreeDirProbe; nil skips
// it). Best-effort, like the guard decision.
func (s *Service) raiseStopDecision(phaseID int64, info phaseInfo, st *mdfence.Stop, guard GuardResult, isDir func(string) bool) {
	paths, rejected := vetAllowPaths(st.Paths, guard.ProtectedPaths, guard.Paths(), isDir)
	flags := append([]string(nil), st.Flags...)
	if _, guardRejected := guard.releasable(); guardRejected {
		rejected = true
	}
	if rejected && !slices.Contains(flags, mdfence.LintStopPathsRejected) {
		flags = append(flags, mdfence.LintStopPathsRejected)
	}
	rec := stopRecord{GuardResult: guard, Stop: &StopPayload{
		Trigger: st.Trigger, Reason: st.Reason, Options: stopOptions(st.Options, guard), Paths: paths, Flags: flags,
	}}
	raw, err := json.Marshal(rec)
	if err != nil {
		log.Printf("error: phaserun: phase=%d stop decision not recorded: %v", phaseID, err)
		return
	}
	d, created, err := store.UpsertStopDecision(s.DB, info.WorkspaceTaskID, phaseID, stopQuestion(st),
		(&mdfence.Stop{Options: rec.Stop.Options}).OptionIDs(), string(raw), s.ts())
	if err != nil {
		log.Printf("error: phaserun: phase=%d stop decision not recorded: %v", phaseID, err)
		return
	}
	verb := "refreshed"
	if created {
		verb = "raised"
	}
	log.Printf("phaserun: phase=%d stop decision %s %s (%s, flags=%v): %s",
		phaseID, d.DecisionID, verb, st.Trigger, flags, d.Question)
	s.notify(info.WorkspaceTaskID)
}

// stopReleasable is what an allow-add-to-scope answer to rec's stop adds to the
// guard's `allow:` list, and whether vetting dropped anything (now, or when the
// row was raised). Every path passes vetAllowPaths, whatever its source: the
// guard check's own findings (file names and guard.log fields the run wrote),
// then — for a protected-path stop only — the agent's `paths:`, all re-vetted
// against the guard the run was checked with, so a stored row is never taken on
// trust. A stop that is not a protected-path stop releases only the guard
// check's findings, which are empty unless that check flagged the run.
func stopReleasable(rec stopRecord) (paths []string, rejected bool) {
	if rec.Stop == nil {
		return nil, false
	}
	if rec.Stop.Trigger != mdfence.StopTriggerProtectedPath {
		return rec.GuardResult.releasable()
	}
	paths, rejected = rec.GuardResult.releasable(rec.Stop.Paths...)
	return paths, rejected || slices.Contains(rec.Stop.Flags, mdfence.LintStopPathsRejected)
}

// stopAllowPaths is stopReleasable's paths.
func stopAllowPaths(rec stopRecord) []string {
	paths, _ := stopReleasable(rec)
	return paths
}

// StopAllowPaths is stopAllowPaths for a decision's stored guard_json: exactly
// the files an allow-add-to-scope answer would release, for the operator's card.
func StopAllowPaths(guardJSON string) []string {
	return stopAllowPaths(decodeStopRecord(guardJSON))
}

// answerStopDecision records the operator's answer to an agent-declared stop:
// one of the block's option ids, or `other` with the operator's own text (an empty
// option with a note is read as `other`). The doc is written before the row, as
// for a guard decision.
func answerStopDecision(db *sql.DB, d store.Decision, rec stopRecord, docPath string, ans GuardAnswer, now time.Time) (store.Decision, error) {
	st := rec.Stop
	// What the card offered: the stored options with the guard's own choices
	// merged in (a row raised before they were stored still offers them).
	offered := &StopPayload{Options: stopOptions(st.Options, rec.GuardResult)}
	opt := strings.ToLower(strings.TrimSpace(ans.Option))
	note := flatten(ans.Note)
	if opt == "" && note != "" {
		opt = mdfence.StopOptionOther
	}
	var answer string
	switch {
	case opt == mdfence.StopOptionOther:
		if note == "" {
			return store.Decision{}, fmt.Errorf("%w: %q needs the operator's text in note", ErrDecisionBadOption, mdfence.StopOptionOther)
		}
		answer = mdfence.StopOptionOther + ": " + note
	case offered.HasOption(opt) && mdfence.ValidOptionID(opt):
		// A row raised before ids were held to bare tokens may offer one that
		// would read back as another (stopAnswerOption); it is not answerable.
		answer = opt
		if note != "" {
			answer += " — " + note
		}
	default:
		want := append(append([]string(nil), (&mdfence.Stop{Options: offered.Options}).OptionIDs()...), mdfence.StopOptionOther)
		return store.Decision{}, fmt.Errorf("%w %q (want one of %s)", ErrDecisionBadOption, ans.Option, strings.Join(want, ", "))
	}

	body, err := os.ReadFile(docPath)
	if err != nil {
		return store.Decision{}, fmt.Errorf("read phase doc: %w", err)
	}
	doc := wsingest.AppendPhaseDecision(string(body),
		wsingest.FormatPhaseDecisionLine(d.Seq, stopSource(st.Trigger), d.Question, answer))
	if opt == GuardOptionAllow {
		if paths := stopAllowPaths(rec); len(paths) > 0 {
			widened, perr := mdfence.AddGuardAllow(doc, paths)
			switch {
			case perr == nil:
				doc = widened
			case perr.Kind == mdfence.ErrAbsent:
				// No guard to widen: nothing protected the paths, so nothing to allow.
				log.Printf("warning: phaserun: decision %s allows %v but the phase doc has no guard block", d.DecisionID, paths)
			default:
				return store.Decision{}, fmt.Errorf("the guard block cannot take an allow list: %v", perr)
			}
		}
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

// answerShown is a decided stop's answer as a run's prompt shows it: the stored
// answer, with the chosen option's label when it differs from the id.
func answerShown(d store.Decision) string {
	st := StopOf(d)
	if st == nil {
		return d.Answer
	}
	id := d.Answer
	if i := strings.Index(id, " — "); i >= 0 {
		id = id[:i]
	}
	if label := st.OptionLabel(id); label != "" && label != id {
		return d.Answer + " (" + label + ")"
	}
	return d.Answer
}

// decisionAsked names what a decided stop answered: the agent's trigger and
// reason, or the guard decision's question.
func decisionAsked(d store.Decision) string {
	if st := StopOf(d); st != nil {
		trigger := st.Trigger
		if trigger == "" {
			trigger = "no trigger"
		}
		return "answers your earlier stop (" + trigger + "): " + d.Question
	}
	return "answers: " + d.Question
}

// maxPromptDecisions bounds the answered stops a prompt repeats; the doc's
// `## Decisions` section still holds every one.
const maxPromptDecisions = 10

// runDecisions loads what DecisionsNote renders for one phase: the cheap
// decisions its doc depends on that nobody answered (cost cheap, not decided —
// the ones the decision hold lets through), and the decided stop decisions of
// this phase, newest maxPromptDecisions. Best-effort: a failed read is logged and
// renders nothing — the doc's `## Decisions` lines still reach the executor.
func (s *Service) runDecisions(phaseID, taskID int64) (recommended, answered []store.Decision) {
	var depsJSON string
	if err := s.DB.QueryRow(`SELECT decision_deps FROM epic_phases WHERE id = ?`, phaseID).Scan(&depsJSON); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			log.Printf("warning: phaserun: phase=%d decisions for the prompt unreadable: %v", phaseID, err)
		}
		return nil, nil
	}
	var deps []string
	_ = json.Unmarshal([]byte(depsJSON), &deps)
	want := make(map[string]bool, len(deps))
	for _, id := range deps {
		want[strings.ToUpper(strings.TrimSpace(id))] = true
	}
	ds, err := store.ListTaskDecisions(s.DB, taskID)
	if err != nil {
		log.Printf("warning: phaserun: phase=%d decisions for the prompt unreadable: %v", phaseID, err)
		return nil, nil
	}
	for _, d := range ds {
		switch {
		case want[strings.ToUpper(d.DecisionID)] && d.Cost == store.DecisionCostCheap && d.Status != store.DecisionDecided:
			recommended = append(recommended, d)
		case d.Source == store.DecisionSourceStop && d.Status == store.DecisionDecided && d.PhaseID != nil && *d.PhaseID == phaseID:
			answered = append(answered, d)
		}
	}
	if len(answered) > maxPromptDecisions {
		answered = answered[len(answered)-maxPromptDecisions:]
	}
	return recommended, answered
}

// decisionsNoteFor is the run prompt's DecisionsNote for one phase.
func (s *Service) decisionsNoteFor(phaseID, taskID int64) string {
	return DecisionsNote(s.runDecisions(phaseID, taskID))
}

// stopAnsweredSinceBlock reports whether the phase's last run ended blocked and
// the operator has answered one of its stop decisions since: the re-run then
// continues on the phase's own branch, like a returned run, so the partial work
// the stopped run committed is built on rather than refused as a dirty branch.
// What counts depends on what the run did to protected paths. A decision whose
// guard check flagged the run (it changed a protected file, or the hook refused
// it) counts only when answered allow-add-to-scope — a guard decision's
// `allow (add to scope)…` answer, or a stop's exact allow-add-to-scope option:
// any other answer, reject or an agent option or the operator's own words, left
// the protected edits that branch holds unaccepted, so the re-run must not build
// on them and the dirty-branch refusal stands. An agent-declared stop whose
// guard check found nothing counts with any answer.
func (s *Service) stopAnsweredSinceBlock(phaseID int64, info phaseInfo) bool {
	if info.RunState != "blocked" {
		return false
	}
	ended, err := time.Parse(time.RFC3339, info.RunEndedAt)
	if err != nil {
		return false
	}
	ds, err := store.ListTaskDecisions(s.DB, info.WorkspaceTaskID)
	if err != nil {
		return false
	}
	for _, d := range ds {
		if d.Source != store.DecisionSourceStop || d.Status != store.DecisionDecided || d.DecidedAt == nil ||
			d.PhaseID == nil || *d.PhaseID != phaseID {
			continue
		}
		if !answerContinuesBranch(d) {
			continue
		}
		if at, err := time.Parse(time.RFC3339, *d.DecidedAt); err == nil && !at.Before(ended) {
			return true
		}
	}
	return false
}

// answerContinuesBranch reports whether decided decision d's answer lets the
// re-run build on the stopped run's branch (stopAnsweredSinceBlock).
func answerContinuesBranch(d store.Decision) bool {
	rec := decodeStopRecord(d.GuardJSON)
	if rec.Stop == nil {
		return strings.HasPrefix(d.Answer, guardAnswerAllow)
	}
	if !rec.GuardResult.Flagged() {
		return true
	}
	return stopAnswerOption(d.Answer) == GuardOptionAllow
}

// stopAnswerOption is the option id a stop's recorded answer chose: `<id>` or
// `<id> — <note>` (answerStopDecision), "" for the operator's own words
// (`other: <text>`). Option ids are bare tokens (mdfence.ValidOptionID), so the
// id cannot itself contain the separator.
func stopAnswerOption(answer string) string {
	if strings.HasPrefix(answer, mdfence.StopOptionOther+": ") {
		return ""
	}
	id, _, _ := strings.Cut(answer, " — ")
	return id
}
