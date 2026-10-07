// Package approvals implements the phase-2 remote-approval channel
// (docs/hooks-protocol.md, FROZEN at gate 2.2): the permission_requests
// store layer with the D6 dedup rule, the in-memory long-poll waiter
// registry, the expiry sweeper, the boot-time heal of requests orphaned by
// a daemon restart (amendment 3), and the hooks heartbeat surfaced by
// GET /api/health.
//
// Concurrency model: all state transitions (Open / Resolve / Detach /
// sweep) run under one mutex — they are short DB round-trips (the store
// pins MaxOpenConns(1) anyway), so the lock also makes the dedup check
// race-free: two concurrent identical requests cannot both miss the
// pending row. Long-poll waiting happens OUTSIDE the lock on a buffered
// decision channel.
package approvals

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/notify"
)

// Request statuses (permission_requests.status, frozen contract).
const (
	StatusPending           = "pending"
	StatusApproved          = "approved"
	StatusDenied            = "denied"
	StatusExpired           = "expired"
	StatusResolvedElsewhere = "resolved_elsewhere"
)

// Sentinel errors mapped to HTTP statuses by the API layer.
var (
	ErrNotFound        = errors.New("permission request not found")
	ErrAlreadyResolved = errors.New("permission request already resolved")
	ErrTooManyPending  = errors.New("too many pending requests for session")
	ErrBadRequest      = errors.New("malformed hook payload")
	// ErrInvalidAnswer: an {action:"answer"} body failed the AskUserQuestion
	// validation matrix (wrong tool, unparseable questions, missing/unknown/
	// malformed answers). Mapped to HTTP 400 with the specific reason.
	ErrInvalidAnswer = errors.New("invalid answer")
	// ErrExcludedProject: the hook cwd matches the exclude list and no session
	// row exists yet. The request is still SERVED — the API answers 204 (no
	// decision) and the shim fails open to the native dialog — but no
	// session/project rows are persisted for the excluded path.
	ErrExcludedProject = errors.New("project excluded from tracking")
	// ErrNoWaiter: the row is still pending but no long-poll waiter is attached
	// in THIS process, so a decision would be recorded and reach no one — the
	// hook that asked died with a previous daemon (HealStale expires those at
	// boot; a row minted by another daemon sharing the DB stays pending here
	// for good). Mapped to HTTP 410: the request is gone from this daemon's
	// point of view, answer it in the terminal.
	ErrNoWaiter = errors.New("permission request has no live hook waiter")
)

// ViaRestart is the resolved_via of a request HealStale expired at boot: its
// hook's long-poll died with the previous daemon process. RestartReason is
// the reason stamped on such rows (and appended to the approval_expired
// webhook body) so History and a notification receiver can tell a restart
// orphan from a genuine timeout.
const (
	ViaRestart    = "restart"
	RestartReason = "orphaned by daemon restart — the hook's long-poll died with the previous process; answer it in the terminal"
)

// maxPendingPerSession caps runaway hook storms: beyond it Open fails fast
// (HTTP 429 → the shim fails open to the native dialog).
const maxPendingPerSession = 20

// DefaultTimeout is the approval window: how long a pending request stays
// answerable from the dashboard before the shim's long-poll gives up and the
// row falls back to the native terminal prompt (resolved_elsewhere). MUST stay
// in sync with hookshim.DefaultPollTimeout — the daemon holds the connection
// this long, the shim waits exactly this long. Override both via
// SWARMERY_APPROVAL_TIMEOUT.
const DefaultTimeout = 10 * time.Minute

// DefaultSweepInterval is the expiry sweeper cadence.
const DefaultSweepInterval = 5 * time.Second

// AskUserQuestionTool is the only tool_name resolvable via {action:"answer"}.
const AskUserQuestionTool = "AskUserQuestion"

// Answer delivery modes (serve --answer-delivery): the wire form a dashboard
// answer takes on the long-poll 200. updated-input is the spike-verified
// default (E12a/b/c); deny-message is the fallback for runtimes that ignore
// updatedInput — it flips ONLY the wire form, the row stays approved.
const (
	DeliveryUpdatedInput = "updated-input"
	DeliveryDenyMessage  = "deny-message"
)

// tsFormat matches the millisecond-Z style of the ws-protocol examples.
const tsFormat = "2006-01-02T15:04:05.000Z"

// Decision is what wakes a long-poll waiter: the terminal status of the row
// plus the human-entered reason (delivered to Claude verbatim on deny).
// UpdatedInput is set only by Answer (AskUserQuestion dashboard answers,
// spike E12): the {questions, answers} object the shim forwards verbatim as
// hookSpecificOutput.decision.updatedInput.
type Decision struct {
	Status       string // approved | denied | expired | resolved_elsewhere
	Reason       string
	UpdatedInput json.RawMessage
}

