package triage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultCap bounds the items one run visits when StartReq.Cap is unset.
	DefaultCap = 50
	// MaxCap is the most items one run may visit: every item is a paid judge
	// call, so a larger StartReq.Cap is clamped to it.
	MaxCap = 1000
	// JudgeTimeout bounds one judge call.
	JudgeTimeout = 120 * time.Second
	// maxSessions caps the session uuids recorded on a run.
	maxSessions = 50
)

// Service owns triage runs: the single-flight run loop, the verdict store and
// the undo/sweep paths. Seams (now, Go, perm) make runs deterministic in tests.
type Service struct {
	DB    *sql.DB
	Judge Judge

	now  func() time.Time  // clock seam
	Go   func(func())      // async-spawn seam (nil ⇒ real `go`)
	perm func(n int) []int // sample-pick seam (nil ⇒ rand.Perm)
	// insertVerdictFn is the verdict-insert seam (nil ⇒ insertVerdict).
	insertVerdictFn func(Verdict) (int64, error)

	mu       sync.Mutex
	activeID int64             // id of the live run, 0 when idle
	sources  map[string]Source // registered by Kind()
	// lastSweep is when MaybeSweepOpen last ran a sweep (service clock).
	lastSweep time.Time
}

// NewService builds a triage service around db and judge j.
func NewService(db *sql.DB, j Judge) *Service {
	return &Service{DB: db, Judge: j, now: time.Now, sources: map[string]Source{}}
}

// Register adds a Source; a later Source of the same kind replaces it.
//
// Kind() is read exactly once, here. The registry key is the Source's kind for
// every check, policy lookup and verdict row from then on: the engine never
// asks the Source for its kind again, so a Kind() that answers differently on a
// later call cannot move an item to another policy rule.
func (s *Service) Register(src Source) {
	kind := src.Kind()
	s.mu.Lock()
	s.sources[kind] = src
	s.mu.Unlock()
}

func (s *Service) source(kind string) Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sources[kind]
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) spawn(fn func()) {
	wrapped := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("error: triage: goroutine panic recovered: %v", r)
			}
		}()
		fn()
	}
	if s.Go != nil {
		s.Go(wrapped)
		return
	}
	go wrapped()
}

func (s *Service) permute(n int) []int {
	if s.perm != nil {
		return s.perm(n)
	}
	return rand.Perm(n)
}

// Start records a run and executes it in the background. A Cap ≤ 0 becomes
// DefaultCap and a Cap above MaxCap is clamped to MaxCap.
//
// It returns ErrBusy while a run is live in THIS process. A 'running' row that
// no goroutine here owns (a panic, a failed finish write) is an orphan: it is
// marked failed and the new run starts. Single-flight therefore holds within
// one daemon process only; two processes on one database are not excluded.
func (s *Service) Start(req StartReq) (int64, error) {
	if req.Trigger == "" {
		req.Trigger = TriggerOperator
	}
	if req.Trigger != TriggerOperator && req.Trigger != TriggerSchedule {
		return 0, fmt.Errorf("triage: unknown trigger %q", req.Trigger)
	}
	if req.Cap <= 0 {
		req.Cap = DefaultCap
	}
	req.Cap = min(req.Cap, MaxCap)
	if req.Kinds == nil {
		req.Kinds = []string{}
	}
	s.mu.Lock()
	if s.activeID != 0 {
		s.mu.Unlock()
		return 0, ErrBusy
	}
	// No goroutine in this process owns a run (activeID == 0), so a 'running'
	// row is an orphan (panic, failed finish write): end it and proceed.
	if _, err := s.DB.Exec(`UPDATE triage_runs SET status='failed', error='run ended without a result', finished_at=?
		WHERE status='running'`, fmtTS(s.clock())); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	id, err := s.insertRun(req)
	if err != nil {
		s.mu.Unlock()
		return 0, err
	}
	s.activeID = id
	s.mu.Unlock()

	s.spawn(func() {
		defer func() {
			s.mu.Lock()
			s.activeID = 0
			s.mu.Unlock()
		}()
		s.execute(id, req)
	})
	return id, nil
}

// kindsFor is requested ∩ registered, in the policy table's fixed order.
func (s *Service) kindsFor(requested []string) []string {
	var out []string
	for _, k := range kindOrder {
		if len(requested) > 0 && !slices.Contains(requested, k) {
			continue
		}
		if s.source(k) != nil {
			out = append(out, k)
		}
	}
	return out
}

