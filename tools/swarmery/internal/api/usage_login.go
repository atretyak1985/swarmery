package api

// Connect-an-account: the dashboard half of internal/usage's rung-2 OAuth flow.
//
// An account with no readable credential file — the normal state of a
// non-default account on macOS, where the `claude` CLI keeps its credential in
// the login Keychain under an undocumented per-config-dir name we refuse to
// depend on — renders a "not connected" card forever under rung 1. These two
// endpoints let the operator authorize swarmery itself, once, from the browser:
//
//	POST   /api/usage/accounts/{account}/login/start     → {authorizeUrl}
//	POST   /api/usage/accounts/{account}/login/complete  → completeResponse
//	       body {"code": "<code>#<state>"}
//	DELETE /api/usage/accounts/{account}/login           → {ok:true}
//
// The split exists because the middle step happens in a browser we do not
// control: start mints the PKCE verifier and CSRF state and hands back only the
// URL, the operator authorizes and pastes back the "code#state" value the
// callback page shows, complete exchanges it and persists the credential.
//
// # Complete is a three-step, honestly-reported transaction
//
// After the exchange succeeds, complete also (1) hands the credential over to
// the account's config dir (usage.HandoffToConfigDir — write-once; skipped for
// the default account, and an already-present file is the CLI's, not a
// failure) and (2) runs the authoritative readiness probe, persisting the
// verdict with source='probe'. The response reports all three outcomes so the
// UI never has to guess. FAILURE DISCIPLINE: a failed handoff or an unready
// probe does NOT fail the request and does NOT roll back the stored credential
// — the quota connection genuinely succeeded; the response says
// nextStep:"pty-login" and the UI offers the interactive login instead. Only a
// failed CompleteLogin is a 4xx/5xx.
//
// The DELETE is that credential's removal — swarmery's own store file for this
// account, and strictly nothing else (usage.Client.Disconnect).
//
// # What never crosses this boundary
//
// The PKCE verifier and the CSRF state stay in this process, in pendingLogins.
// No response body here carries a token, a verifier or the authorization code —
// asserted by TestUsageLoginResponsesCarryNoSecrets — and no upstream response
// body is ever interpolated into an error: internal/usage returns fixed
// sentinels precisely so an authorization server cannot reflect text into the
// operator's browser through us.
//
// # Guards
//
//   - requireLocalOrigin (D4), as on every other state-changing endpoint.
//   - The account must be one the daemon actually reads (accountsFromRoots), so
//     these routes cannot be used to write — or delete — a credential file for
//     an arbitrary name.
//   - SWARMERY_USAGE_OAUTH=0 disables all three with 409, matching the read
//     path's ErrDisabled: the kill switch turns the whole OAuth surface off, not
//     just the polling half.

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// loginFlowTTL bounds how long a started authorization stays completable. Long
// enough for a real browser login (including a fresh claude.ai sign-in and a
// 2FA prompt), short enough that an abandoned flow's CSRF state does not sit in
// memory for the daemon's lifetime.
const loginFlowTTL = 10 * time.Minute

// maxLoginBodyBytes caps the complete-step body. An authorization code plus a
// state fragment is a few hundred bytes; anything near this cap is not one.
const maxLoginBodyBytes = 8 << 10

// pendingLogin is one started-but-unfinished authorization.
type pendingLogin struct {
	flow *usage.LoginFlow
	at   time.Time
}

// pendingLogins holds at most ONE in-flight flow per account: starting again
// replaces the previous one, which is also how an operator recovers from a
// browser tab they lost. Process-wide, like the usage cache — the flow belongs
// to the daemon, not to a request.
var (
	pendingLoginsMu sync.Mutex
	pendingLogins   = map[string]pendingLogin{}
)

func putPendingLogin(account string, flow *usage.LoginFlow, now time.Time) {
	pendingLoginsMu.Lock()
	defer pendingLoginsMu.Unlock()
	// Opportunistic sweep: expired flows for OTHER accounts are dead weight and
	// dead CSRF state, and there is no other moment that reliably runs.
	for k, p := range pendingLogins {
		if now.Sub(p.at) >= loginFlowTTL {
			delete(pendingLogins, k)
		}
	}
	pendingLogins[account] = pendingLogin{flow: flow, at: now}
}

// takePendingLogin removes and returns the account's flow when it exists and is
// still within the TTL. Removal is unconditional: a code is single-use, so a
// failed completion must not leave a flow a second attempt could replay.
func takePendingLogin(account string, now time.Time) *usage.LoginFlow {
	pendingLoginsMu.Lock()
	defer pendingLoginsMu.Unlock()
	p, ok := pendingLogins[account]
	if !ok {
		return nil
	}
	delete(pendingLogins, account)
	if now.Sub(p.at) >= loginFlowTTL {
		return nil
	}
	return p.flow
}

