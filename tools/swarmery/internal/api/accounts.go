package api

// Accounts: the dashboard's view of — and control over — the Claude Code
// accounts installed on this machine, plus the per-project binding that decides
// which one a project runs under.
//
//	GET    /api/accounts                  → every account with its live state
//	POST   /api/accounts                  → {key}     provision a config dir
//	DELETE /api/accounts/{account}        →           remove a config dir
//	POST   /api/accounts/{account}/probe  →           re-check CLI readiness
//	GET    /api/projects/{id}/account     → {account, effective, source, configDir}
//	PUT    /api/projects/{id}/account     → {account} bind, or "" to unbind
//
// # Write-once credential handoff, delegated login as the manual path
//
// The connect flow performs a WRITE-ONCE handoff of the account's
// swarmery-owned credential into its config dir (usage.HandoffToConfigDir),
// verified by the authoritative probe (internal/claudeprobe), with the
// interactive PTY login as the fallback. The evidence is
// docs/claude-cli-credential-behaviour.md (measured 2026-08-12, re-running the
// 2026-08-06 spike on CLI 2.1.220): a non-empty CLAUDE_CONFIG_DIR with no
// credential of its own fails authentication outright and does NOT fall back
// to the default account — so the handed-over file is both necessary and
// sufficient — and the CLI's own store deletes <dir>/.credentials.json after a
// successful Keychain write, so the file is consumed, not co-owned.
//
// What stays forbidden is REFRESHING that file. Token rotation lives
// exclusively in swarmery's own store (internal/usage/store.go), so swarmery
// and the CLI are never two writers over one rotating refresh token — the
// failure mode the original spike ruled out, and the reason the handoff is
// write-once by contract, not by accident.
//
// POST still hands back a loginCommand — the exact `CLAUDE_CONFIG_DIR=<dir>
// claude` invocation the operator runs, in the embedded terminal or their own
// — as the manual path. The existing PKCE flow (usage_login.go) authorizes
// SWARMERY against Anthropic for quota reads; the handoff is what turns that
// same stored credential into a CLI login.
//
// # Two honesty properties this file exists to preserve
//
//  1. `connected` is resolved through usage.LoadCredsFor, NOT by asking whether
//     a file is there. On macOS the default account's credential lives in the
//     login Keychain and has no file at all, so a file check would report the
//     operator's primary login as disconnected. A credential that fails to parse
//     is likewise not a connection.
//  2. `ingested` reports whether the daemon is ACTUALLY watching the account's
//     transcripts — its projects root being among the live ingest roots. Those
//     roots are resolved once at daemon start (cmd/swarmery: defaultProjectsRoots),
//     so an account provisioned afterwards is genuinely not ingested until a
//     restart, and POST says so in its hint. Hot re-attachment of ingest roots is
//     deliberately out of scope: it is a riskier refactor of the ingest pipeline,
//     and an honest "restart me" is cheaper and does not lie.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// maxAccountBodyBytes caps the request bodies here. Both carry a single account
// key; anything near this cap is not one.
const maxAccountBodyBytes = 4 << 10

// bindingSource values for accountBindingDTO.Source.
const (
	bindingSourceBinding = "binding"
	bindingSourceDefault = "default"
)

// accountCreds resolves ONE account's credential. A package var only so tests
// can answer deterministically: the DEFAULT account's real resolution ends at
// the macOS login Keychain (usage.chainCreds), which would make every assertion
// here depend on whether the machine running the test happens to be logged in.
// Production is the real thing — see the honesty note in the file header.
var accountCreds = usage.LoadCredsFor

// probeAccount runs the authoritative CLI-readiness probe. A package var for
// the same reason as accountCreds — and so tests can assert the READ paths
// never touch it: GET /api/accounts must never spawn a process.
var probeAccount = claudeprobe.Probe