func (s *Service) execute(id int64, req StartReq) {
	ctx := context.Background()
	run := &Run{ID: id, SessionUUIDs: []string{}}
	status, errMsg := StatusOK, ""
	// The row is ALWAYS ended, even when a Source or the judge panics.
	defer func() {
		if r := recover(); r != nil {
			status, errMsg = StatusFailed, fmt.Sprintf("panic: %v", r)
			log.Printf("error: triage: run %d panic recovered: %v", id, r)
		}
		if err := s.saveCounters(run); err != nil {
			log.Printf("warning: triage: run %d counters: %v", id, err)
		}
		if err := s.finishRun(id, status, errMsg); err != nil {
			log.Printf("warning: triage: run %d finish: %v", id, err)
		}
	}()
	for _, kind := range s.kindsFor(req.Kinds) {
		remaining := req.Cap - run.Total
		if remaining <= 0 {
			break
		}
		if err := s.runKind(ctx, run, kind, s.source(kind), req.Scope, remaining); err != nil {
			status, errMsg = StatusFailed, err.Error()
			break
		}
	}
}

// runKind triages one kind. kind is the registry key src was registered under
// (see Register) — never src.Kind() again.
func (s *Service) runKind(ctx context.Context, run *Run, kind string, src Source, sc Scope, limit int) error {
	// Collect every candidate (0 = no limit): blocked items must not eat the
	// cap, so the cap applies only after they are dropped.
	collected, err := src.Collect(ctx, sc, 0)
	if err != nil {
		return fmt.Errorf("collect %s: %w", kind, err)
	}
	// The run works on its own deep copy of what Collect returned: a Source
	// that keeps (and later changes) the slices it handed out cannot change an
	// item between the blocking check below and the item's turn.
	items := make([]Item, len(collected))
	for i, it := range collected {
		items[i] = copyItem(it)
	}
	fresh := items[:0:0]
	for _, it := range items {
		if len(fresh) >= limit {
			break
		}
		// An item with no parts has nothing to decide: judging it would be
		// paid for and leave no row, so it would come back every run.
		if len(it.Parts) == 0 {
			log.Printf("warning: triage: run %d dropped %s item %q: it has no parts", run.ID, kind, it.Key)
			run.Skipped++
			continue
		}
		blocked, err := s.itemBlocked(kind, it)
		if err != nil {
			return err
		}
		if !blocked {
			fresh = append(fresh, it)
		}
	}
	sample := map[int]bool{}
	if n := sampleMax(kind); n > 0 {
		for _, i := range s.permute(len(fresh)) {
			if len(sample) >= n {
				break
			}
			sample[i] = true
		}
	}
	run.Total += len(fresh)
	if err := s.saveCounters(run); err != nil {
		return err
	}
	for i, it := range fresh {
		err := s.triageItem(ctx, run, kind, src, it, sample[i])
		run.Done++
		if err != nil {
			return err
		}
		if err := s.saveCounters(run); err != nil {
			log.Printf("warning: triage: run %d counters: %v", run.ID, err)
		}
	}
	return nil
}

// itemBlocked reports whether ANY of the item's parts is blocked (partBlocked).
func (s *Service) itemBlocked(kind string, it Item) (bool, error) {
	for _, p := range it.Parts {
		blocked, err := s.partBlocked(kind, p.Ref)
		if err != nil || blocked {
			return blocked, err
		}
	}
	return false, nil
}

// answer asks the Source's Decider, else the judge, for the item's values. The
// returned Answer carries the call's cost and session even with an error. A
// part without a value is skipped later; an answer with no value for ANY part
// is an error.
//
// it is the engine's own snapshot: Decide and Judge each get a fresh deep copy
// of it, and the Answer they return is copied before the engine reads it.
func (s *Service) answer(ctx context.Context, src Source, it Item) (Answer, error) {
	if d, ok := src.(Decider); ok {
		if a, ok := d.Decide(copyItem(it)); ok {
			a = copyAnswer(a)
			return a, requireAnyValue(it, a)
		}
	}
	if s.Judge == nil {
		return Answer{}, fmt.Errorf("no judge configured")
	}
	jctx, cancel := context.WithTimeout(ctx, JudgeTimeout)
	defer cancel()
	a, err := s.Judge.Judge(jctx, copyItem(it))
	a = copyAnswer(a)
	if err != nil {
		return a, err
	}
	return a, requireAnyValue(it, a)
}

// partValue is the answer's value for ref; ok is false when it has none.
func partValue(a Answer, ref string) (string, bool) {
	v := strings.TrimSpace(a.Values[ref])
	return v, v != ""
}

