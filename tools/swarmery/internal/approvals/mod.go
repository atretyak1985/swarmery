// The mod approvals channel (mods-swarmery-link phase 5): the swarmery link
// mod asks from inside its tool.check hook — POST /api/link/permission, then
// repeated GET /api/link/permission/{id} polls of at most 25 s each
// (docs/mods-link-protocol.md, Permission). It is layered on the shim's
// channel, not beside it: the same permission_requests rows, the same D6
// dedup hash, the same risk class, rules, prod-deploy hand-off and dashboard
// decisions. Only the delivery differs.
//
// The shim holds ONE long-poll for the whole approval window, so "a waiter is
// attached" is exactly "someone can receive a decision". The mod polls in
// short GETs with gaps between them, so it claims its row with a LEASE
// instead: leases[id] = now + ModLeaseTTL, renewed by every POST and GET. A
// live lease counts as a waiter for Decide / Answer / Detach, and a decision
// reached while the mod is between polls is kept in modDecided for its next
// GET. A lapsed lease with nobody attached means the asking hook is gone:
// Sweep resolves the row resolved_elsewhere via 'terminal', the same meaning
// as the shim's Detach. A mod that gives up mid-wait says so at once
// (CancelMod) rather than leaving the lease to lapse.
package approvals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// Delivering channels (permission_requests.channel). resolved_via keeps
// meaning WHO decided a row; channel says which hook channel asked.
const (
	ChannelShim = "shim"
	ChannelMod  = "mod"
)

// ViaRule is the resolved_via of a row an auto-approve rule resolved.
const ViaRule = "rule"

// ModLeaseTTL is how long one mod POST or GET keeps its claim on a row: a
// poll holds at most 25 s (ModMaxWait), plus slack for the mod's next request.
const ModLeaseTTL = 40 * time.Second

// ModMaxWait caps one mod poll (GET /api/link/permission/{id}?wait=).
const ModMaxWait = 25 * time.Second

// ShimAfterModWindow is how long after a channel='mod' row reached a terminal
// status a shim request for the same call is answered from that row at once
// (shimDecisionAfterMod) instead of opening a row of its own.
const ShimAfterModWindow = 5 * time.Second

// modDecision is a decision kept for the mod's next poll, stamped for pruning.
type modDecision struct {
	d  Decision
	at time.Time
}

// recentModDecisionLocked finds the channel='mod' row with this hash that
// reached any terminal status within ShimAfterModWindow, and the decision a
// shim request for the same call takes from it (index idx_pr_mod_hash). The
// mod has just handed this very call back to the engine, whose permission
// stage now fires the shim: the operator's decision on the mod's row — a deny
// included — answers it rather than a second row.
func (s *Service) recentModDecisionLocked(hash string) (int64, Decision, bool, error) {
	since := s.opt.Now().UTC().Add(-ShimAfterModWindow).Format(tsFormat)
	var id int64
	var toolName string
	err := s.db.QueryRow(
		`SELECT id, tool_name FROM permission_requests
		 WHERE channel = ? AND dedup_hash = ? AND resolved_at >= ?
		   AND status IN (?, ?, ?, ?)
		 ORDER BY resolved_at DESC, id DESC LIMIT 1`,
		ChannelMod, hash, since,
		StatusApproved, StatusDenied, StatusExpired, StatusResolvedElsewhere).Scan(&id, &toolName)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, Decision{}, false, nil
	case err != nil:
		return 0, Decision{}, false, err
	}
	d, err := s.terminalDecisionLocked(id)
	if err != nil {
		return 0, Decision{}, false, err
	}
	return id, shimDecisionAfterMod(d, toolName), true, nil
}

// shimDecisionAfterMod maps a mod row's terminal decision onto what the
// shim's long-poll answers for the same call:
//
//   - denied → the deny, with the row's reason;
//   - approved with an answer's UpdatedInput (kept in modDecided) → the
//     allow + updatedInput, which the handler delivers per --answer-delivery;
//   - approved without one on an AskUserQuestion row → no decision (204): a
//     plain allow would resolve the question with empty answers (E12d), so the
//     native dialog asks;
//   - approved on any other tool → the allow;
//   - expired / resolved_elsewhere → no decision (204), the native dialog.
func shimDecisionAfterMod(d Decision, toolName string) Decision {
	switch d.Status {
	case StatusDenied:
		return Decision{Status: StatusDenied, Reason: d.Reason, Via: d.Via}
	case StatusApproved:
		if len(d.UpdatedInput) == 0 && toolName == AskUserQuestionTool {
			return Decision{Status: StatusResolvedElsewhere}
		}
		return d
	default:
		return Decision{Status: StatusResolvedElsewhere}
	}
}

// hasWaiterLocked: a long-poll is attached to id, or the mod holds a live
// lease on it.
func (s *Service) hasWaiterLocked(id int64) bool {
	return len(s.waiters[id]) > 0 || s.leaseLiveLocked(id)
}