// accountDTO is one installed account and everything the dashboard needs to
// decide what to offer for it.
//
// NO FIELD HERE CARRIES CREDENTIAL MATERIAL, and none ever may — asserted by
// TestAccountResponsesCarryNoSecrets, the twin of the login path's
// TestUsageLoginResponsesCarryNoSecrets.
type accountDTO struct {
	Key       string `json:"key"`
	ConfigDir string `json:"configDir"`
	IsDefault bool   `json:"isDefault"`
	// Connected is TRI-STATE, which is why it is a pointer: true/false when the
	// question was asked and answered, and null when it could not be asked at
	// all — SWARMERY_USAGE_OAUTH=0 switches credential resolution off wholesale
	// (usage.ErrDisabled). A plain bool would render that kill switch as "every
	// account is disconnected", which is a different and false statement.
	Connected *bool `json:"connected"`
	// Plan is the credential's raw rateLimitTier when one resolved, "" otherwise.
	// Deliberately RAW: the polished label on the usage card comes from
	// usage.inferPlan, and a second, subtly different derivation of "the
	// operator's plan" living here is how the two start disagreeing on screen.
	Plan string `json:"plan"`
	// Runnable is the authoritative "the CLI can run under this account"
	// verdict, tri-state for the same reason Connected is: null = never probed
	// (or the last probe could not determine an answer). It is a DIFFERENT
	// question from Connected (which means "swarmery can read this account's
	// quota") and the two legitimately disagree — that disagreement is exactly
	// the bug this field exists to expose. Populated from the stored verdict
	// (store.AllAccountRunnable) ONLY: listing accounts never runs a probe.
	Runnable *bool `json:"runnable"`
	// RunnableReason is a short fixed phrase (internal/claudeprobe constants)
	// explaining a false or undetermined verdict, "" otherwise.
	RunnableReason string `json:"runnableReason,omitempty"`
	// RunnableCheckedAt is when the verdict was taken (RFC3339), "" when never
	// probed.
	RunnableCheckedAt string `json:"runnableCheckedAt,omitempty"`
	// Ingested is whether the daemon is watching this account's transcripts —
	// see the file header. False for an account provisioned after startup.
	Ingested bool `json:"ingested"`
	// Projects are the project paths EXPLICITLY bound to this account. The
	// default account's list therefore holds only projects deliberately pinned
	// to it, not every unbound project that implicitly runs under it — a count
	// of "all of them" would carry no information.
	Projects []string `json:"projects"`
}

// accountsResponse is the GET /api/accounts body.
type accountsResponse struct {
	Accounts []accountDTO `json:"accounts"`
}

// provisionResponse is the POST /api/accounts body.
type provisionResponse struct {
	Account accountDTO `json:"account"`
	// LoginCommand is what the operator must run to finish. We hand over the
	// exact command instead of performing it — see the file header.
	LoginCommand string `json:"loginCommand"`
	// Hint is present exactly when the account is not yet ingested: the daemon
	// resolved its ingest roots at startup, so a restart is what makes this
	// account's sessions visible. Omitted rather than emptied when it does not
	// apply, so a client cannot render a stale instruction.
	Hint string `json:"hint,omitempty"`
}

// removeAccountResponse is the DELETE /api/accounts/{account} body.
type removeAccountResponse struct {
	OK bool `json:"ok"`
	// DanglingBindings are projects that were still bound to the account when it
	// was removed. Their binding now names an account that does not exist, and
	// claudeacct.EnvForAccount will point them at a config dir with no login in
	// it. Reported rather than silently rewritten: rebinding another project's
	// settings file is the operator's decision, not a side effect of a delete.
	DanglingBindings []string `json:"danglingBindings,omitempty"`
}

// accountBindingDTO is the per-project binding, for both GET and PUT.
type accountBindingDTO struct {
	// Account is the STORED binding — "" when the project has none.
	Account string `json:"account"`
	// Effective is the account this project actually runs under.
	Effective string `json:"effective"`
	// ConfigDir is the effective account's config dir.
	ConfigDir string `json:"configDir"`
	// Source is "binding" or "default". The two are visibly identical for a
	// project pinned to the default account, and mean different things the day
	// the default changes — which is exactly why the UI is given both.
	Source string `json:"source"`
}