func requireAnyValue(it Item, a Answer) error {
	for _, p := range it.Parts {
		if _, ok := partValue(a, p.Ref); ok {
			return nil
		}
	}
	return fmt.Errorf("answer has no value for any part")
}

func (s *Service) insert(v Verdict) (int64, error) {
	if s.insertVerdictFn != nil {
		return s.insertVerdictFn(v)
	}
	return s.insertVerdict(v)
}

// record stores a verdict that caused no write; a failed insert is logged and
// the run goes on (nothing changed that would need an audit row).
func (s *Service) record(run *Run, v Verdict) {
	v.RunID = run.ID
	if _, err := s.insert(v); err != nil {
		log.Printf("warning: triage: run %d verdict %s/%s: %v", run.ID, v.Kind, v.Ref, err)
		return
	}
	count(run, v.State)
}

// recordApplied stores the verdict of a write Source.Apply already made,
// retrying the insert once. If it still fails the write would have no audit
// row and no undo, so it is reverted (best effort, Source.Undo with the
// in-memory verdict), counted as failed, and the returned error ends the run.
// The error says whether the revert worked ("; reverted") or not ("; REVERT
// FAILED: …"); a failed revert is also logged with everything needed to revert
// the write by hand (run, kind, ref, item key, value and the prior state).
func (s *Service) recordApplied(ctx context.Context, run *Run, src Source, v Verdict) error {
	v.RunID = run.ID
	_, err := s.insert(v)
	if err != nil {
		log.Printf("warning: triage: run %d applied verdict %s/%s: %v (retrying)", run.ID, v.Kind, v.Ref, err)
		_, err = s.insert(v)
	}
	if err == nil {
		count(run, v.State)
		return nil
	}
	run.Failed++
	if uerr := src.Undo(ctx, copyVerdict(v)); uerr != nil {
		log.Printf("error: triage: REVERT FAILED for an unrecorded write — revert by hand: run=%d kind=%s ref=%q item=%q value=%q prior=%s: undo: %v; verdict insert: %v",
			run.ID, v.Kind, v.Ref, v.ItemKey, v.Value, string(v.Prior), uerr, err)
		return fmt.Errorf("applied %s/%s but could not record the verdict: %w; REVERT FAILED: %w", v.Kind, v.Ref, err, uerr)
	}
	return fmt.Errorf("applied %s/%s but could not record the verdict: %w; reverted", v.Kind, v.Ref, err)
}

// count books one recorded verdict on the run's counters.
func count(run *Run, state string) {
	switch state {
	case StateApplied:
		run.Applied++
	case StateSuggested, StateSample:
		run.Suggested++
	case StateSkipped:
		run.Skipped++
	case StateRejected:
		run.Rejected++
	case StateFailed:
		run.Failed++
	}
}

// recordAll records one verdict per part, all in the same state and reason.
func (s *Service) recordAll(run *Run, base Verdict, parts []Part, state, reason string) {
	for _, p := range parts {
		v := base
		v.Ref, v.State, v.Reason = p.Ref, state, reason
		s.record(run, v)
	}
}

