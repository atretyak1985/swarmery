package api

// Signing the daemon in to a project's code host from the dashboard (Phase 8
// of the landing plan, SC-14) — for an operator with no `gh`/`glab` login on
// the daemon's machine, or a daemon on a remote host. Two ways in, both ending
// in the daemon's own credential store (credstore, ~/.swarmery/secrets/
// vcs-<host>.env, 0600):
//
//	POST   /api/projects/{id}/vcs/login            {"method":"device"}
//	       → 202 {loginId, userCode, verificationUri, verificationUriComplete?,
//	              expiresIn, interval}
//	GET    /api/projects/{id}/vcs/login/{loginId}  ONE poll step
//	       → 200 {status: pending|ok|expired|denied, login?, interval}
//	POST   /api/projects/{id}/vcs/login            {"method":"token","token":"…"}
//	       → 200 {status:"ok", login} | 422 not-authenticated
//	DELETE /api/projects/{id}/vcs/token            → 204
//
// # Device flow
//
// The daemon opens an OAuth device flow (deviceflow.Start) and keeps the
// Pending — whose DeviceCode is a bearer secret for the flow's lifetime —
// server-side, in an in-memory map keyed by a random loginId. The browser only
// ever sees the user code and the verification URI. Each GET is ONE poll step
// (the browser owns the loop and its interval, raised by slow_down); the poll
// runs detached from the request, because a granted device code is single-use
// and a client that navigates away mid-poll must not lose the token. Pending
// logins are daemon-memory only — lost on restart, the operator just starts
// again — capped at vcsLoginCapPerProject per project, and swept once their
// device code has expired (plus a grace, so a late poll still reads "expired"
// rather than "unknown").
//
// # Client ids
//
// The device flow needs the client id of an OAuth app on the host (public, not
// a secret; there is no client secret). Per host, first match wins:
//
//  1. `swarmery.vcs.clientIds.<host>` in the project's
//     .claude/settings.local.json (read per request by vcsLocalClientID — a
//     small reader of its own, because repoprovider.Config is the landing
//     config and carries no per-host map);
//  2. the daemon's env: SWARMERY_GITHUB_CLIENT_ID / SWARMERY_GITLAB_CLIENT_ID.
//
// None ⇒ 409 device-flow-unconfigured with a hint naming the env var and
// docs/vcs-login.md. The Token tab needs no client id.
//
// # Pasted token
//
// The token is validated BEFORE anything is written: the provider's AuthStatus
// runs with credstore.CandidateEnv — the token plus CLI config dirs under a
// throwaway temp dir — so a rejected token never touches the store and the
// validating call reads none of the operator's CLI config.
//
// # Secrets discipline
//
// The token is read from the request body once and never echoed; no response
// body carries it, nor the device code. Log lines name the project, the host
// and the login — never a token — and every error text that leaves this file
// has passed credstore.Redact (deviceflow errors already have).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/deviceflow"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/providers"
)

const (
	// vcsLoginCapPerProject bounds the pending device logins one project may
	// hold at once.
	vcsLoginCapPerProject = 10
	// vcsLoginGrace keeps an expired login answerable ("expired") for a while
	// before the sweep forgets it.
	vcsLoginGrace = 5 * time.Minute
	// maxVcsLoginBodyBytes bounds the POST body: a method and at most a token.
	maxVcsLoginBodyBytes = 16 << 10
	// vcsLoginNetTimeout bounds one device-flow exchange or token validation.
	vcsLoginNetTimeout = 30 * time.Second
	// vcsDocsHint is where the operator reads how to register the OAuth apps.
	vcsDocsHint = "tools/swarmery/docs/vcs-login.md"
)

// vcsClientIDEnv is the daemon env var carrying each provider's OAuth client id.
var vcsClientIDEnv = map[repoprovider.Kind]string{
	repoprovider.KindGitHub: "SWARMERY_GITHUB_CLIENT_ID",
	repoprovider.KindGitLab: "SWARMERY_GITLAB_CLIENT_ID",
}

