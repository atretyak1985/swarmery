package verify

import (
	"strconv"
	"strings"
)

// Target kinds — the prefix of a Target.Key, and the only two surfaces this
// package grades. Exported because the reaper and the startup heal have to route a
// verification_runs row back to the table it must stamp, and they see nothing but
// the key.
const (
	KindTask  = "task"
	KindPhase = "phase"
)

// TaskKey / PhaseKey mint the single-flight key for each surface. One function per
// kind rather than one fmt call at every site: the key is a storage contract (it is
// what idx_verification_running is unique on, and what the tree-hash memo is keyed
// by), and a second spelling of "task:" would silently open a second single-flight
// namespace for the same card.
func TaskKey(taskID int64) string   { return KindTask + ":" + strconv.FormatInt(taskID, 10) }
func PhaseKey(phaseID int64) string { return KindPhase + ":" + strconv.FormatInt(phaseID, 10) }

// SplitKey parses a Target.Key back into its kind and row id. ok=false on anything
// it does not recognize — a key from a future kind, or a corrupted row. Callers must
// treat that as "cannot route this verdict" and skip, never as a default kind: the
// wrong guess writes a verdict onto an unrelated row.
func SplitKey(key string) (kind string, id int64, ok bool) {
	k, rest, found := strings.Cut(key, ":")
	if !found {
		return "", 0, false
	}
	if k != KindTask && k != KindPhase {
		return "", 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return k, id, true
}

// Target is what one verification run grades: a live worktree, the branch it sits
// on, the base its diff is measured against, and where the verdict gets stamped.
// Board cards and phase runs both produce one — the engine (VerifyTarget) is
// identical for both, which is the whole point of the generalization: the *target*
// varies, never the grading.
//
// Everything the engine needs is IN here. It performs no row lookup of its own, so
// nothing about "a task" leaks into a path a phase also walks.
type Target struct {
	// Key is the single-flight identity: "task:<id>" | "phase:<id>". It is what the
	// partial unique index on verification_runs rejects a second in-flight row for,
	// and what the tree-hash cache is keyed by. Use TaskKey/PhaseKey.
	Key string
	// TaskID populates verification_runs.task_id — the nullable FK that gives a
	// board card's history the same cascade-on-delete it always had. 0 for every
	// non-task target, stored as NULL (see 0058 for why a phase gets no FK).
	TaskID int64
	// WorktreePath is the checkout being graded. Empty ⇒ ErrNoWorktree: there is
	// nothing to read, and a verdict on an absent worktree would be invented.
	WorktreePath string
	// Branch is the branch the work sits on. Descriptive only — the diff base is
	// StartPoint, deliberately: a branch diffed against itself is always empty, which
	// is what made the scope gate unfireable before 0051.
	Branch string
	// StartPoint is the SHA the worktree was pinned to — the honest base for
	// `diff base...HEAD`. "" ⇒ the scope gate is skipped and the prompt falls back to
	// naming Branch, because an unmeasurable diff is not evidence of a huge one.
	StartPoint string
	// Title and Prompt are the verifier's subject and its contract: for a card, the
	// task title and prompt; for a phase, the phase name and its doc, which is where
	// that phase's acceptance criteria live.
	Title, Prompt string
	// FocusHint, when set, is appended to the contract as a clearly labelled
	// section telling the verifier where to look first (the learning loop's
	// surprise summary). "" leaves the prompt byte-identical to before.
	FocusHint string
	// Model overrides the verifier's model ("" ⇒ DefaultModel), and ProjectPath is
	// the repo root the Claude account binding resolves from — never the worktree,
	// which carries no settings of its own.
	Model, ProjectPath string
	// Strictness is the bar the prompt is built at. strictnessOff makes VerifyTarget
	// a no-op before any row is written (nothing graded, nothing stamped).
	Strictness Strictness
	// Stamp writes the verdict + detail onto whichever row this target represents
	// (tasks for a card, epic_phases for a phase). Required: a target with no way to
	// record its answer has nothing to run for. The detail handed here is already
	// truncated to the schema's budget.
	Stamp func(verdict Verdict, detail string) error
	// OnFail is the follow-up a FAIL verdict triggers after it is stamped — the
	// board's fix-task chain (budget walk, dedup, create-or-pause). nil ⇒ the verdict
	// is the end of it, which is exactly what a phase target wants: spawning fix work
	// for a plan is a different decision, made by the operator on the Plans page.
	OnFail func(detail string) error
	// Notify nudges whichever live surface shows this verdict (the board's
	// task_updated, the Plans page's plan_updated). nil ⇒ no nudge; the verdict is
	// still durable and shows up on the next fetch.
	Notify func()
}

// notify fires the target's nudge when one is wired.
func (t Target) notify() {
	if t.Notify != nil {
		t.Notify()
	}
}

// onFail runs the target's fail follow-up. A nil hook is a deliberate answer
// ("stamp and stop"), not a missing one, so it is nil-safe here rather than guarded
// at each of the two call sites.
func (t Target) onFail(detail string) error {
	if t.OnFail == nil {
		return nil
	}
	return t.OnFail(detail)
}

// taskIDValue maps Target.TaskID to what verification_runs.task_id stores: the id
// for a board card, NULL for everything else. A literal 0 would be a foreign-key
// violation against tasks(id) — the column has to be absent, not zero.
func (t Target) taskIDValue() any {
	if t.TaskID <= 0 {
		return nil
	}
	return t.TaskID
}