// triageItem decides one item. Its error is fatal for the run: an applied
// write whose verdict could not be recorded (see recordApplied).
//
// Invariant: the engine never hands its own item to code it does not own. It
// first takes a private snapshot (own) and from then on reads only that — for
// the kind check, the policy decision, the ref and Allowed it validates, and
// the verdict rows. Every hook (Prepare, Decide, Judge, Apply) receives a fresh
// deep copy instead, so whatever a hook changes in what it was handed has no
// effect on what the engine validates, applies or records.
//
// kind is the registry key src was registered under. The engine does not call
// src.Kind() here: the kind used for the match below, for the policy lookup
// (own.Kind, proven equal to it) and for the verdict rows is one value, read
// once at registration.
func (s *Service) triageItem(ctx context.Context, run *Run, kind string, src Source, it Item, isSample bool) error {
	own := copyItem(it)
	base := Verdict{Kind: kind, Class: own.Class, ItemKey: own.Key, Title: own.Title,
		ProjectID: projectRef(own.ProjectID)}
	// A Source may only offer items of its own kind: the policy table is keyed
	// by kind, so a foreign item is never prepared, judged or applied.
	if own.Kind != kind {
		s.recordAll(run, base, own.Parts, StateRejected,
			fmt.Sprintf("source %s offered an item of kind %s", kind, own.Kind))
		return nil
	}
	if p, ok := src.(Preparer); ok {
		// Prepare works on a deep copy; only Instruction and Evidence come back.
		// Kind, Class, Key, ProjectID, Title, WaitingSince and Parts stay what
		// Collect returned and what passed the kind and blocking checks above.
		prepared := copyItem(own)
		if err := p.Prepare(ctx, &prepared); err != nil {
			s.recordAll(run, base, own.Parts, StateFailed, "prepare: "+err.Error())
			return nil
		}
		own.Instruction, own.Evidence = prepared.Instruction, prepared.Evidence
	}
	a, err := s.answer(ctx, src, own)
	// Book the call's cost and session before looking at the error: a failed
	// judge call was still paid for.
	run.CostUSD += a.CostUSD
	if a.SessionUUID != "" && len(run.SessionUUIDs) < maxSessions {
		run.SessionUUIDs = append(run.SessionUUIDs, a.SessionUUID)
	}
	if err != nil {
		s.recordAll(run, base, own.Parts, StateFailed, err.Error())
		return nil
	}
	var follow []Suggestion
	for _, p := range own.Parts {
		v := base
		value, ok := partValue(a, p.Ref)
		if !ok {
			v.Ref, v.State, v.Reason = p.Ref, StateSkipped, "no answer"
			s.record(run, v)
			continue
		}
		v.Ref, v.Value, v.Reason, v.Payload = p.Ref, value, a.Reason, a.Payload
		switch decide(own.Kind, own.Class, p, v.Value) {
		case DecisionReject:
			v.State = StateRejected
		case DecisionSkip:
			v.State = StateSkipped
		case DecisionSuggest:
			v.State = StateSuggested
		case DecisionAuto:
			if isSample {
				v.State = StateSample
				break
			}
			// Apply gets copies of the item, the part and the payload; what
			// it returns is copied before the engine keeps it.
			applied, err := src.Apply(ctx, copyItem(own), copyPart(p), v.Value, a.Reason, slices.Clone(a.Payload))
			if err != nil {
				v.State, v.Reason = StateFailed, "apply: "+err.Error()
				break
			}
			v.State, v.Prior = StateApplied, slices.Clone(applied.Prior)
			if err := s.recordApplied(ctx, run, src, v); err != nil {
				return err
			}
			for _, f := range applied.Follow {
				f.Payload = slices.Clone(f.Payload)
				follow = append(follow, f)
			}
			continue
		}
		s.record(run, v)
	}
	s.recordFollow(run, follow)
	return nil
}

// copyItem deep-copies it: the Parts slice and every part's Allowed slice. The
// engine hands a fresh copyItem to every hook and keeps its own snapshot, so a
// hook that changes what it received has no effect (see triageItem).
func copyItem(it Item) Item {
	out := it
	if it.Parts != nil {
		out.Parts = make([]Part, len(it.Parts))
		for i, p := range it.Parts {
			out.Parts[i] = copyPart(p)
		}
	}
	return out
}

// copyPart deep-copies p: its Allowed slice is its own.
func copyPart(p Part) Part {
	p.Allowed = slices.Clone(p.Allowed)
	return p
}

// copyAnswer deep-copies a hook's Answer (Values map, Payload bytes), so a
// Source that kept them cannot change a value the engine has yet to read.
func copyAnswer(a Answer) Answer {
	if a.Values != nil {
		vals := make(map[string]string, len(a.Values))
		maps.Copy(vals, a.Values)
		a.Values = vals
	}
	a.Payload = slices.Clone(a.Payload)
	return a
}

// copyVerdict deep-copies v's byte fields (Payload, Prior) before it is handed
// to Source.Undo, so the engine's copy — logged afterwards — stays intact.
func copyVerdict(v Verdict) Verdict {
	v.Payload, v.Prior = slices.Clone(v.Payload), slices.Clone(v.Prior)
	if v.ProjectID != nil {
		id := *v.ProjectID
		v.ProjectID = &id
	}
	if v.DecidedAt != nil {
		at := *v.DecidedAt
		v.DecidedAt = &at
	}
	return v
}

// recordFollow stores a Source's follow-up suggestions; each passes its own
// policy check and is only ever stored as 'suggested' (never applied).
func (s *Service) recordFollow(run *Run, follow []Suggestion) {
	for _, f := range follow {
		v := Verdict{Kind: f.Kind, Class: f.Class, Ref: f.Ref, ItemKey: f.ItemKey, Title: f.Title,
			Value: f.Value, Reason: f.Reason, Payload: f.Payload, ProjectID: projectRef(f.ProjectID)}
		if decide(f.Kind, f.Class, Part{Ref: f.Ref, Allowed: []string{f.Value}}, f.Value) == DecisionSuggest {
			v.State = StateSuggested
		} else {
			v.State = StateRejected
		}
		s.record(run, v)
	}
}