// vcsTokenKey is the store key (and the CLI's env name) a provider's token
// lives under.
func vcsTokenKey(kind repoprovider.Kind) (string, bool) {
	switch kind {
	case repoprovider.KindGitHub:
		return credstore.GitHubTokenKey, true
	case repoprovider.KindGitLab:
		return credstore.GitLabTokenKey, true
	}
	return "", false
}

// vcsLogin is one pending device login. pollMu serialises poll steps: two
// overlapping GETs must not both redeem the same device code.
type vcsLogin struct {
	projectID int64
	pollMu    sync.Mutex
	// pending is read and written under the store's mu (Interval moves on
	// slow_down); pollMu only orders the network steps.
	pending deviceflow.Pending
}

// vcsLoginStore is the process-wide map of pending device logins.
type vcsLoginStore struct {
	mu     sync.Mutex
	now    func() time.Time
	logins map[string]*vcsLogin
}

func newVcsLoginStore() *vcsLoginStore {
	return &vcsLoginStore{now: time.Now, logins: map[string]*vcsLogin{}}
}

var vcsLogins = newVcsLoginStore()

var errTooManyLogins = errors.New("too many pending sign-ins for this project")

// sweepLocked forgets logins whose device code expired more than the grace
// ago. Caller holds mu.
func (s *vcsLoginStore) sweepLocked() {
	now := s.now()
	for id, l := range s.logins {
		if now.After(l.pending.ExpiresAt.Add(vcsLoginGrace)) {
			delete(s.logins, id)
		}
	}
}

// add stores p for projectID under a fresh random id.
func (s *vcsLoginStore) add(projectID int64, p deviceflow.Pending) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	n := 0
	now := s.now()
	for _, l := range s.logins {
		if l.projectID == projectID && now.Before(l.pending.ExpiresAt) {
			n++
		}
	}
	if n >= vcsLoginCapPerProject {
		return "", errTooManyLogins
	}
	s.logins[id] = &vcsLogin{projectID: projectID, pending: p}
	return id, nil
}

// get returns projectID's login id, or nil (unknown id, another project's id,
// or swept).
func (s *vcsLoginStore) get(projectID int64, id string) *vcsLogin {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	l, ok := s.logins[id]
	if !ok || l.projectID != projectID {
		return nil
	}
	return l
}

func (s *vcsLoginStore) snapshot(l *vcsLogin) deviceflow.Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return l.pending
}

func (s *vcsLoginStore) setInterval(l *vcsLogin, interval int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l.pending.Interval = interval
}

func (s *vcsLoginStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.logins, id)
}

