package api

// Alerts: the findings that need the operator NOW, and the one action the first
// of them offers.
//
//	GET  /api/alerts                              → every unresolved alert finding
//	POST /api/accounts/{account}/breaker/resume   → probe the account, close its breaker when ready
//
// config_lint_findings already holds every "something is wrong" row the daemon
// detects, and almost all of them are lint: advice the operator reads when they
// have a minute. An ALERT is the small subset that stops work until someone
// acts — a rule listed in AlertRules. The first is the account circuit breaker
// (internal/runcore): while it is open, no card, phase or plan is admitted onto
// that account. The second is an outage of Claude Code's auto mode permission
// check (internal/automode), which carries no action.
//
// Read-only over the findings table; the breaker row adds the facts a finding
// cannot carry (kind, fixed reason, reset time). No field here is CLI output.

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/automode"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// AlertRules are the config_lint_findings rules GET /api/alerts surfaces. A rule
// belongs here when its open finding means work is stopped right now; adding
// one is all it takes for it to reach the Inbox.
//
//   - store.AccountBreakerRule — an account is paused until a probe succeeds;
//     the one alert with an action.
//   - automode.Rule — Claude Code's server-side auto mode permission check is
//     not answering, so the sessions it hits pause. Nothing to press: the alert
//     resolves itself once the check has answered for a while.
var AlertRules = []string{store.AccountBreakerRule, automode.Rule}

// probeAccountRun is the two-stage probe behind "Probe & resume" (`claude auth
// status`, then the `claude -p` ping). A package var for the same reason as
// probeAccount: tests answer deterministically and never spawn the real CLI.
var probeAccountRun runcore.AccountProbeFunc = runcore.ProbeAccount

// alertDTO is one open alert. The first six fields are the finding itself; the
// rest are present only for an account-breaker alert.
type alertDTO struct {
	ID         int64  `json:"id"`
	Rule       string `json:"rule"`
	Target     string `json:"target"`
	Severity   string `json:"severity"`
	Message    string `json:"message"` // a fixed sentence per rule and kind
	DetectedAt string `json:"detectedAt"`

	// Account is the account key of a breaker alert — what the resume endpoint
	// takes.
	Account string `json:"account,omitempty"`
	// Kind is the breaker's kind: "auth" or "quota".
	Kind string `json:"kind,omitempty"`
	// Reason is the breaker's fixed reason phrase (internal/claudeprobe).
	Reason string `json:"reason,omitempty"`
	// OpenedAt is when the breaker opened (RFC 3339).
	OpenedAt string `json:"openedAt,omitempty"`
	// ResetsAt is when a quota breaker closes on its own (RFC 3339); absent for
	// an auth one, which closes only on a ready probe.
	ResetsAt string `json:"resetsAt,omitempty"`
}

// alertsResponse is the GET /api/alerts body.
type alertsResponse struct {
	Alerts []alertDTO `json:"alerts"`
}