// leaseLiveLocked reports whether the mod's lease on id has not lapsed yet.
func (s *Service) leaseLiveLocked(id int64) bool {
	until, ok := s.leases[id]
	return ok && s.opt.Now().Before(until)
}

// renewLeaseLocked (re)claims id for the mod for ModLeaseTTL from now.
func (s *Service) renewLeaseLocked(id int64) {
	s.leases[id] = s.opt.Now().Add(ModLeaseTTL)
}

// OpenMod registers one mod tool.check ask. in.Channel is forced to
// ChannelMod. In order:
//
//  1. in.ToolUseID names a row of this session already (a re-POST of the
//     same call): that row, as is — pending, or its terminal decision.
//  2. a PENDING row with the same dedup hash exists (D6, any channel): the
//     mod attaches to it.
//  3. otherwise a new channel='mod' row through the shim's own create path
//     (createLocked): excluded project → ErrExcludedProject, too many pending
//     → ErrTooManyPending, a prod deploy is handed to the terminal at once
//     (Decision resolved_elsewhere, Via ViaLocalOnly), a matching rule
//     approves it at once (Decision approved, Via ViaRule).
//
// pending reports a row still waiting for a decision; d is meaningful only
// when it is false. Every pending return leaves a fresh lease on the row and
// attaches no channel: the mod collects the decision with WaitMod.
func (s *Service) OpenMod(in HookInput) (id int64, d Decision, pending bool, err error) {
	hash, err := DedupHash(in.SessionUUID, in.ToolName, in.ToolInput)
	if err != nil {
		return 0, Decision{}, false, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	in.Channel = ChannelMod

	s.mu.Lock()
	defer s.mu.Unlock()

	if in.ToolUseID != "" {
		id, status, found, err := s.rowByToolUseLocked(in.SessionUUID, in.ToolUseID)
		if err != nil {
			return 0, Decision{}, false, err
		}
		if found {
			if status == StatusPending {
				s.renewLeaseLocked(id)
				return id, Decision{}, true, nil
			}
			d, err := s.terminalDecisionLocked(id)
			return id, d, false, err
		}
	}

	id, found, err := s.pendingByHashLocked(hash)
	if err != nil {
		return 0, Decision{}, false, err
	}
	if found {
		s.renewLeaseLocked(id)
		if in.ToolUseID != "" {
			// A shim-created row has no tool_use_id: carry the mod's, so a
			// re-POST of this call finds the row by step 1 even once it is
			// terminal. A row that already has one keeps it.
			if _, err := s.db.Exec(
				`UPDATE permission_requests SET tool_use_id = ? WHERE id = ? AND tool_use_id IS NULL`,
				in.ToolUseID, id); err != nil {
				log.Printf("warn: approvals: carry tool_use_id onto request %d: %v", id, err)
			}
		}
		return id, Decision{}, true, nil
	}

	id, d, resolved, err := s.createLocked(in, hash, s.renewLeaseLocked)
	if err != nil {
		return 0, Decision{}, false, err
	}
	return id, d, !resolved, nil
}

// rowByToolUseLocked finds the row of (session uuid, tool_use_id) via
// idx_pr_tool_use.
func (s *Service) rowByToolUseLocked(sessionUUID, toolUseID string) (int64, string, bool, error) {
	var id int64
	var status string
	err := s.db.QueryRow(
		`SELECT pr.id, pr.status FROM permission_requests pr
		 JOIN sessions se ON se.id = pr.session_id
		 WHERE se.session_uuid = ? AND pr.tool_use_id = ?
		 ORDER BY pr.id DESC LIMIT 1`,
		sessionUUID, toolUseID).Scan(&id, &status)
	switch {
	case err == nil:
		return id, status, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, "", false, nil
	default:
		return 0, "", false, err
	}
}

// terminalDecisionLocked is the decision of a resolved row for the mod: the
// one kept in modDecided (with an Answer's UpdatedInput), else rebuilt from
// the row's status, reason and resolved_via (after a restart, or once
// pruned — an AskUserQuestion answer's UpdatedInput is not stored, so it is
// lost by then).
func (s *Service) terminalDecisionLocked(id int64) (Decision, error) {
	if md, ok := s.modDecided[id]; ok {
		return md.d, nil
	}
	var d Decision
	var reason, via sql.NullString
	if err := s.db.QueryRow(
		`SELECT status, reason, resolved_via FROM permission_requests WHERE id = ?`, id).
		Scan(&d.Status, &reason, &via); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Decision{}, ErrNotFound
		}
		return Decision{}, err
	}
	d.Reason, d.Via = reason.String, via.String
	return d, nil
}