// Options tunes a Service; zero values fall back to defaults.
type Options struct {
	Timeout        time.Duration      // approval_timeout (default 120s)
	SweepInterval  time.Duration      // expiry sweeper cadence (default 5s)
	Thresholds     ingest.Thresholds  // session status heuristic windows
	Exclude        ingest.ExcludeList // cwd globs that never mint session/project rows
	AnswerDelivery string             // DeliveryUpdatedInput (default) | DeliveryDenyMessage
	Now            func() time.Time   // test seam (default time.Now)

	// Notifier is the outbound webhook dispatcher (nil = disabled). Emit is
	// nil-receiver-safe and never blocks (buffered queue, drop on overflow).
	Notifier *notify.Notifier

	// ProdGuard classifies production-deploy tool calls (prodguard.go). nil
	// disables classification — only tests leave it unset; serve always
	// builds one.
	ProdGuard *ProdGuard
}

// Service owns the approvals lifecycle. Bus may be nil (no live WS updates,
// e.g. serve --no-ingest); every publish is nil-guarded.
type Service struct {
	db  *sql.DB
	bus *ingest.Bus
	opt Options

	mu      sync.Mutex
	waiters map[int64]map[chan Decision]struct{} // request id → attached long-poll waiters

	hbMu     sync.Mutex
	lastSeen time.Time // most recent POST /api/hooks/* (in-memory heartbeat;
	// deliberately not persisted — "hooks alive" is a property of the running
	// daemon, and a stale value surviving a restart would be a lie)
}