// vcsLocalClientID reads `swarmery.vcs.clientIds.<host>` from the project's
// .claude/settings.local.json; "" when the file, the key or the host is absent
// or malformed. Hosts match case-insensitively.
func vcsLocalClientID(projectPath, host string) string {
	if projectPath == "" || host == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(projectPath, ".claude", "settings.local.json"))
	if err != nil {
		return ""
	}
	var doc struct {
		Swarmery struct {
			VCS struct {
				ClientIDs map[string]string `json:"clientIds"`
			} `json:"vcs"`
		} `json:"swarmery"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	for h, id := range doc.Swarmery.VCS.ClientIDs {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

// vcsClientID resolves the OAuth client id for host (see the file header).
func vcsClientID(projectPath string, kind repoprovider.Kind, host string) string {
	if id := vcsLocalClientID(projectPath, host); id != "" {
		return id
	}
	if name, ok := vcsClientIDEnv[kind]; ok {
		return strings.TrimSpace(os.Getenv(name))
	}
	return ""
}

// writeVcsLoginErr replies {error, code, hint} with status.
func writeVcsLoginErr(w http.ResponseWriter, status int, code, msg, hint string) {
	writeJSONStatus(w, status, map[string]any{"error": msg, "code": code, "hint": hint})
}

// vcsLoginTarget is the code host a sign-in is for.
type vcsLoginTarget struct {
	projectID int64
	path      string
	kind      repoprovider.Kind
	host      string
}

// resolveVcsLoginTarget detects the project's provider and host, answering the
// request itself (and ok=false) when there is none to sign in to. needKind:
// an unknown provider is refused (a sign-in needs a known CLI and token key);
// DELETE needs only the host.
func (h *Handler) resolveVcsLoginTarget(w http.ResponseWriter, r *http.Request, needKind bool) (vcsLoginTarget, bool) {
	id, path, ok := h.projectPathByID(w, r)
	if !ok {
		return vcsLoginTarget{}, false
	}
	if strings.TrimSpace(path) == "" {
		writeConflict(w, codeNoProjectPath, "project has no known path")
		return vcsLoginTarget{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), vcsProbeTimeout)
	defer cancel()
	det, err := repoprovider.Detect(ctx, vcsExec, path, repoprovider.LoadConfig(path), vcsProber)
	if err != nil || det.Remote.Host == "" {
		writeVcsLoginErr(w, http.StatusUnprocessableEntity, codeNoRemote, "no origin remote",
			"this repository has no `origin` remote with a host to sign in to — add one, then try again")
		return vcsLoginTarget{}, false
	}
	if _, known := vcsTokenKey(det.Kind); needKind && !known {
		writeVcsLoginErr(w, http.StatusUnprocessableEntity, codeProviderUnknown, "repository provider unknown",
			"the host of this repository's `origin` ("+det.Remote.Host+") is not a recognised code host — "+
				"answer which service hosts it in the banner, then sign in")
		return vcsLoginTarget{}, false
	}
	return vcsLoginTarget{projectID: id, path: path, kind: det.Kind, host: det.Remote.Host}, true
}

// postProjectVcsLogin handles POST /api/projects/{id}/vcs/login.
func (h *Handler) postProjectVcsLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Method string `json:"method"`
		Token  string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxVcsLoginBodyBytes)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	switch strings.TrimSpace(req.Method) {
	case "device":
		h.startVcsDeviceLogin(w, r)
	case "token":
		h.submitVcsToken(w, r, req.Token)
	default:
		writeClientErr(w, http.StatusBadRequest, `method must be "device" or "token"`)
	}
}

// startVcsDeviceLogin opens a device flow: 202 with what the human needs.
func (h *Handler) startVcsDeviceLogin(w http.ResponseWriter, r *http.Request) {
	t, ok := h.resolveVcsLoginTarget(w, r, true)
	if !ok {
		return
	}
	clientID := vcsClientID(t.path, t.kind, t.host)
	if clientID == "" {
		env := vcsClientIDEnv[t.kind]
		writeConflictFields(w, codeDeviceFlowUnconfigured,
			"no OAuth client id is configured for "+t.host+", so the device flow cannot start",
			map[string]any{
				"envVar": env,
				"hint": "Register an OAuth application on " + t.host + " with the device flow enabled, then set " +
					env + "=<client id> in the daemon's environment and restart it — or set " +
					`swarmery.vcs.clientIds["` + t.host + `"] in the project's .claude/settings.local.json. ` +
					"See " + vcsDocsHint + ". The Token tab works without a client id.",
			})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), vcsLoginNetTimeout)
	defer cancel()
	p, err := deviceflow.Start(ctx, t.kind, t.host, clientID, nil)
	if err != nil {
		writeDeviceFlowErr(w, t.host, err)
		return
	}
	loginID, err := vcsLogins.add(t.projectID, p)
	if errors.Is(err, errTooManyLogins) {
		writeVcsLoginErr(w, http.StatusTooManyRequests, codeTooManyLogins, err.Error(),
			"finish or wait out a sign-in already in progress, then try again")
		return
	}
	if err != nil {
		writeErr(w, err) // crypto/rand failure: no secret in it
		return
	}
	body := map[string]any{
		"loginId":         loginID,
		"userCode":        p.UserCode,
		"verificationUri": p.VerificationURI,
		"expiresIn":       p.ExpiresIn,
		"interval":        p.Interval,
	}
	if p.VerificationURIComplete != "" {
		body["verificationUriComplete"] = p.VerificationURIComplete
	}
	writeJSONStatus(w, http.StatusAccepted, body)
}

