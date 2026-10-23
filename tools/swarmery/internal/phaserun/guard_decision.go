package phaserun

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/mdfence"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// The guard decision (D25): a run that touched protected paths — changed them, or
// tried to and was refused by the hook — is not killed mid-run. It finishes, and
// then the operator is asked, through a source='stop' phase_decisions row the
// needs-you queue lists as kind phase_decision:
//
//	The run touched protected paths: <list>. Allow (add to scope) or reject?
//
// Allow appends the paths to the guard block's `allow:` list, so the next run may
// edit them; reject keeps the guard as it is. Either answer is recorded as a
// `- **D-n** (stop) — … → …` line under the phase doc's `## Decisions`, which also
// changes the blocked re-run fingerprint (blocked.go hashes the doc body).

// Guard decision options, in the order the operator sees them.
const (
	GuardOptionAllow  = "allow-add-to-scope"
	GuardOptionReject = "reject"
)

// GuardOptions are the option ids a guard decision offers.
var GuardOptions = []string{GuardOptionAllow, GuardOptionReject}

// guardQuestion is the decision's question for a flagged guard result.
func guardQuestion(r GuardResult) string {
	paths := r.Paths()
	list := strings.Join(paths, ", ")
	if list == "" {
		list = "(the guard refused " + plural(r.GuardBlocksTotal, "attempt") + "; see guard.log)"
	}
	return "The run touched protected paths: " + list + ". Allow (add to scope) or reject?"
}

// guardStage runs the guard check of a finished run and, when the run touched
// protected paths, raises the guard decision. Called from runAndHandle's defer
// BEFORE the review stage, whatever the run's end state — a run that ended
// PHASE DONE is exactly the one whose report must not be the only word. A panic
// is logged and read as "no result": the review, the verifier and the worktree
// removal after it still run.
func (s *Service) guardStage(phaseID int64, info phaseInfo, base string, worktreePath string) (res GuardResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("error: phaserun: phase=%d guard check panicked: %v", phaseID, r)
			res = GuardResult{}
		}
	}()
	if worktreePath == "" {
		return GuardResult{}
	}
	if strings.TrimSpace(base) == "" {
		base = s.runBaseFallback(info.RepoRoot, worktreePath)
	}
	res = GuardCheck(s.reviewGit(), GuardInput{DocPath: info.DocPath, LentDoc: info.LentDoc, Worktree: worktreePath, Base: base})
	if res.HasFindings() {
		log.Printf("warning: phaserun: phase=%d guard check: changed=%v blocks=%d docCopyMismatch=%v parseError=%v",
			phaseID, res.ProtectedChanged, res.GuardBlocksTotal, res.DocCopyMismatch, res.ParseError)
	}
	if res.Flagged() {
		s.raiseGuardDecision(phaseID, info, res)
	}
	return res
}

// runBaseFallback is the run's base when it recorded no start point: the
// merge-base of the worktree's HEAD and the repository's checked-out branch. ""
// when neither resolves — GuardCheck then reports the diff as not run.
func (s *Service) runBaseFallback(repoRoot, worktreePath string) string {
	branch := s.baseBranch(repoRoot)
	if branch == "" {
		return ""
	}
	out, err := s.reviewGit().Run(worktreePath, "merge-base", "HEAD", branch)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// raiseGuardDecision writes (or refreshes) the phase's open guard decision.
// Best-effort: a lost decision is logged, never turned into a run failure.
func (s *Service) raiseGuardDecision(phaseID int64, info phaseInfo, res GuardResult) {
	d, created, err := store.UpsertStopDecision(s.DB, info.WorkspaceTaskID, phaseID, guardQuestion(res),
		GuardOptions, guardJSON(res), s.ts())
	if err != nil {
		log.Printf("error: phaserun: phase=%d guard decision not recorded: %v", phaseID, err)
		return
	}
	verb := "refreshed"
	if created {
		verb = "raised"
	}
	log.Printf("phaserun: phase=%d guard decision %s %s: %s", phaseID, d.DecisionID, verb, d.Question)
	s.notify(info.WorkspaceTaskID)
}

// Guard decision answer errors, mapped to HTTP by the API.
var (
	// ErrDecisionNotFound: no such decision on this phase of this plan (404).
	ErrDecisionNotFound = errors.New("decision not found")
	// ErrDecisionNotOpen: the decision is not an undecided guard decision (409).
	ErrDecisionNotOpen = errors.New("decision is not an open guard decision")
	// ErrDecisionBadOption: the answer names no option the decision offers (400).
	ErrDecisionBadOption = errors.New("unknown option")
)

// answerMu serializes answers: two clicks racing on one decision must not both
// append their line to the doc before one of them loses the row update.
var answerMu sync.Mutex

// GuardAnswer is the operator's answer to a guard decision.
type GuardAnswer struct {
	Option string
	// Note is optional free text, appended to the recorded answer.
	Note string
}

// AnswerGuardDecision records the operator's answer to a guard decision of phase
// phaseID: the `D-n` line under the phase doc's `## Decisions`, the guard block's
// `allow:` list on allow-add-to-scope, then the row stamped decided. The doc is
// written before the row: a doc write that fails leaves the decision open to be
// answered again, never a decided row the doc does not show. Returns the row as
// decided.
func AnswerGuardDecision(db *sql.DB, taskID, phaseID int64, decisionID string, ans GuardAnswer, now time.Time) (store.Decision, error) {
	answerMu.Lock()
	defer answerMu.Unlock()
	d, err := store.GetTaskDecision(db, taskID, decisionID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (d.PhaseID == nil || *d.PhaseID != phaseID)) {
		return store.Decision{}, ErrDecisionNotFound
	}
	if err != nil {
		return store.Decision{}, err
	}
	if d.Source != store.DecisionSourceStop || d.Status != store.DecisionUndecided {
		return store.Decision{}, ErrDecisionNotOpen
	}
	ans.Option = strings.TrimSpace(ans.Option)
	if ans.Option != GuardOptionAllow && ans.Option != GuardOptionReject {
		return store.Decision{}, fmt.Errorf("%w %q (want %s or %s)", ErrDecisionBadOption, ans.Option, GuardOptionAllow, GuardOptionReject)
	}
	var docPath string
	if err := db.QueryRow(`SELECT doc_path FROM epic_phases WHERE id = ? AND workspace_task_id = ?`, phaseID, taskID).
		Scan(&docPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.Decision{}, ErrDecisionNotFound
		}
		return store.Decision{}, err
	}
	var guard GuardResult
	_ = json.Unmarshal([]byte(d.GuardJSON), &guard)
	paths := guard.Paths()

	answer := "reject"
	if ans.Option == GuardOptionAllow {
		answer = "allow (add to scope)"
		if len(paths) > 0 {
			answer += ": " + strings.Join(paths, ", ")
		}
	}
	if note := strings.Join(strings.Fields(ans.Note), " "); note != "" {
		answer += " — " + note
	}

	body, err := os.ReadFile(docPath)
	if err != nil {
		return store.Decision{}, fmt.Errorf("read phase doc: %w", err)
	}
	doc := wsingest.AppendPhaseDecision(string(body), wsingest.FormatStopDecisionLine(d.Seq, d.Question, answer))
	if ans.Option == GuardOptionAllow && len(paths) > 0 {
		widened, perr := mdfence.AddGuardAllow(doc, paths)
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