// projectRef maps an item's project id to the verdict's nullable column
// (0 = none = nil).
func projectRef(id int64) *int64 {
	if id == 0 {
		return nil
	}
	return &id
}

// Undo reverts an applied verdict through its Source, exactly once. The row
// is claimed first (applied → undoing in one conditional UPDATE), so of two
// concurrent undos only one reaches Source.Undo; the Source call runs without
// the service mutex. Success ends the row undone; a failed Source.Undo puts it
// back to applied, and so does HealStale when the process died in between.
func (s *Service) Undo(ctx context.Context, id int64) (Verdict, error) {
	v, err := s.GetVerdict(id)
	if err != nil {
		return v, err
	}
	src := s.source(v.Kind)
	if src == nil {
		if v.State != StateApplied {
			return v, ErrNotUndoable
		}
		return v, fmt.Errorf("triage: no source registered for kind %q", v.Kind)
	}
	res, err := s.DB.Exec(`UPDATE triage_verdicts SET state=? WHERE id=? AND state=?`,
		StateUndoing, id, StateApplied)
	if err != nil {
		return v, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return v, err
	} else if n == 0 {
		return v, ErrNotUndoable
	}
	if err := src.Undo(ctx, copyVerdict(v)); err != nil {
		if _, rerr := s.DB.Exec(`UPDATE triage_verdicts SET state=?, decided_at=NULL WHERE id=? AND state=?`,
			StateApplied, id, StateUndoing); rerr != nil {
			return v, errors.Join(err, fmt.Errorf("triage: restore applied state: %w", rerr))
		}
		return v, err
	}
	// The write is reverted: mark it undone, retrying once. If both writes
	// fail the row stays 'undoing', which blocks re-judging (partBlocked)
	// until HealStale restores it to applied, so the ref cannot be re-applied
	// in between.
	markUndone := func() error {
		_, err := s.DB.Exec(`UPDATE triage_verdicts SET state=?, decided_at=? WHERE id=? AND state=?`,
			StateUndone, fmtTS(s.clock()), id, StateUndoing)
		return err
	}
	if err := markUndone(); err != nil {
		log.Printf("warning: triage: verdict %d reverted, marking undone: %v (retrying)", id, err)
		if err := markUndone(); err != nil {
			return v, fmt.Errorf("triage: reverted but could not mark undone: %w", err)
		}
	}
	return s.GetVerdict(id)
}

// sweepBatch is how many open verdicts SweepOpen reads per page.
const sweepBatch = 500

// SweepOpen retires waiting verdicts whose ref no longer waits: suggested →
// stale, sample → audited. Kinds without a registered Source are left alone.
// It pages through EVERY open verdict by id (keyset), not just the newest.
func (s *Service) SweepOpen(ctx context.Context) error {
	var after int64
	for {
		page, err := s.openVerdictsAfter(after, sweepBatch)
		if err != nil {
			return err
		}
		for _, v := range page {
			after = v.ID
			src := s.source(v.Kind)
			if src == nil {
				continue
			}
			still, err := src.Open(ctx, v.Ref)
			if err != nil || still {
				continue
			}
			next := StateStale
			if v.State == StateSample {
				next = StateAudited
			}
			if err := s.setVerdictState(v.ID, v.State, next); err != nil {
				return err
			}
		}
		if len(page) < sweepBatch {
			return nil
		}
	}
}

// openVerdictsAfter returns up to n suggested/sample verdicts with id > after,
// oldest first (the SweepOpen keyset page).
func (s *Service) openVerdictsAfter(after int64, n int) ([]Verdict, error) {
	rows, err := s.DB.Query(`SELECT `+verdictCols+` FROM triage_verdicts v
		WHERE v.state IN (?,?) AND v.id > ? ORDER BY v.id LIMIT ?`,
		StateSuggested, StateSample, after, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verdict
	for rows.Next() {
		v, err := scanVerdict(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// sweepInterval is the minimum gap between two list-triggered sweeps.
const sweepInterval = 10 * time.Second

// MaybeSweepOpen runs SweepOpen unless one ran within sweepInterval (by the
// service clock). The verdict list calls it on every read.
func (s *Service) MaybeSweepOpen(ctx context.Context) error {
	now := s.clock()
	s.mu.Lock()
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < sweepInterval {
		s.mu.Unlock()
		return nil
	}
	s.lastSweep = now
	s.mu.Unlock()
	return s.SweepOpen(ctx)
}