// writeDeviceFlowErr maps a deviceflow failure (already redacted) to a reply.
func writeDeviceFlowErr(w http.ResponseWriter, host string, err error) {
	var dfErr *deviceflow.Error
	if errors.As(err, &dfErr) && dfErr.Code == "device_flow_disabled" {
		writeVcsLoginErr(w, http.StatusConflict, codeDeviceFlowDisabled,
			"the device flow is disabled for this OAuth application on "+host,
			"Enable Device Flow in the OAuth application's settings on "+host+" (see "+vcsDocsHint+
				"), or sign in with a token instead.")
		return
	}
	writeVcsLoginErr(w, http.StatusBadGateway, codeDeviceFlowFailed,
		credstore.Redact(err.Error()),
		"Check the OAuth client id ("+vcsDocsHint+"), or sign in with a token instead.")
}

// getProjectVcsLogin handles GET /api/projects/{id}/vcs/login/{loginId}: ONE
// poll step.
func (h *Handler) getProjectVcsLogin(w http.ResponseWriter, r *http.Request) {
	id, _, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	loginID := r.PathValue("loginId")
	l := vcsLogins.get(id, loginID)
	if l == nil {
		writeVcsLoginErr(w, http.StatusNotFound, codeLoginNotFound, "unknown sign-in",
			"the sign-in finished, expired or was lost when the daemon restarted — start a new one")
		return
	}
	l.pollMu.Lock()
	defer l.pollMu.Unlock()
	// A poll that overlapped this one may already have finished the login.
	if vcsLogins.get(id, loginID) == nil {
		writeVcsLoginErr(w, http.StatusNotFound, codeLoginNotFound, "unknown sign-in",
			"the sign-in already finished — start a new one if needed")
		return
	}
	p := vcsLogins.snapshot(l)

	// Detached: a granted device code is single-use, so the exchange and the
	// store write must finish even when the browser goes away mid-poll.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), vcsLoginNetTimeout)
	defer cancel()
	tok, err := deviceflow.Poll(ctx, p)
	switch {
	case err == nil:
		// fall through to storing the token
	case errors.Is(err, deviceflow.ErrPending):
		writeJSON(w, map[string]any{"status": "pending", "interval": p.Interval}, nil)
		return
	case errors.Is(err, deviceflow.ErrSlowDown):
		next := deviceflow.NextInterval(p, err)
		vcsLogins.setInterval(l, next)
		writeJSON(w, map[string]any{"status": "pending", "interval": next}, nil)
		return
	case errors.Is(err, deviceflow.ErrExpired):
		vcsLogins.remove(loginID)
		writeJSON(w, map[string]any{"status": "expired", "interval": p.Interval}, nil)
		return
	case errors.Is(err, deviceflow.ErrDenied):
		vcsLogins.remove(loginID)
		writeJSON(w, map[string]any{"status": "denied", "interval": p.Interval}, nil)
		return
	default:
		writeDeviceFlowErr(w, p.Host, err)
		return
	}

	// Granted: the device code is spent whatever happens next.
	vcsLogins.remove(loginID)
	key, _ := vcsTokenKey(p.Kind)
	if err := credstore.Write(p.Host, key, tok.AccessToken); err != nil {
		writeErr(w, errors.New(credstore.Redact(err.Error())))
		return
	}
	InvalidateVcsCache(id)
	// The host itself just issued the token, so only an explicit rejection
	// undoes the write. An unknown answer (the provider CLI missing on this
	// machine, the API unreachable) keeps it: the next GET …/vcs re-probes.
	st := vcsAuthStatus(ctx, p.Kind, p.Host, vcsEnv)
	if st.Status == repoprovider.AuthExpired || st.Status == repoprovider.AuthMissing {
		// The host just granted it, yet rejects it: do not keep a dead token.
		_ = credstore.Delete(p.Host)
		InvalidateVcsCache(id)
		writeVcsLoginErr(w, http.StatusUnprocessableEntity, codeNotAuthenticated,
			"the granted token was rejected by "+p.Host, "start the sign-in again, or use a token")
		return
	}
	log.Printf("vcs: project %d signed in to %s via device flow (login %q)", id, p.Host, st.Login)
	writeJSON(w, map[string]any{"status": "ok", "login": st.Login, "interval": p.Interval}, nil)
}

