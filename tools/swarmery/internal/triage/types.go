package triage

import (
	"context"
	"encoding/json"
	"errors"
)

// Scope narrows a run to one project; ProjectID 0 is the whole fleet.
type Scope struct{ ProjectID int64 }

// Part is one decidable piece of an Item: Ref names the target the Source
// applies a value to, Allowed is the closed set of values the Source accepts.
type Part struct {
	Ref, Label string
	Allowed    []string
}

// Item is one waiting inbox entry a Source hands to a run.
type Item struct {
	Kind, Class, Key      string // Key groups parts for the UI (classifier: session uuid)
	ProjectID             int64
	Title, WaitingSince   string
	Instruction, Evidence string // what the judge is asked and shown
	Parts                 []Part
}

// Answer is a judge's (or Decider's) reply for one Item.
type Answer struct {
	Values      map[string]string // Part.Ref → value
	Reason      string
	Payload     json.RawMessage
	SessionUUID string
	CostUSD     float64
}

// Suggestion is a follow-up verdict a Source proposes after an Apply; it is
// stored only after its own policy check.
type Suggestion struct {
	Kind, Class, Ref, ItemKey, Title, Value, Reason string
	Payload                                         json.RawMessage
	ProjectID                                       int64 // the suggested item's project; 0 = none
}

// Applied is what Source.Apply returns: the prior state (kept for Undo) and
// any follow-up suggestions.
type Applied struct {
	Prior  json.RawMessage
	Follow []Suggestion
}

// Source is one inbox kind the triage agent can work on.
//
// Every Item, Part, payload and Verdict the engine passes to a Source method
// is a copy: changing it has no effect on what the engine validates, applies
// or records. Likewise the engine copies what Collect and Apply return before
// it uses them, so a Source may keep and reuse its own slices.
type Source interface {
	Kind() string
	// Collect is a cheap listing of the waiting items in scope; limit 0 means
	// every candidate. The run drops blocked items and applies its cap itself,
	// so expensive per-item work belongs in Preparer, not here.
	Collect(ctx context.Context, sc Scope, limit int) ([]Item, error)
	Apply(ctx context.Context, it Item, p Part, value, reason string, payload json.RawMessage) (Applied, error)
	// Undo reverts an applied verdict from v.Prior. It MUST be idempotent: a
	// crash between the engine's claim and its result leaves the verdict to be
	// healed back to applied, so Undo may run again for a write it already
	// reverted. The engine may also call it with an unrecorded verdict (ID 0)
	// when an applied write could not be recorded.
	Undo(ctx context.Context, v Verdict) error
	Open(ctx context.Context, ref string) (bool, error) // does ref still wait as it did?
}

// Decider is optional on a Source: a rule decides, no model call is made.
// Decide receives a copy of the item; changing it has no effect.
type Decider interface {
	Decide(it Item) (Answer, bool)
}

// Preparer is optional on a Source: it fills an Item's expensive fields (e.g.
// Evidence) just before the item is decided, only for items the run will
// actually judge. An error fails every part of the item; nothing is applied.
//
// Prepare may fill ONLY Instruction and Evidence, and the engine enforces it:
// Prepare runs on a deep copy of the item and only those two fields are copied
// back. Kind, Class, Key, ProjectID, Title, WaitingSince and Parts stay what
// Collect returned and what passed the kind and blocking checks; any change
// Prepare makes to them is discarded.
type Preparer interface {
	Prepare(ctx context.Context, it *Item) error
}

// Judge answers an Item with one value per part. It receives a copy of the
// item; changing it has no effect.
type Judge interface {
	Judge(ctx context.Context, it Item) (Answer, error)
}

// Verdict states (triage_verdicts.state).
const (
	StateApplied   = "applied"
	StateSuggested = "suggested"
	StateSample    = "sample"
	StateAccepted  = "accepted"
	StateAudited   = "audited"
	StateStale     = "stale"
	StateUndone    = "undone"
	// StateUndoing is a claimed undo whose Source.Undo has not finished yet;
	// it ends as undone (success) or applied (failure, or HealStale after a
	// crash). It blocks re-judging (a reverted ref must not be re-applied
	// while its row is stuck here) but does not count as open for SweepOpen.
	StateUndoing  = "undoing"
	StateRejected = "rejected"
	StateFailed   = "failed"
	StateSkipped  = "skipped"
)

// Run triggers and statuses (triage_runs.trigger / .status).
const (
	TriggerOperator = "operator"
	TriggerSchedule = "schedule"
	StatusRunning   = "running"
	StatusOK        = "ok"
	StatusFailed    = "failed"
)

// Run is one row of triage_runs.
type Run struct {
	ID             int64    `json:"id"`
	Trigger        string   `json:"trigger"`
	ScopeProjectID *int64   `json:"scopeProjectId"`
	Kinds          []string `json:"kinds"`
	Status         string   `json:"status"`
	Total          int      `json:"total"`
	Done           int      `json:"done"`
	Applied        int      `json:"applied"`
	Suggested      int      `json:"suggested"`
	Skipped        int      `json:"skipped"`
	Failed         int      `json:"failed"`
	Rejected       int      `json:"rejected"`
	CostUSD        float64  `json:"costUsd"`
	SessionUUIDs   []string `json:"sessionUuids"`
	Error          string   `json:"error"`
	StartedAt      string   `json:"startedAt"`
	FinishedAt     *string  `json:"finishedAt"`
}

// Verdict is one row of triage_verdicts.
type Verdict struct {
	ID        int64           `json:"id"`
	RunID     int64           `json:"runId"`
	Kind      string          `json:"kind"`
	Class     string          `json:"class"`
	Ref       string          `json:"ref"`
	ItemKey   string          `json:"itemKey"`
	Title     string          `json:"title"`
	Value     string          `json:"value"`
	Reason    string          `json:"reason"`
	Payload   json.RawMessage `json:"payload"`
	Prior     json.RawMessage `json:"prior"`
	State     string          `json:"state"`
	CreatedAt string          `json:"createdAt"`
	DecidedAt *string         `json:"decidedAt"`
	ProjectID *int64          `json:"projectId"` // the item's project; nil = none
}

// StartReq asks for one run.
type StartReq struct {
	Scope   Scope
	Kinds   []string // empty = every registered kind
	Trigger string   // "" = operator
	Cap     int      // max items across the run; <=0 = DefaultCap
}

// VerdictFilter narrows ListVerdicts; zero values mean "any".
type VerdictFilter struct {
	States    []string
	Kind      string
	RunID     int64
	ProjectID int64 // the verdict's own project, or a project-less verdict
	Limit     int
}

var (
	// ErrBusy is returned by Start while another run is active.
	ErrBusy = errors.New("triage: a run is already active")
	// ErrNotUndoable is returned by Undo for a verdict that was not applied.
	ErrNotUndoable = errors.New("triage: verdict is not undoable")
	// ErrNotFound is returned for an unknown run or verdict id.
	ErrNotFound = errors.New("triage: not found")
)