// New builds a Service.
func New(db *sql.DB, bus *ingest.Bus, opt Options) *Service {
	if opt.Timeout <= 0 {
		opt.Timeout = DefaultTimeout
	}
	if opt.SweepInterval <= 0 {
		opt.SweepInterval = DefaultSweepInterval
	}
	if opt.AnswerDelivery == "" {
		opt.AnswerDelivery = DeliveryUpdatedInput
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Service{db: db, bus: bus, opt: opt, waiters: map[int64]map[chan Decision]struct{}{}}
}

// Timeout returns the configured approval_timeout.
func (s *Service) Timeout() time.Duration { return s.opt.Timeout }

// AnswerDelivery returns the configured dashboard-answer wire form.
func (s *Service) AnswerDelivery() string { return s.opt.AnswerDelivery }

// Heartbeat records a hook check-in (both /api/hooks/* endpoints call it).
func (s *Service) Heartbeat() {
	s.hbMu.Lock()
	s.lastSeen = s.opt.Now()
	s.hbMu.Unlock()
}

// LastSeen returns the most recent hook check-in, if any.
func (s *Service) LastSeen() (time.Time, bool) {
	s.hbMu.Lock()
	defer s.hbMu.Unlock()
	return s.lastSeen, !s.lastSeen.IsZero()
}

func (s *Service) publish(n ingest.Notification) {
	if s.bus != nil {
		s.bus.Publish(n)
	}
}

// ── hook stdin parsing ───────────────────────────────────────────────────────

// HookInput is the subset of the PermissionRequest hook stdin the daemon
// acts on (E1 fixture); Raw keeps the verbatim body for request_json.
type HookInput struct {
	Raw         []byte
	SessionUUID string
	ToolName    string
	ToolInput   json.RawMessage
	CWD         string
}

// ParseHookStdin validates the verbatim hook stdin (E1 shape).
func ParseHookStdin(raw []byte) (HookInput, error) {
	var p struct {
		SessionID string          `json:"session_id"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
		CWD       string          `json:"cwd"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return HookInput{}, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	if p.SessionID == "" || p.ToolName == "" {
		return HookInput{}, fmt.Errorf("%w: session_id and tool_name are required", ErrBadRequest)
	}
	return HookInput{
		Raw:         raw,
		SessionUUID: p.SessionID,
		ToolName:    p.ToolName,
		ToolInput:   p.ToolInput,
		CWD:         p.CWD,
	}, nil
}

// DedupHash implements the frozen D6 rule:
//
//	hex(SHA-256(session_id + "\n" + tool_name + "\n" + canonical_json(tool_input)))
func DedupHash(sessionUUID, toolName string, toolInput json.RawMessage) (string, error) {
	canon, err := CanonicalJSON(toolInput)
	if err != nil {
		return "", fmt.Errorf("canonicalize tool_input: %w", err)
	}
	sum := sha256.Sum256([]byte(sessionUUID + "\n" + toolName + "\n" + canon))
	return hex.EncodeToString(sum[:]), nil
}

// CanonicalJSON re-serializes a JSON value with object keys sorted
// lexicographically (byte order) at every nesting level and no insignificant
// whitespace; arrays keep their order. Go's encoding/json already marshals
// map keys in sorted byte order, so a decode/encode round-trip through
// interface{} is exactly the frozen canonical form. Empty input canonicalizes
// as JSON null.
func CanonicalJSON(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ── open (hook → pending row / dedup attach) ─────────────────────────────────

// Open registers one incoming PermissionRequest hook call and returns the
// request id plus the decision channel to long-poll on.
//
// New dedup_hash → new pending row + permission_request event + session →
// waiting_approval + WS (session_updated, event_appended,
// permission_requested). A hash matching an existing PENDING row attaches to
// it instead: no new row, no events, no WS — the eventual decision fans out
// to every attached waiter (D6, spike E11).
//
// The caller MUST hand the channel back via Detach (client disconnect) or
// receive from it (decision/expiry); Resolve drops all attached waiters.
func (s *Service) Open(in HookInput) (id int64, ch chan Decision, isNew bool, err error) {
	hash, err := DedupHash(in.SessionUUID, in.ToolName, in.ToolInput)
	if err != nil {
		return 0, nil, false, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Dedup: only ever against pending rows (partial index idx_pr_dedup).
	err = s.db.QueryRow(
		`SELECT id FROM permission_requests WHERE dedup_hash = ? AND status = 'pending'`,
		hash).Scan(&id)
	switch {
	case err == nil:
		return id, s.attachLocked(id), false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, nil, false, err
	}

	sessionID, err := s.resolveSessionLocked(in)
	if err != nil {
		return 0, nil, false, err
	}

	var pending int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM permission_requests WHERE session_id = ? AND status = 'pending'`,
		sessionID).Scan(&pending); err != nil {
		return 0, nil, false, err
	}
	if pending >= maxPendingPerSession {
		return 0, nil, false, ErrTooManyPending
	}

	riskClass := s.riskClassLocked(sessionID, in)

	now := s.opt.Now().UTC()
	requestedAt := now.Format(tsFormat)
	expiresAt := now.Add(s.opt.Timeout).Format(tsFormat)
	res, err := s.db.Exec(
		`INSERT INTO permission_requests
		   (session_id, tool_name, request_json, status, requested_at, dedup_hash, expires_at, risk_class)
		 VALUES (?, ?, ?, 'pending', ?, ?, ?, ?)`,
		sessionID, in.ToolName, string(in.Raw), requestedAt, hash, expiresAt, riskClass)
	if err != nil {
		return 0, nil, false, fmt.Errorf("insert permission_request: %w", err)
	}
	id, _ = res.LastInsertId()

	eventID, err := s.insertEventLocked(sessionID, "permission_request", in.ToolName, "", requestedAt, string(in.Raw))
	if err != nil {
		return 0, nil, false, err
	}
	if _, err := s.db.Exec(
		`UPDATE permission_requests SET event_id = ? WHERE id = ?`, eventID, id); err != nil {
		return 0, nil, false, err
	}

	if _, err := s.db.Exec(
		`UPDATE sessions SET status = 'waiting_approval' WHERE id = ?`, sessionID); err != nil {
		return 0, nil, false, err
	}

	ch = s.attachLocked(id)
	s.publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: sessionID})
	s.publish(ingest.Notification{Type: ingest.NoteEventAppended, SessionID: sessionID, EventID: eventID})
	s.publish(ingest.Notification{Type: ingest.NotePermissionRequested, SessionID: sessionID, RequestID: id})

	// Production deploy: hand it straight back to the session's own terminal.
	// resolved_elsewhere wakes the waiter just attached → the long-poll answers
	// 204 → the shim prints nothing → Claude Code shows its native dialog
	// (internal/hookshim/shim.go). Rule evaluation is never reached, and
	// resolveLocked refuses any later approval of the row (ErrLocalOnly). On a
	// failed hand-off the error goes back to the hook (5xx → the shim fails
	// open to the same native dialog) — never on to the rules below.
	if riskClass == RiskProdDeploy {
		if err := s.resolveLocked(id, StatusResolvedElsewhere, ViaLocalOnly, LocalOnlyReason, nil); err != nil {
			delete(s.waiters, id) // nobody will receive on ch; the sweeper expires the row
			return 0, nil, false, fmt.Errorf("hand prod-deploy request %d to the terminal: %w", id, err)
		}
		s.notifyApproval(notify.EventApprovalRequested, id, sessionID, in.ToolName, in.ToolInput, LocalOnlyReason)
		return id, ch, true, nil
	}

	// control-plane v2 — auto-approve rules: a matching enabled rule resolves
	// the row immediately (resolved_via='rule'). The pending row above is
	// KEPT as the audit trail; resolveLocked inserts permission_resolved,
	// restores the session status, publishes the WS frame, and fans the
	// decision into the waiter channel just attached — the long-poll caller
	// answers {"decision":"allow"} without any human in the loop. The
	// approval_requested webhook is intentionally skipped for auto-approvals.
	if ruleID := s.matchRuleLocked(sessionID, in); ruleID != 0 {
		reason := fmt.Sprintf("auto-approved by rule #%d", ruleID)
		if err := s.resolveLocked(id, StatusApproved, "rule", reason, nil); err != nil {
			log.Printf("warn: approvals: rule %d auto-approve of request %d: %v", ruleID, id, err)
		} else {
			return id, ch, true, nil
		}
	}

	s.notifyApproval(notify.EventApprovalRequested, id, sessionID, in.ToolName, in.ToolInput, "")
	return id, ch, true, nil
}

// riskClassLocked classifies one hook call: RiskProdDeploy when the guard
// matches it for the session's project (projects.path), else the empty
// class. A failed project lookup still runs the defaults and daemon extras.
func (s *Service) riskClassLocked(sessionID int64, in HookInput) string {
	if s.opt.ProdGuard == nil {
		return ""
	}
	var projectDir string
	if err := s.db.QueryRow(
		`SELECT p.path FROM sessions se JOIN projects p ON p.id = se.project_id WHERE se.id = ?`,
		sessionID).Scan(&projectDir); err != nil {
		log.Printf("warn: approvals: project dir for session %d: %v (prod-deploy guard uses defaults only)", sessionID, err)
		projectDir = ""
	}
	if s.opt.ProdGuard.Match(projectDir, in.ToolName, in.ToolInput) {
		return RiskProdDeploy
	}
	return ""
}

// attachLocked registers a buffered waiter channel for a request id.
func (s *Service) attachLocked(id int64) chan Decision {
	ch := make(chan Decision, 1)
	if s.waiters[id] == nil {
		s.waiters[id] = map[chan Decision]struct{}{}
	}
	s.waiters[id][ch] = struct{}{}
	return ch
}

// resolveSessionLocked maps the hook's session_id to a sessions row,
// creating the project (keyed by cwd, same derivation as ingest) and a
// source='hook' session when the transcript has not been ingested yet.
// Parallel subagents share the parent's session uuid (E11), so this is a
// plain uuid lookup.
func (s *Service) resolveSessionLocked(in HookInput) (int64, error) {
	var sessionID int64
	err := s.db.QueryRow(
		`SELECT id FROM sessions WHERE session_uuid = ?`, in.SessionUUID).Scan(&sessionID)
	if err == nil {
		return sessionID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	// Exclusion (row creation only — an already-tracked session proceeds
	// normally above): excluded cwds are still served, but persist nothing.
	if in.CWD != "" && s.opt.Exclude.MatchPath(in.CWD) {
		return 0, ErrExcludedProject
	}

	// The hook stdin carries the real cwd (docs/hooks-format.md E1) — attribute
	// the stub to the real project via the SAME derivation as the JSONL ingest
	// (path → slug '/'→'-', name = path base). '(unknown)' remains only for a
	// genuinely absent cwd; the ingest upsert / HealStubSessions re-attribute
	// such stubs once the transcript reveals the cwd.
	cwd := in.CWD
	if cwd == "" {
		cwd = ingest.UnknownProjectPath
	}
	now := s.opt.Now().UTC().Format(tsFormat)
	projectID, _, err := ingest.UpsertProject(s.db, cwd, now, now)
	if err != nil {
		return 0, err
	}

	res, err := s.db.Exec(
		`INSERT INTO sessions (project_id, session_uuid, cwd, status, started_at, source)
		 VALUES (?, ?, ?, 'waiting_approval', ?, 'hook')`,
		projectID, in.SessionUUID, cwd, now)
	if err != nil {
		return 0, fmt.Errorf("insert hook session: %w", err)
	}
	sessionID, _ = res.LastInsertId()
	// The SessionStart hook parked this session's identity (verified pid,
	// terminal) because no row existed yet. A session that never writes a
	// transcript (e.g. cancelled at the native prod-deploy dialog) is only
	// ever minted here, so apply it now — without the pid, procwatch never
	// sees the process die. Best effort: the stub row is already useful.
	if err := ingest.ApplyPendingTerminal(s.db, in.SessionUUID, sessionID); err != nil {
		log.Printf("warn: approvals: session %d: %v", sessionID, err)
	}
	s.publish(ingest.Notification{Type: ingest.NoteSessionStarted, SessionID: sessionID})
	return sessionID, nil
}

// ── resolve (dashboard / expiry / disconnect → terminal status) ──────────────

// Resolve moves a pending request to a terminal status: updates the row,
// inserts the permission_resolved event, restores the session status via the
// heuristic when no other pending requests remain, publishes WS
// (permission_resolved, event_appended, session_updated), and fans the
// decision out to every attached long-poll waiter.
//
// status must be one of approved|denied|expired|resolved_elsewhere.
// Returns ErrNotFound / ErrAlreadyResolved (mapped to 404/409 upstream).
func (s *Service) Resolve(id int64, status, via, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resolveLocked(id, status, via, reason, nil)
}

// Decide is Resolve for a decision that must REACH a hook — the dashboard's
// approve / deny / terminal handoff. It refuses with ErrNoWaiter when the row
// is pending but no long-poll waiter is attached in this process: recording
// such a decision would answer 200 while delivering nothing (the shim that
// asked died with a previous daemon, and Claude Code is already in its native
// dialog). The row is left untouched — the sweeper, or the daemon that does
// own the waiter, still resolves it. Error precedence matches Resolve:
// ErrNotFound for an unknown id, ErrAlreadyResolved for a terminal row,
// ErrNoWaiter only for a pending row nobody is polling on.
func (s *Service) Decide(id int64, status, via, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWaiterLocked(id); err != nil {
		return err
	}
	return s.resolveLocked(id, status, via, reason, nil)
}

// requireWaiterLocked reports nil when at least one long-poll waiter is
// attached to id; otherwise it reads the row to return the most specific
// error (unknown → ErrNotFound, terminal → ErrAlreadyResolved, pending →
// ErrNoWaiter).
func (s *Service) requireWaiterLocked(id int64) error {
	if len(s.waiters[id]) > 0 {
		return nil
	}
	var cur string
	err := s.db.QueryRow(`SELECT status FROM permission_requests WHERE id = ?`, id).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur != StatusPending {
		return ErrAlreadyResolved
	}
	return ErrNoWaiter
}

func (s *Service) resolveLocked(id int64, status, via, reason string, updatedInput json.RawMessage) error {
	var sessionID int64
	var cur, toolName, riskClass string
	err := s.db.QueryRow(
		`SELECT session_id, status, tool_name, risk_class FROM permission_requests WHERE id = ?`, id).
		Scan(&sessionID, &cur, &toolName, &riskClass)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Defense in depth: no path — dashboard Decide, Answer, a rule, any future
	// caller — may approve a production deploy remotely.
	if riskClass == RiskProdDeploy && status == StatusApproved {
		return ErrLocalOnly
	}
	if cur != StatusPending {
		return ErrAlreadyResolved
	}

	now := s.opt.Now().UTC().Format(tsFormat)
	if _, err := s.db.Exec(
		`UPDATE permission_requests
		 SET status = ?, resolved_at = ?, resolved_via = ?, reason = ?
		 WHERE id = ? AND status = 'pending'`,
		status, now, nullStr(via), nullStr(reason), id); err != nil {
		return err
	}

	payload, _ := json.Marshal(map[string]any{"requestId": id, "decision": status, "via": via})
	eventID, err := s.insertEventLocked(sessionID, "permission_resolved", "", eventStatusFor(status), now, string(payload))
	if err != nil {
		return err
	}

	// Restore the session status via the normal heuristic — but only when no
	// other request is still pending for this session (parallel subagents).
	var remaining int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM permission_requests WHERE session_id = ? AND status = 'pending'`,
		sessionID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if err := s.recomputeSessionLocked(sessionID); err != nil {
			return err
		}
	}

	s.publish(ingest.Notification{Type: ingest.NoteEventAppended, SessionID: sessionID, EventID: eventID})
	s.publish(ingest.Notification{Type: ingest.NotePermissionResolved, SessionID: sessionID, RequestID: id})

	if status == StatusExpired {
		s.notifyApproval(notify.EventApprovalExpired, id, sessionID, toolName, nil, reason)
	}

	d := Decision{Status: status, Reason: reason, UpdatedInput: updatedInput}
	for ch := range s.waiters[id] {
		ch <- d // each channel is buffered(1) and receives exactly one decision
	}
	delete(s.waiters, id)
	return nil
}

// recomputeSessionLocked re-derives the heuristic status (active|idle|
// completed) for a session leaving waiting_approval and emits
// session_updated. Sessions not in waiting_approval are left alone (the
// ticker owns their transitions).
func (s *Service) recomputeSessionLocked(sessionID int64) error {
	var cur, lastTS string
	err := s.db.QueryRow(
		`SELECT status, COALESCE(ended_at, started_at) FROM sessions WHERE id = ?`,
		sessionID).Scan(&cur, &lastTS)
	if err != nil {
		return err
	}
	if cur != "waiting_approval" {
		return nil
	}
	last, perr := time.Parse(time.RFC3339Nano, lastTS)
	status := "active"
	if perr == nil {
		status = ingest.StatusFor(last, s.opt.Now(), s.opt.Thresholds)
	}
	if _, err := s.db.Exec(
		`UPDATE sessions SET status = ? WHERE id = ?`, status, sessionID); err != nil {
		return err
	}
	s.publish(ingest.Notification{Type: ingest.NoteSessionUpdated, SessionID: sessionID})
	return nil
}

// Detach removes one waiter (long-poll client gone). When the LAST waiter of
// a still-pending request disconnects — e.g. the terminal Esc/Ctrl-C killed
// the shim mid-poll — the row is resolved as resolved_elsewhere
// (via 'terminal', hooks-protocol §side effects / spike E4-interrupt).
func (s *Service) Detach(id int64, ch chan Decision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set, ok := s.waiters[id]
	if !ok {
		return // already resolved — waiters dropped by resolveLocked
	}
	delete(set, ch)
	if len(set) > 0 {
		return
	}
	delete(s.waiters, id)
	if err := s.resolveLocked(id, StatusResolvedElsewhere, "terminal", "", nil); err != nil &&
		!errors.Is(err, ErrAlreadyResolved) && !errors.Is(err, ErrNotFound) {
		log.Printf("warn: approvals: resolve_elsewhere request %d: %v", id, err)
	}
}

// Expire resolves a request as expired if it is still pending (idempotent:
// ErrAlreadyResolved is swallowed). Used by the sweeper and by the long-poll
// handler's belt-and-braces deadline.
func (s *Service) Expire(id int64) error {
	err := s.Resolve(id, StatusExpired, "", "")
	if errors.Is(err, ErrAlreadyResolved) || errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// ── answer (dashboard → AskUserQuestion answers, spike E12) ──────────────────

// answerQuestion is the subset of the upstream AskUserQuestion tool_input
// schema the validation matrix needs (docs/hooks-format.md E12).
type answerQuestion struct {
	Question    string `json:"question"`
	MultiSelect bool   `json:"multiSelect"`
}

// Answer resolves a pending AskUserQuestion request with the operator's
// answers. Validation (each failure wraps ErrInvalidAnswer → HTTP 400):
//
//   - the row's tool_name must be AskUserQuestion;
//   - the stored request_json's tool_input.questions must parse;
//   - every question must be answered; no unknown question keys;
//   - an array value is only legal for a multiSelect:true question;
//   - any non-empty string is legal (options are suggestions — free text is
//     first-class, same as the native dialog).
//
// On success the row resolves as approved (resolved_via 'dashboard') with
// reason = the human summary «Q» → A · «Q2» → B, C, and the Decision fans
// out to all dedup waiters carrying UpdatedInput {questions, answers} built
// from the stored request_json (questions echoed verbatim, E12a; multiSelect
// answers as arrays of labels, E12c).
func (s *Service) Answer(id int64, answers map[string]json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var cur, toolName, requestJSON string
	err := s.db.QueryRow(
		`SELECT status, tool_name, request_json FROM permission_requests WHERE id = ?`, id).
		Scan(&cur, &toolName, &requestJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur != StatusPending {
		return ErrAlreadyResolved
	}
	// Same guard as Decide: answers are delivered through the long-poll, so a
	// pending row nobody is polling on cannot take one (ErrNoWaiter → 410).
	if len(s.waiters[id]) == 0 {
		return ErrNoWaiter
	}
	if toolName != AskUserQuestionTool {
		return fmt.Errorf("%w: action 'answer' requires tool %s, request %d is %s",
			ErrInvalidAnswer, AskUserQuestionTool, id, toolName)
	}

	rawQuestions, questions, err := parseQuestions(requestJSON)
	if err != nil {
		return err
	}
	byText := make(map[string]answerQuestion, len(questions))
	for _, q := range questions {
		byText[q.Question] = q
	}
	for key := range answers {
		if _, ok := byText[key]; !ok {
			return fmt.Errorf("%w: unknown question %q", ErrInvalidAnswer, key)
		}
	}
	values := make(map[string][]string, len(questions))
	for _, q := range questions {
		raw, ok := answers[q.Question]
		if !ok {
			return fmt.Errorf("%w: question %q has no answer", ErrInvalidAnswer, q.Question)
		}
		vals, err := answerValues(q, raw)
		if err != nil {
			return err
		}
		values[q.Question] = vals
	}

	// updatedInput = {questions verbatim, answers as validated} — built
	// server-side so the shim (and the audit trail) never trust
	// dashboard-echoed questions.
	updatedInput, err := json.Marshal(struct {
		Questions json.RawMessage            `json:"questions"`
		Answers   map[string]json.RawMessage `json:"answers"`
	}{Questions: rawQuestions, Answers: answers})
	if err != nil {
		return fmt.Errorf("marshal updatedInput: %w", err)
	}
	return s.resolveLocked(id, StatusApproved, "dashboard", answerSummary(questions, values), updatedInput)
}

// parseQuestions extracts tool_input.questions from a stored request_json
// (the verbatim hook stdin): the raw value (echoed verbatim into
// updatedInput) plus the parsed validation subset.
func parseQuestions(requestJSON string) (json.RawMessage, []answerQuestion, error) {
	var p struct {
		ToolInput struct {
			Questions json.RawMessage `json:"questions"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal([]byte(requestJSON), &p); err != nil {
		return nil, nil, fmt.Errorf("%w: request_json does not parse: %v", ErrInvalidAnswer, err)
	}
	if len(p.ToolInput.Questions) == 0 {
		return nil, nil, fmt.Errorf("%w: tool_input.questions missing", ErrInvalidAnswer)
	}
	var questions []answerQuestion
	if err := json.Unmarshal(p.ToolInput.Questions, &questions); err != nil {
		return nil, nil, fmt.Errorf("%w: tool_input.questions does not parse: %v", ErrInvalidAnswer, err)
	}
	if len(questions) == 0 {
		return nil, nil, fmt.Errorf("%w: tool_input.questions is empty", ErrInvalidAnswer)
	}
	for _, q := range questions {
		if q.Question == "" {
			return nil, nil, fmt.Errorf("%w: a question without text", ErrInvalidAnswer)
		}
	}
	return p.ToolInput.Questions, questions, nil
}

// answerValues validates one answer value: a non-empty string for any
// question (label or free text), a non-empty array of non-empty strings for
// multiSelect only (E12c: arrays of labels are the emitted form).
func answerValues(q answerQuestion, raw json.RawMessage) ([]string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("%w: empty answer for question %q", ErrInvalidAnswer, q.Question)
		}
		return []string{s}, nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("%w: answer for question %q must be a string or an array of strings",
			ErrInvalidAnswer, q.Question)
	}
	if !q.MultiSelect {
		return nil, fmt.Errorf("%w: array answer for single-select question %q", ErrInvalidAnswer, q.Question)
	}
	if len(arr) == 0 {
		return nil, fmt.Errorf("%w: empty answer for question %q", ErrInvalidAnswer, q.Question)
	}
	for _, v := range arr {
		if strings.TrimSpace(v) == "" {
			return nil, fmt.Errorf("%w: empty option in answer for question %q", ErrInvalidAnswer, q.Question)
		}
	}
	return arr, nil
}

// answerSummary renders the History-facing reason: «Q» → A · «Q2» → B, C
// (questions in tool_input order, multiSelect values comma-joined).
func answerSummary(questions []answerQuestion, values map[string][]string) string {
	parts := make([]string, 0, len(questions))
	for _, q := range questions {
		parts = append(parts, fmt.Sprintf("«%s» → %s", q.Question, strings.Join(values[q.Question], ", ")))
	}
	return strings.Join(parts, " · ")
}

// ── expiry sweeper ───────────────────────────────────────────────────────────

// RunSweeper ticks until ctx is done, expiring overdue pending rows
// (fail-open semantics: their waiters wake with 'expired' and the shim's
// long-poll answers 204 → Claude falls back to the native dialog). It also
// self-heals sessions stuck in waiting_approval with no pending rows left
// (e.g. daemon crash between resolve and recompute).
func (s *Service) RunSweeper(ctx context.Context) {
	t := time.NewTicker(s.opt.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep()
		}
	}
}

// Sweep runs one sweeper pass (exported for tests and manual ticks).
func (s *Service) Sweep() {
	now := s.opt.Now().UTC().Format(tsFormat)

	rows, err := s.db.Query(
		`SELECT id FROM permission_requests
		 WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= ?`, now)
	if err != nil {
		log.Printf("warn: approvals: sweep query: %v", err)
		return
	}
	var overdue []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			log.Printf("warn: approvals: sweep scan: %v", err)
			return
		}
		overdue = append(overdue, id)
	}
	rows.Close()
	for _, id := range overdue {
		if err := s.Expire(id); err != nil {
			log.Printf("warn: approvals: expire request %d: %v", id, err)
		}
	}

	// Self-heal: waiting_approval must never be sticky.
	stuck, err := s.db.Query(
		`SELECT id FROM sessions s WHERE s.status = 'waiting_approval'
		 AND NOT EXISTS (SELECT 1 FROM permission_requests pr
		                 WHERE pr.session_id = s.id AND pr.status = 'pending')`)
	if err != nil {
		log.Printf("warn: approvals: sweep stuck-session query: %v", err)
		return
	}
	var ids []int64
	for stuck.Next() {
		var id int64
		if err := stuck.Scan(&id); err != nil {
			stuck.Close()
			return
		}
		ids = append(ids, id)
	}
	stuck.Close()
	if len(ids) > 0 {
		s.mu.Lock()
		for _, id := range ids {
			if err := s.recomputeSessionLocked(id); err != nil {
				log.Printf("warn: approvals: heal session %d: %v", id, err)
			}
		}
		s.mu.Unlock()
	}
}

// ── boot heal (amendment 3) ──────────────────────────────────────────────────

// HealStale expires every request still pending from BEFORE this process
// started. Called once at daemon boot, before the HTTP listener is up, next
// to the other services' HealStale sweeps. A pending row's long-poll waiter
// lived in the previous daemon process and died with it: the shim's
// connection broke, Claude Code fell back to its native dialog (D3), and
// nothing can deliver a decision to the row any more. Left alone it would sit
// in every pending list (dashboard, notch, `swarmery console`) until
// expires_at, and a decision POSTed on it would "succeed" into the void.
//
// Each orphan is resolved exactly like a sweeper expiry — status expired, the
// permission_resolved event, the session status recomputed, WS
// permission_resolved, the approval_expired webhook — with resolved_via =
// ViaRestart and reason = RestartReason so History tells the two apart. Only
// rows with requested_at < bootAt are touched: a row minted after this
// process started belongs to a live waiter (ours, or another daemon sharing
// the DB — never ours to expire). Returns the number of rows expired.
func (s *Service) HealStale(bootAt time.Time) (int, error) {
	cutoff := bootAt.UTC().Format(tsFormat)
	rows, err := s.db.Query(
		`SELECT id FROM permission_requests
		 WHERE status = 'pending' AND requested_at < ?
		 ORDER BY id`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("approvals heal query: %w", err)
	}
	var orphans []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("approvals heal scan: %w", err)
		}
		orphans = append(orphans, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("approvals heal rows: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	healed := 0
	for _, id := range orphans {
		err := s.resolveLocked(id, StatusExpired, ViaRestart, RestartReason, nil)
		switch {
		case errors.Is(err, ErrAlreadyResolved), errors.Is(err, ErrNotFound):
			continue // raced to a terminal state between the query and the lock
		case err != nil:
			return healed, fmt.Errorf("approvals heal request %d: %w", id, err)
		}
		healed++
	}
	if healed > 0 {
		log.Printf("swarmery approvals: expired %d pending request(s) orphaned by the restart", healed)
	}
	return healed, nil
}

// ── outbound webhooks (control-plane v2) ─────────────────────────────────────

// notifyApproval emits one webhook event for a permission request. Never
// blocks and never fails the caller: Notifier.Emit is nil-safe and
// queue-dropping; the project lookup failing just blanks the label. A
// non-empty reason is appended to the body (the restart heal uses it to say
// why an approval expired early).
func (s *Service) notifyApproval(typ string, requestID, sessionID int64, toolName string, toolInput json.RawMessage, reason string) {
	if s.opt.Notifier == nil {
		return
	}
	var project string
	if err := s.db.QueryRow(
		`SELECT COALESCE(p.name, p.slug) FROM sessions se
		 JOIN projects p ON p.id = se.project_id WHERE se.id = ?`,
		sessionID).Scan(&project); err != nil {
		project = ""
	}
	body := toolName
	if arg, ok := argOf(toolName, toolInput); ok {
		body = toolName + ": " + truncate(arg, 160)
	}
	if project != "" {
		body = project + " — " + body
	}
	if reason != "" {
		body += " · " + reason
	}
	title := "Approval needed: " + toolName
	if typ == notify.EventApprovalExpired {
		title = "Approval expired: " + toolName
	}
	s.opt.Notifier.Emit(notify.Event{
		Type: typ, SessionID: sessionID, RequestID: requestID,
		Project: project, Tool: toolName, Title: title, Body: body,
	})
}

// ── helpers ──────────────────────────────────────────────────────────────────

// insertEventLocked writes one approvals event row. dedup_key is minted
// ('hook:'+random) — hook events have no transcript uuid to dedup on.
func (s *Service) insertEventLocked(sessionID int64, typ, toolName, status, ts, payload string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sessionID, ts, typ, nullStr(toolName), nullStr(status), payload, "hook:"+randomToken())
	if err != nil {
		return 0, fmt.Errorf("insert %s event: %w", typ, err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// eventStatusFor maps a terminal request status onto the events.status
// vocabulary (ok | denied | timeout; resolved_elsewhere carries none).
func eventStatusFor(status string) string {
	switch status {
	case StatusApproved:
		return "ok"
	case StatusDenied:
		return "denied"
	case StatusExpired:
		return "timeout"
	default:
		return ""
	}
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