// vcsAuthStatus runs the provider's AuthStatus for host with env as the
// credential env. A provider that cannot be built answers unknown.
func vcsAuthStatus(ctx context.Context, kind repoprovider.Kind, host string, env func(string) []string) repoprovider.AuthStatus {
	provider, err := providers.Factory(kind, vcsExec, env)
	if err != nil {
		return repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceNone}
	}
	st, _ := provider.AuthStatus(ctx, host)
	st.Login = strings.TrimSpace(st.Login)
	if st.Status == "" {
		st.Status = repoprovider.AuthUnknown
	}
	return st
}

// submitVcsToken validates a pasted token with a temporary env, and stores it
// only when the host accepted it.
func (h *Handler) submitVcsToken(w http.ResponseWriter, r *http.Request, token string) {
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, "\r\n") {
		writeClientErr(w, http.StatusBadRequest, "token must be a single non-empty line")
		return
	}
	t, ok := h.resolveVcsLoginTarget(w, r, true)
	if !ok {
		return
	}
	key, _ := vcsTokenKey(t.kind)

	tmp, err := os.MkdirTemp("", "swarmery-vcs-validate-*")
	if err != nil {
		writeErr(w, err)
		return
	}
	defer os.RemoveAll(tmp)
	env := credstore.CandidateEnv(t.host, key, token, tmp)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), vcsLoginNetTimeout)
	defer cancel()
	st := vcsAuthStatus(ctx, t.kind, t.host, func(string) []string { return env })
	switch st.Status {
	case repoprovider.AuthOK:
	case repoprovider.AuthExpired, repoprovider.AuthMissing:
		writeVcsLoginErr(w, http.StatusUnprocessableEntity, codeNotAuthenticated,
			t.host+" rejected this token", "check the token and its scopes, then try again — nothing was stored")
		return
	default:
		writeVcsLoginErr(w, http.StatusBadGateway, codeTokenUnverified,
			"could not check the token with "+t.host,
			"the provider's CLI is missing on the daemon's machine, or the host did not answer — nothing was stored")
		return
	}
	if err := credstore.Write(t.host, key, token); err != nil {
		writeErr(w, errors.New(credstore.Redact(err.Error())))
		return
	}
	InvalidateVcsCache(t.projectID)
	log.Printf("vcs: project %d stored a token for %s (login %q)", t.projectID, t.host, st.Login)
	writeJSON(w, map[string]any{"status": "ok", "login": st.Login}, nil)
}

// deleteProjectVcsToken handles DELETE /api/projects/{id}/vcs/token: the
// daemon forgets its token for the project's host (the next CLI call runs on
// the operator's own login again).
func (h *Handler) deleteProjectVcsToken(w http.ResponseWriter, r *http.Request) {
	t, ok := h.resolveVcsLoginTarget(w, r, false)
	if !ok {
		return
	}
	if err := credstore.Delete(t.host); err != nil {
		if errors.Is(err, credstore.ErrInsecure) {
			writeClientErr(w, http.StatusConflict, err.Error()) // path only, never a value
			return
		}
		writeErr(w, err)
		return
	}
	InvalidateVcsCache(t.projectID)
	log.Printf("vcs: project %d removed the stored token for %s", t.projectID, t.host)
	w.WriteHeader(http.StatusNoContent)
}