// WaitMod is one mod poll on row id: it verifies the row belongs to
// sessionUUID (else ErrNotFound), returns a terminal row's decision at once,
// and otherwise renews the lease and waits up to wait for a decision. It
// returns pending=true when the wait elapsed, or ctx ended, with no decision —
// and then detaches WITHOUT resolving (unlike Detach: the mod polls again) and
// renews the lease once more. wait <= 0 only looks.
func (s *Service) WaitMod(ctx context.Context, id int64, sessionUUID string, wait time.Duration) (d Decision, pending bool, err error) {
	s.mu.Lock()
	var status string
	err = s.db.QueryRow(
		`SELECT pr.status FROM permission_requests pr
		 JOIN sessions se ON se.id = pr.session_id
		 WHERE pr.id = ? AND se.session_uuid = ?`,
		id, sessionUUID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		s.mu.Unlock()
		return Decision{}, false, ErrNotFound
	}
	if err != nil {
		s.mu.Unlock()
		return Decision{}, false, err
	}
	if status != StatusPending {
		d, err = s.terminalDecisionLocked(id)
		s.mu.Unlock()
		return d, false, err
	}
	s.renewLeaseLocked(id)
	if wait <= 0 {
		s.mu.Unlock()
		return Decision{}, true, nil
	}
	ch := s.attachLocked(id)
	s.modPolls[ch] = struct{}{}
	s.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case d = <-ch:
		s.mu.Lock()
		delete(s.modPolls, ch)
		s.mu.Unlock()
		return d, false, nil
	case <-ctx.Done():
	case <-timer.C:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.modPolls, ch)
	// CancelMod took this poll off the row (the mod gave up): it is no longer
	// a waiter, and its end must not renew the dropped lease — fall through.
	if set, ok := s.waiters[id]; ok {
		if _, mine := set[ch]; mine {
			delete(set, ch)
			if len(set) == 0 {
				delete(s.waiters, id)
			}
			s.renewLeaseLocked(id)
			return Decision{}, true, nil
		}
	}
	// resolveLocked dropped the channel after sending into it: the decision
	// raced the deadline and is buffered.
	select {
	case d = <-ch:
		return d, false, nil
	default:
		return Decision{}, true, nil
	}
}

// CancelMod is the mod giving up on row id (its hook's dispatch was
// abandoned: the person interrupted, or a hook above settled first). It
// verifies the row belongs to sessionUUID (else ErrNotFound) and is a no-op on
// a terminal row. On a pending row it drops every claim of the mod — its lease
// and its own in-flight polls, which then end without renewing it — and then:
//
//   - a shim long-poll still waits on the row: nothing more, the shim's own
//     decision or Detach settles it;
//   - nobody else waits: the row resolves resolved_elsewhere via 'terminal'
//     at once, exactly what a lapsed lease would record ~40 s later, so a
//     dashboard decision no longer lands on a row no hook will read.
func (s *Service) CancelMod(id int64, sessionUUID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var status string
	err := s.db.QueryRow(
		`SELECT pr.status FROM permission_requests pr
		 JOIN sessions se ON se.id = pr.session_id
		 WHERE pr.id = ? AND se.session_uuid = ?`,
		id, sessionUUID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != StatusPending {
		return nil
	}
	delete(s.leases, id)
	if set, ok := s.waiters[id]; ok {
		for ch := range set {
			if _, isMod := s.modPolls[ch]; isMod {
				delete(set, ch)
			}
		}
		if len(set) > 0 {
			return nil
		}
		delete(s.waiters, id)
	}
	err = s.resolveLocked(id, StatusResolvedElsewhere, "terminal", "", nil)
	if errors.Is(err, ErrAlreadyResolved) {
		return nil
	}
	return err
}

// sweepLeases is Sweep's mod pass: a pending row whose lease lapsed with
// nobody attached is resolved resolved_elsewhere via 'terminal' — the mod that
// asked is gone (its session ended, or it handed the call back to the
// engine), exactly what the shim's Detach records. Kept decisions and stale
// leases older than 2×ModLeaseTTL are dropped.
func (s *Service) sweepLeases() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	for id, until := range s.leases {
		if now.Before(until) {
			continue
		}
		if len(s.waiters[id]) > 0 {
			// A shim attached to the mod's row still waits; its own Detach or
			// decision settles the row. Forget a long-dead lease.
			if now.Sub(until) > 2*ModLeaseTTL {
				delete(s.leases, id)
			}
			continue
		}
		delete(s.leases, id)
		err := s.resolveLocked(id, StatusResolvedElsewhere, "terminal", "", nil)
		if err != nil && !errors.Is(err, ErrAlreadyResolved) && !errors.Is(err, ErrNotFound) {
			log.Printf("warn: approvals: lapsed mod lease on request %d: %v", id, err)
		}
	}
	for id, md := range s.modDecided {
		if now.Sub(md.at) > 2*ModLeaseTTL {
			delete(s.modDecided, id)
		}
	}
}