// listAlerts handles GET /api/alerts: the unresolved findings whose rule is in
// AlertRules, oldest first.
func (h *Handler) listAlerts(w http.ResponseWriter, _ *http.Request) {
	if len(AlertRules) == 0 {
		writeJSON(w, alertsResponse{Alerts: []alertDTO{}}, nil)
		return
	}
	args := make([]any, len(AlertRules))
	for i, rule := range AlertRules {
		args[i] = rule
	}
	rows, err := h.DB.Query(`
		SELECT id, rule, target, severity, message, detected_at
		  FROM config_lint_findings
		 WHERE resolved_at IS NULL
		   AND rule IN (?`+strings.Repeat(`,?`, len(AlertRules)-1)+`)
		 ORDER BY detected_at, id`, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	alerts := []alertDTO{}
	for rows.Next() {
		var a alertDTO
		if err := rows.Scan(&a.ID, &a.Rule, &a.Target, &a.Severity, &a.Message, &a.DetectedAt); err != nil {
			rows.Close()
			writeErr(w, err)
			return
		}
		alerts = append(alerts, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	// Enriched AFTER the cursor is closed: the store runs one connection.
	for i := range alerts {
		h.enrichBreakerAlert(&alerts[i])
	}
	writeJSON(w, alertsResponse{Alerts: alerts}, nil)
}

// enrichBreakerAlert adds the breaker row's facts to an account-breaker alert.
// A breaker that cannot be read leaves the alert as the bare finding — still
// true, just less detailed.
func (h *Handler) enrichBreakerAlert(a *alertDTO) {
	if a.Rule != store.AccountBreakerRule {
		return
	}
	account, ok := store.AccountFromBreakerTarget(a.Target)
	if !ok {
		return
	}
	a.Account = account
	b, found, err := store.GetAccountBreaker(h.DB, account)
	if err != nil || !found {
		return
	}
	a.Kind, a.Reason, a.OpenedAt, a.ResetsAt = b.Kind, b.Reason, b.OpenedAt, b.ResetsAt
}

// resumeResponse is the 200 body of POST /api/accounts/{account}/breaker/resume.
type resumeResponse struct {
	OK      bool   `json:"ok"`
	Account string `json:"account"`
	// State is the breaker's state after the probe — always "closed" on a 200.
	State string `json:"state"`
}

// codeAccountNotReady is the resume refusal: the probe ran and the account
// still cannot run.
const codeAccountNotReady = "account-not-ready"

// resumeAccountBreaker handles POST /api/accounts/{account}/breaker/resume —
// the alert's "Probe & resume".
//
// It probes the account with BOTH stages and closes the breaker only on a ready
// answer: 200 {ok, account, state:"closed"}. Anything else is 409 with the
// probe's fixed reason phrase — the account is still paused, and the body says
// why: {error, code:"account-not-ready", account, status, reason}. 404 for an
// account that does not exist.
//
// Single-flighted per account, and the probe's context is not the request's: a
// client that gives up must not abort a probe a second click is waiting on.
func (h *Handler) resumeAccountBreaker(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("account"))
	acct, ok := findAccount(key)
	if !ok {
		writeClientErr(w, http.StatusNotFound, "unknown account")
		return
	}
	// No config dir is computed here, on purpose: runcore.ResumeBreaker probes
	// under the environment a run filed under this breaker key gets
	// (runcore.AccountEnv) — the same composition a real spawn uses. For the
	// default key that is the UNBOUND environment, which keeps a
	// CLAUDE_CONFIG_DIR the daemon inherited; the plain probe endpoint strips it,
	// and doing so here would check ~/.claude while the stopped runs use another
	// directory.
	//
	// The flight's value is what every caller reads — the leader and the clicks
	// that waited on it alike — so the answer travels in it, not in a captured
	// variable only the leader would have set.
	verdict, err := h.probes.do("resume:"+acct.Key, func() (store.AccountRunnable, error) {
		res, err := runcore.ResumeBreaker(context.Background(), h.DB, acct.Key, probeAccountRun, time.Now())
		return store.AccountRunnable{Status: string(res.Status), Reason: res.Reason}, err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("account resume: account=%s status=%s", acct.Key, verdict.Status)
	if claudeprobe.Status(verdict.Status) != claudeprobe.StatusReady {
		reason := verdict.Reason
		if reason == "" {
			reason = claudeprobe.ReasonUnrecognised
		}
		writeConflictFields(w, codeAccountNotReady, reason, map[string]any{
			"account": acct.Key,
			"status":  verdict.Status,
			"reason":  reason,
		})
		return
	}
	writeJSON(w, resumeResponse{OK: true, Account: acct.Key, State: store.BreakerClosed}, nil)
}

// closeAuthBreaker closes account's breaker when it is an open AUTH one — the
// step the probe and login-complete handlers take after a ready verdict. A
// quota opening is left alone: a working login says nothing about a usage
// limit, and it closes itself at its reset. Best-effort; a failure is logged.
func (h *Handler) closeAuthBreaker(account, closedBy string) {
	b, ok, err := store.GetAccountBreaker(h.DB, account)
	if err != nil || !ok || !b.IsOpen() || b.Kind != store.BreakerKindAuth {
		return
	}
	if _, err := runcore.CloseBreaker(h.DB, account, closedBy, time.Now()); err != nil {
		log.Printf("warning: account breaker: close account=%s by=%s: %v", account, closedBy, err)
	}
}