// resetPendingLogins clears the registry. Tests only — the flows are
// process-wide, so one test's leftover must not answer another's complete call.
func resetPendingLogins() {
	pendingLoginsMu.Lock()
	pendingLogins = map[string]pendingLogin{}
	pendingLoginsMu.Unlock()
}

// completeResponse — what one Connect click produced, step by step. Every
// field is a fixed vocabulary value or a fixed phrase (claudeprobe's Reason
// constants); nothing here may ever carry credential material or raw CLI
// output — TestUsageLoginResponsesCarryNoSecrets covers these fields too.
type completeResponse struct {
	// Connected is the quota half: the credential is in swarmery's store.
	Connected bool `json:"connected"`
	// Handoff is 'written' | 'already-present' | 'skipped-default' | 'failed'.
	Handoff string `json:"handoff"`
	// Runnable is the probe verdict: 'ready' | 'no-login' | 'unknown'.
	Runnable string `json:"runnable"`
	// Reason is a short fixed phrase when Runnable is not 'ready', "" otherwise.
	Reason string `json:"reason,omitempty"`
	// NextStep is what the UI should offer: "" when ready, 'pty-login' otherwise.
	NextStep string `json:"nextStep,omitempty"`
}

// completeResponse.Handoff values.
const (
	handoffOutcomeWritten        = "written"
	handoffOutcomeAlreadyPresent = "already-present"
	handoffOutcomeSkippedDefault = "skipped-default"
	handoffOutcomeFailed         = "failed"
)

// nextStepPTYLogin tells the UI to offer the interactive PTY login in place.
const nextStepPTYLogin = "pty-login"

// reasonVerdictNotStored is the fixed phrase for a probe whose verdict could
// not be persisted — the one complete-path failure that is neither the CLI's
// answer nor a handoff outcome. Fixed, like every claudeprobe reason.
const reasonVerdictNotStored = "the readiness check could not be recorded"

// handoffCredential is usage.HandoffToConfigDir behind a seam, so tests can
// force the one outcome ('failed') that has no deterministic filesystem
// trigger of its own. Production never reassigns it.
var handoffCredential = usage.HandoffToConfigDir

// knownUsageAccount resolves {account} against the accounts the daemon actually
// polls. Returning the SOURCE (not just a bool) is the point: the client the
// login runs on must be the very client the usage fan-out will later use, or the
// credential would be written for one account and read for another.
func knownUsageAccount(name string) (usage.Source, bool) {
	for _, src := range accountsFromRoots(transcriptsRoots) {
		if src.Account == name {
			return src, true
		}
	}
	return usage.Source{}, false
}

// POST /api/usage/accounts/{account}/login/start — begin an authorization.
//
// The response carries ONLY the URL to open. The verifier and state stay here.
func (h *Handler) usageLoginStart(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	src, ok := knownUsageAccount(account)
	if !ok {
		writeClientErr(w, http.StatusNotFound, "unknown account")
		return
	}

	flow, err := usageClientFor(src).StartLogin()
	switch {
	case errors.Is(err, usage.ErrDisabled):
		writeClientErr(w, http.StatusConflict, "live usage is switched off (SWARMERY_USAGE_OAUTH=0)")
		return
	case err != nil:
		// Only a CSPRNG failure reaches here. Generic on purpose.
		writeClientErr(w, http.StatusInternalServerError, "could not start the login")
		return
	}
	putPendingLogin(account, flow, time.Now())

	writeJSON(w, map[string]string{"authorizeUrl": flow.AuthorizeURL}, nil)
}