// ── shared helpers ─────────────────────────────────────────────────────────

// findAccount resolves a key against the accounts that exist.
func findAccount(key string) (claudeacct.Account, bool) {
	for _, a := range claudeacct.DiscoverWithDefault() {
		if a.Key == key {
			return a, true
		}
	}
	return claudeacct.Account{}, false
}

// ingestedRoots is the live ingest roots as a cleaned set, so membership is a
// map lookup rather than an O(n) walk per account.
func ingestedRoots() map[string]bool {
	set := make(map[string]bool, len(transcriptsRoots))
	for _, root := range transcriptsRoots {
		set[filepath.Clean(strings.TrimSpace(root))] = true
	}
	return set
}

// bindingsByAccount maps account key → the project paths bound to it.
//
// One settings-file read per indexed project. That is a handful of small local
// reads on a machine with tens of projects, and the alternative — caching a
// binding the operator edits by hand in their editor — would go stale silently.
func (h *Handler) bindingsByAccount() (map[string][]string, error) {
	rows, err := h.DB.Query(`SELECT path FROM projects ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		if key := claudeacct.BindingForDisplay(path); key != "" {
			out[key] = append(out[key], path)
		}
	}
	return out, rows.Err()
}

// accountRow builds one account's DTO. runnable is the STORED verdict map
// (store.AllAccountRunnable) — this function must stay a pure read; the probe
// itself runs only from the explicit POST …/probe endpoint.
func accountRow(ctx context.Context, a claudeacct.Account, roots map[string]bool, bound map[string][]string, runnable map[string]store.AccountRunnable) accountDTO {
	row := accountDTO{
		Key:       a.Key,
		ConfigDir: a.ConfigDir,
		IsDefault: a.IsDefault,
		Ingested:  roots[filepath.Clean(a.ProjectsRoot())],
		Projects:  bound[a.Key],
	}
	if row.Projects == nil {
		row.Projects = []string{}
	}
	if verdict, ok := runnable[a.Key]; ok {
		row.Runnable, row.RunnableReason, row.RunnableCheckedAt = runnableDTOFields(verdict)
	}

	// The DEFAULT account is asked for with an EMPTY ConfigDir on purpose: that
	// selects usage's legacy resolution chain (CLAUDE_CONFIG_DIR, ~/.claude,
	// ~/.config/claude, and the plain keychain item on darwin), which is the only
	// source that resolves the stock account on macOS. Naming its dir here would
	// switch resolution to the EXCLUSIVE scoped file lookup and report the
	// operator's primary login as disconnected. This mirrors accountsFromRoots
	// (usage.go) exactly — the two must not drift.
	src := usage.Source{Account: a.Key}
	if !a.IsDefault {
		src.ConfigDir = a.ConfigDir
	}
	creds, err := accountCreds(ctx, src)
	switch {
	case errors.Is(err, usage.ErrDisabled):
		// Unknown, not disconnected: the kill switch turned the question off.
		row.Connected = nil
	case err != nil:
		no := false
		row.Connected = &no
	default:
		yes := true
		row.Connected = &yes
		row.Plan = strings.TrimSpace(creds.RateLimitTier)
	}
	return row
}

// runnableDTOFields maps one stored verdict to the DTO's three fields. Ready
// and no-login are answered questions (true / false); a stored 'unknown' —
// timeout, missing binary — renders as null exactly like "never probed",
// because "we could not determine it" must never be shown as "not ready", only
// with its reason and timestamp still attached so the operator can see a
// re-check happened.
func runnableDTOFields(v store.AccountRunnable) (*bool, string, string) {
	checkedAt := v.CheckedAt.UTC().Format(time.RFC3339)
	switch claudeprobe.Status(v.Status) {
	case claudeprobe.StatusReady:
		yes := true
		return &yes, "", checkedAt
	case claudeprobe.StatusNoLogin:
		no := false
		return &no, v.Reason, checkedAt
	default:
		return nil, v.Reason, checkedAt
	}
}

// loginCommandFor is the command the OPERATOR runs to finish an account's login.
//
// Quoted only when the path needs it, so the ordinary case is byte-identical to
// the documented `CLAUDE_CONFIG_DIR=<dir> claude` while a home directory with a
// space in it still yields a command that runs rather than one that silently
// starts the CLI on the DEFAULT account.
func loginCommandFor(dir string) string {
	return "CLAUDE_CONFIG_DIR=" + shellQuoteIfNeeded(dir) + " claude"
}

// shellQuoteIfNeeded single-quotes s unless every rune is shell-inert.
func shellQuoteIfNeeded(s string) string {
	safe := s != "" && !strings.ContainsFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '/' || r == '.' || r == '-' || r == '_':
			return false
		}
		return true
	})
	if safe {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bindingRow reads a project's binding and resolves what it means.
func bindingRow(path string) accountBindingDTO {
	bound := claudeacct.BindingForDisplay(path)
	row := accountBindingDTO{Account: bound, Effective: bound, Source: bindingSourceBinding}
	if bound == "" {
		row.Effective, row.Source = ingest.DefaultAccount, bindingSourceDefault
	}
	if a, ok := findAccount(row.Effective); ok {
		row.ConfigDir = a.ConfigDir
		return row
	}
	// A binding to an account that no longer exists. The dir is still reported
	// (it is where a spawn WOULD point — claudeacct.EnvForAccount), because a
	// blank field would read as "nothing is wrong here".
	if dir, err := claudeacct.ConfigDirFor(row.Effective); err == nil {
		row.ConfigDir = dir
	}
	return row
}

// ── handlers ───────────────────────────────────────────────────────────────

// listAccounts handles GET /api/accounts.
//
// Read-only and unfenced, like GET /api/usage. It exposes config-dir PATHS and
// connection state, never credential material.
func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	bound, err := h.bindingsByAccount()
	if err != nil {
		writeErr(w, err)
		return
	}
	runnable, err := store.AllAccountRunnable(h.DB)
	if err != nil {
		writeErr(w, err)
		return
	}
	roots := ingestedRoots()
	accounts := claudeacct.DiscoverWithDefault()
	rows := make([]accountDTO, 0, len(accounts))
	for _, a := range accounts {
		rows = append(rows, accountRow(r.Context(), a, roots, bound, runnable))
	}
	writeJSON(w, accountsResponse{Accounts: rows}, nil)
}

// createAccount handles POST /api/accounts — provision a config dir.
//
// IDEMPOTENT (claudeacct.Provision is), so it answers 200 rather than 201: the
// operator asked for the account to exist, and a retried click is not an error.
func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAccountBodyBytes)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key := strings.TrimSpace(req.Key)

	acct, err := claudeacct.Provision(key)
	switch {
	case errors.Is(err, claudeacct.ErrDefaultAccount):
		writeClientErr(w, http.StatusBadRequest,
			"the default account already exists and is never provisioned by swarmery")
		return
	case err != nil && !claudeacct.ValidKey(key):
		// The rejection the operator can act on, kept separate from a genuine
		// filesystem failure below.
		writeClientErr(w, http.StatusBadRequest,
			"invalid account key — use letters, digits, '-' or '_' (it becomes a directory name)")
		return
	case err != nil:
		writeErr(w, err)
		return
	}

	bound, err := h.bindingsByAccount()
	if err != nil {
		writeErr(w, err)
		return
	}
	runnable, err := store.AllAccountRunnable(h.DB)
	if err != nil {
		writeErr(w, err)
		return
	}
	row := accountRow(r.Context(), acct, ingestedRoots(), bound, runnable)
	resp := provisionResponse{Account: row, LoginCommand: loginCommandFor(acct.ConfigDir)}
	if !row.Ingested {
		resp.Hint = "the daemon resolves its ingest roots at startup, so this account's " +
			"sessions stay invisible until swarmery is restarted (make install / launchctl kickstart)"
	}
	writeJSON(w, resp, nil)
}

// deleteAccount handles DELETE /api/accounts/{account} — remove a config dir.
//
// The destructive operation of this surface: it takes the account's transcripts
// with it. Two guards, both deliberate — the default account is refused outright,
// and the key must name an account that actually exists, the same allow-list the
// login routes enforce so the route cannot be aimed at an arbitrary name.
func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("account"))
	if key == ingest.DefaultAccount {
		writeClientErr(w, http.StatusBadRequest,
			"the default account cannot be removed — it is the primary login, in ~/.claude")
		return
	}
	acct, ok := findAccount(key)
	if !ok || acct.IsDefault {
		writeClientErr(w, http.StatusNotFound, "unknown account")
		return
	}

	bound, err := h.bindingsByAccount()
	if err != nil {
		writeErr(w, err)
		return
	}
	switch err := claudeacct.Remove(key); {
	case errors.Is(err, claudeacct.ErrDefaultAccount):
		writeClientErr(w, http.StatusBadRequest, "the default account cannot be removed")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSON(w, removeAccountResponse{OK: true, DanglingBindings: bound[key]}, nil)
}

// accountProbeResponse is the POST /api/accounts/{account}/probe body — the same
// three fields the account row carries, so a client can patch its row in place.
type accountProbeResponse struct {
	Runnable          *bool  `json:"runnable"`
	RunnableReason    string `json:"runnableReason,omitempty"`
	RunnableCheckedAt string `json:"runnableCheckedAt,omitempty"`
}

// Verdict sources the probe endpoint may persist. 'probe' is the plain
// re-check; 'pty-login' marks a verdict taken right after an interactive PTY
// login, so its provenance stays legible in the store ('run' is written by the
// run-truth path, never through this endpoint).
const (
	probeSourceProbe    = "probe"
	probeSourcePTYLogin = "pty-login"
)

// probeFlights single-flights the probe per account key: a second concurrent
// POST for the same account waits for and returns the in-flight result rather
// than spawning a second CLI. Package-scoped state would bleed between test
// servers, so it lives on the Handler.
type probeFlights struct {
	mu     sync.Mutex
	flight map[string]*probeFlight
}

type probeFlight struct {
	done    chan struct{}
	verdict store.AccountRunnable
	err     error
}

// do runs fn once per key at a time; latecomers block on the leader's result.
func (p *probeFlights) do(key string, fn func() (store.AccountRunnable, error)) (store.AccountRunnable, error) {
	p.mu.Lock()
	if p.flight == nil {
		p.flight = map[string]*probeFlight{}
	}
	if f, ok := p.flight[key]; ok {
		p.mu.Unlock()
		<-f.done
		return f.verdict, f.err
	}
	f := &probeFlight{done: make(chan struct{})}
	p.flight[key] = f
	p.mu.Unlock()

	f.verdict, f.err = fn()
	p.mu.Lock()
	delete(p.flight, key)
	p.mu.Unlock()
	close(f.done)
	return f.verdict, f.err
}

// runAccountProbe runs the single-flighted readiness probe for one account and
// persists the verdict with the given source. Shared by the explicit probe
// endpoint and the connect flow's post-handoff verification — the two must
// never diverge on how a verdict is taken or stored.
//
// The probe's ctx is deliberately NOT a request's: a client that gives up
// and disconnects must not abort a probe a second waiter is blocked on
// (single-flight hands one result to everyone).
func (h *Handler) runAccountProbe(key, dir, source string) (store.AccountRunnable, error) {
	return h.probes.do(key, func() (store.AccountRunnable, error) {
		res := probeAccount(context.Background(), dir)
		row := store.AccountRunnable{
			Status:    string(res.Status),
			Reason:    res.Reason,
			CheckedAt: time.Now().UTC(),
			Source:    source,
		}
		if err := store.PutAccountRunnable(h.DB, key, row.Status, row.Reason, row.Source, row.CheckedAt); err != nil {
			return store.AccountRunnable{}, err
		}
		return row, nil
	})
}

// probeAccountHandler handles POST /api/accounts/{account}/probe — the
// explicit operator re-check. The verdict is persisted and returned; the list
// endpoint then serves the stored row.
//
// `?source=pty-login` records that the verdict follows an interactive PTY
// login (the connect flow's fallback re-probes through here when the terminal
// session ends). Anything outside the fixed set is refused: the source column
// is provenance, not a free-text field.
func (h *Handler) probeAccountHandler(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.PathValue("account"))
	acct, ok := findAccount(key)
	if !ok {
		writeClientErr(w, http.StatusNotFound, "unknown account")
		return
	}
	source := probeSourceProbe
	switch s := r.URL.Query().Get("source"); s {
	case "", probeSourceProbe:
	case probeSourcePTYLogin:
		source = probeSourcePTYLogin
	default:
		writeClientErr(w, http.StatusBadRequest,
			"invalid probe source — allowed: probe, pty-login")
		return
	}
	// The DEFAULT account is probed with an EMPTY configDir — absence of
	// CLAUDE_CONFIG_DIR, not its dir spelled out, is what selects the default
	// (claudeprobe.Probe's contract, same as claudeacct.EnvForAccount).
	dir := ""
	if !acct.IsDefault {
		dir = acct.ConfigDir
	}

	verdict, err := h.runAccountProbe(acct.Key, dir, source)
	if err != nil {
		writeErr(w, err)
		return
	}
	log.Printf("account probe: account=%s status=%s source=%s", acct.Key, verdict.Status, source)
	var resp accountProbeResponse
	resp.Runnable, resp.RunnableReason, resp.RunnableCheckedAt = runnableDTOFields(verdict)
	writeJSON(w, resp, nil)
}

// projectAccount handles GET /api/projects/{id}/account.
func (h *Handler) projectAccount(w http.ResponseWriter, r *http.Request) {
	_, path, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	writeJSON(w, bindingRow(path), nil)
}

// putProjectAccount handles PUT /api/projects/{id}/account.
//
// An account absent from Discover() is refused with 400. Without that check a
// project could be bound to a config dir that does not exist, and every spawn
// would start the CLI in an empty dir with no login in it — a failure that looks
// like "the CLI is broken" rather than "the binding is wrong". An empty account
// clears the binding.
//
// Fenced by requireLocalOrigin at the route; the account key is validated by
// claudeacct.ValidKey plus the existence check above.
//
// This endpoint deliberately does NOT gate on SWARMERY_ONBOARD_ROOTS, and the
// reason is the FILE it writes, not where the path came from (plan decision
// A2): a binding lives in .claude/settings.local.json — machine-local,
// gitignored, per-operator — whereas the roots allow-list exists to bound
// writes into a repo's SHARED, tracked files.
//
// It is NOT "the path comes from the projects table", so do not carry that
// reasoning to another route: putProjectConfig (project_config.go) resolves
// its target the very same way, {id} → the projects row's path, and still
// refuses when len(onboardCfg.Roots) == 0. DB-sourced is not the distinction;
// local-vs-shared is.
//
// The accepted consequence: ingest and wsingest INSERT a projects row for any
// path a transcript was seen under (ingest.go, wsingest.go), unbounded by
// onboardCfg.Roots. A PUT here can therefore create .claude/settings.local.json,
// and its parent .claude/ dir, inside a directory that was never explicitly
// onboarded. Accepted because the file is machine-local and the route is
// reachable only from a local origin — pinned by
// TestProjectAccountBindsAnUnOnboardedProject.
func (h *Handler) putProjectAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Account string `json:"account"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxAccountBodyBytes)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key := strings.TrimSpace(req.Account)

	_, path, ok := h.projectPathByID(w, r)
	if !ok {
		return
	}
	if key != "" {
		if _, exists := findAccount(key); !exists {
			writeClientErr(w, http.StatusBadRequest,
				"unknown account: "+key+" — provision it first, or every session would "+
					"start in a config dir with no login in it")
			return
		}
	}
	if err := claudeacct.SetBinding(path, key); err != nil {
		// SetBinding refuses an invalid key and an unparseable settings file;
		// both are the operator's to fix, neither is a server fault.
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, bindingRow(path), nil)
}