// POST /api/usage/accounts/{account}/login/complete — finish an authorization
// with the "code#state" value the callback page displayed, then hand the
// credential over and verify (see the file header's three-step contract).
//
// On success the account's credential is in swarmery's own store and the shared
// usage cache is dropped, so the next poll reflects the new connection instead
// of serving up to 30s of the "not connected" card the operator just fixed.
func (h *Handler) usageLoginComplete(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	src, ok := knownUsageAccount(account)
	if !ok {
		writeClientErr(w, http.StatusNotFound, "unknown account")
		return
	}

	var body struct {
		Code string `json:"code"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxLoginBodyBytes))
	if err != nil || json.Unmarshal(raw, &body) != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	flow := takePendingLogin(account, time.Now())
	if flow == nil {
		writeClientErr(w, http.StatusBadRequest,
			"no login in progress for this account — start the connection again")
		return
	}

	err = usageClientFor(src).CompleteLogin(r.Context(), flow, body.Code)
	switch {
	case err == nil:
		resetUsageCache()
		writeJSON(w, h.completeLoginOutcome(account, src), nil)
	case errors.Is(err, usage.ErrDisabled):
		writeClientErr(w, http.StatusConflict, "live usage is switched off (SWARMERY_USAGE_OAUTH=0)")
	case errors.Is(err, usage.ErrLoginCodeFormat):
		writeClientErr(w, http.StatusBadRequest,
			"that does not look like the full code — copy the whole value, including the part after the #")
	case errors.Is(err, usage.ErrLoginStateMismatch):
		writeClientErr(w, http.StatusBadRequest,
			"this code belongs to a different login attempt — start the connection again")
	default:
		// Covers a declined exchange and a store that could not be written.
		// One fixed string: the upstream body is never echoed (see the file
		// header), and the operator's next action is the same either way.
		writeClientErr(w, http.StatusBadRequest,
			"the authorization could not be completed — start the connection again")
	}
}

// completeLoginOutcome runs steps 2 and 3 of the complete transaction —
// handoff, then probe — and reports every outcome. It never fails: the stored
// credential (step 1) is already a fact, and nothing that happens here may
// roll it back or turn the response into an error. See the file header.
func (h *Handler) completeLoginOutcome(account string, src usage.Source) completeResponse {
	resp := completeResponse{Connected: true}

	// Step 2: handoff. The default account is skipped entirely — ~/.claude is
	// the CLI's own, there is nothing to hand over (its src.ConfigDir is empty
	// by the accountsFromRoots contract). ErrHandoffExists is NOT a failure:
	// the CLI already owns a credential there; verification decides the rest.
	switch {
	case src.ConfigDir == "":
		resp.Handoff = handoffOutcomeSkippedDefault
	default:
		switch err := handoffCredential(account, src.ConfigDir); {
		case err == nil:
			resp.Handoff = handoffOutcomeWritten
		case errors.Is(err, usage.ErrHandoffExists):
			resp.Handoff = handoffOutcomeAlreadyPresent
		case errors.Is(err, usage.ErrHandoffDefaultAccount):
			resp.Handoff = handoffOutcomeSkippedDefault
		default:
			resp.Handoff = handoffOutcomeFailed
		}
	}

	// Step 3: the authoritative probe, persisted exactly like the explicit
	// probe endpoint (same helper, same single-flight, source='probe'). The
	// default account's empty dir is claudeprobe.Probe's contract for "the
	// default account", so src.ConfigDir passes through unmapped.
	verdict, err := h.runAccountProbe(account, src.ConfigDir, probeSourceProbe)
	if err != nil {
		resp.Runnable = string(claudeprobe.StatusUnknown)
		resp.Reason = reasonVerdictNotStored
		resp.NextStep = nextStepPTYLogin
	} else {
		resp.Runnable = verdict.Status
		resp.Reason = verdict.Reason
		if claudeprobe.Status(verdict.Status) != claudeprobe.StatusReady {
			resp.NextStep = nextStepPTYLogin
		} else {
			// The login the operator just completed is what an auth breaker was
			// waiting for.
			h.closeAuthBreaker(account, store.BreakerClosedByLogin)
		}
	}
	// Fixed vocabulary values only — never an error's text, which could carry
	// a path (the same discipline as usage's handoffFailed logging).
	log.Printf("connect: account=%s handoff=%s runnable=%s", account, resp.Handoff, resp.Runnable)
	return resp
}

// DELETE /api/usage/accounts/{account}/login — disconnect an account.
//
// Removes the credential swarmery's OWN store holds for this account. The
// `claude` CLI's credential file and the macOS keychain item are untouched:
// they belong to the CLI, this daemon never writes to them, and a dashboard
// button that could end the operator's terminal login would be a trap. Nothing
// is revoked upstream either — the tokens stay valid at Anthropic until they
// expire; what ends is this daemon's use of them. See usage.Client.Disconnect.
//
// IDEMPOTENT: an account with no stored credential is already disconnected, so a
// missing file is 200, not 404. Only an unknown ACCOUNT is 404 — the same
// allow-list the two login steps enforce, for the same reason.
//
// The account's client is reset through the very same resolver the read path
// uses (usageClientFor), so the bearer it may still be replaying cannot outlive
// the credential that justified it; the shared usage cache is dropped so the
// next poll shows the disconnection instead of up to 30s of the card the
// operator just removed.
//
// The response is {ok:true} and nothing else: no path, no credential material,
// not even whether a file was there — which is also why the failure body is a
// fixed string rather than the store error, whose text carries the store path.
func (h *Handler) usageLoginDisconnect(w http.ResponseWriter, r *http.Request) {
	account := r.PathValue("account")
	src, ok := knownUsageAccount(account)
	if !ok {
		writeClientErr(w, http.StatusNotFound, "unknown account")
		return
	}

	switch err := usageClientFor(src).Disconnect(); {
	case err == nil:
		resetUsageCache()
		writeJSON(w, map[string]bool{"ok": true}, nil)
	case errors.Is(err, usage.ErrDisabled):
		writeClientErr(w, http.StatusConflict, "live usage is switched off (SWARMERY_USAGE_OAUTH=0)")
	default:
		writeClientErr(w, http.StatusInternalServerError, "the connection could not be removed")
	}
}
