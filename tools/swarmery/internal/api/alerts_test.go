package api

// Tests for the alerts surface (alerts.go) and the account circuit breaker's
// API edges: GET /api/alerts, POST /api/accounts/{account}/breaker/resume, the
// 409 account-breaker refusal on both run surfaces, and the close-on-probe /
// close-on-login hooks. Every probe is a stub — no test here may spawn the CLI.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/runcore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// noConfigDir is what useProbeRun records for a probe environment that carries
// no CLAUDE_CONFIG_DIR at all — absence, which is what selects ~/.claude.
const noConfigDir = "(none)"

// useProbeRun installs the two-stage probe seam behind "Probe & resume" and
// returns a live call counter plus, per call, the config dir selected by the
// ENVIRONMENT the probe was handed (noConfigDir when it carries none). The test
// process's own CLAUDE_CONFIG_DIR is removed first: the probe environment is
// composed onto os.Environ(), so a developer's shell would otherwise decide the
// assertions.
func useProbeRun(t *testing.T, fn func(dir string) claudeprobe.Result) (*atomic.Int32, *[]string) {
	t.Helper()
	if prev, had := os.LookupEnv("CLAUDE_CONFIG_DIR"); had {
		if err := os.Unsetenv("CLAUDE_CONFIG_DIR"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Setenv("CLAUDE_CONFIG_DIR", prev) })
	}
	var calls atomic.Int32
	dirs := &[]string{}
	prev := probeAccountRun
	probeAccountRun = func(_ context.Context, env []string) claudeprobe.Result {
		calls.Add(1)
		dir := noConfigDir
		for _, kv := range env {
			if v, ok := strings.CutPrefix(kv, "CLAUDE_CONFIG_DIR="); ok {
				dir = v
				break
			}
		}
		*dirs = append(*dirs, dir)
		return fn(dir)
	}
	t.Cleanup(func() { probeAccountRun = prev })
	return &calls, dirs
}

func listAlertsOK(t *testing.T, srv *httptest.Server) []alertDTO {
	t.Helper()
	status, body := acctDo(t, http.MethodGet, srv.URL+"/api/alerts", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/alerts = %d, want 200\n%s", status, body)
	}
	if !strings.Contains(body, `"alerts":[`) {
		t.Fatalf("alerts is not an array (a null would break the client):\n%s", body)
	}
	var resp alertsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode alerts: %v\n%s", err, body)
	}
	return resp.Alerts
}

func openTestBreaker(t *testing.T, db *sql.DB, account, kind, reason string, at time.Time) {
	t.Helper()
	if err := runcore.OpenBreaker(db, account, kind, reason, store.BreakerSourceRun, at); err != nil {
		t.Fatalf("open breaker %s: %v", account, err)
	}
}

func storedBreaker(t *testing.T, db *sql.DB, account string) store.AccountBreaker {
	t.Helper()
	b, _, err := store.GetAccountBreaker(db, account)
	if err != nil {
		t.Fatalf("get breaker %s: %v", account, err)
	}
	return b
}

// TestAlertsListsOpenBreaker: GET /api/alerts is every UNRESOLVED finding whose
// rule is in AlertRules — an open account breaker, with the breaker row's facts
// folded in — and nothing else: ordinary lint findings and closed breakers are
// not alerts. It is a pure read: no probe runs.
func TestAlertsListsOpenBreaker(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	db, srv := accountsTestDB(t, "alerts.db")
	calls, _ := useProbeRun(t, func(string) claudeprobe.Result {
		t.Error("GET /api/alerts ran a probe")
		return claudeprobe.Result{Status: claudeprobe.StatusReady}
	})

	if got := listAlertsOK(t, srv); len(got) != 0 {
		t.Fatalf("alerts on a healthy machine = %+v, want none", got)
	}

	opened := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	openTestBreaker(t, db, "nabu-org", store.BreakerKindAuth, claudeprobe.ReasonAccessRefused, opened)
	openTestBreaker(t, db, ingest.DefaultAccount, store.BreakerKindQuota, claudeprobe.ReasonRateLimited, opened)
	// An ordinary lint finding, and a breaker that opened and closed again.
	if _, err := db.Exec(`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at)
		VALUES ('agent:planner', 'missing_description', 'warn', 'no description', '2026-09-30T09:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	openTestBreaker(t, db, "gone-org", store.BreakerKindAuth, claudeprobe.ReasonNoLogin, opened)
	if _, err := runcore.CloseBreaker(db, "gone-org", store.BreakerClosedByOperator, opened.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got := listAlertsOK(t, srv)
	if len(got) != 2 {
		t.Fatalf("alerts = %+v, want exactly the two open breakers", got)
	}
	byAccount := map[string]alertDTO{}
	for _, a := range got {
		byAccount[a.Account] = a
		if a.ID == 0 || a.DetectedAt == "" {
			t.Errorf("alert %+v lacks its finding id or detection time", a)
		}
	}
	auth := byAccount["nabu-org"]
	if auth.Rule != store.AccountBreakerRule || auth.Target != "account:nabu-org" || auth.Severity != "error" ||
		auth.Message != store.AccountBreakerMessage(store.BreakerKindAuth) ||
		auth.Kind != "auth" || auth.Reason != claudeprobe.ReasonAccessRefused ||
		auth.OpenedAt != "2026-09-30T12:00:00Z" || auth.ResetsAt != "" {
		t.Errorf("auth alert = %+v", auth)
	}
	quota := byAccount[ingest.DefaultAccount]
	if quota.Kind != "quota" || quota.Reason != claudeprobe.ReasonRateLimited ||
		quota.Message != store.AccountBreakerMessage(store.BreakerKindQuota) || quota.ResetsAt != "2026-09-30T13:00:00Z" {
		t.Errorf("quota alert = %+v, want its reset time an hour after opening", quota)
	}

	// Closing a breaker resolves its alert.
	if _, err := runcore.CloseBreaker(db, "nabu-org", store.BreakerClosedByProbe, opened.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := listAlertsOK(t, srv); len(got) != 1 || got[0].Account != ingest.DefaultAccount {
		t.Errorf("alerts after the close = %+v, want only the default account's", got)
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("probe calls = %d, want 0", n)
	}

	// The exported rule list is what decides membership.
	if len(AlertRules) == 0 || AlertRules[0] != store.AccountBreakerRule {
		t.Errorf("AlertRules = %v, want the breaker rule in it", AlertRules)
	}
}

// TestResumeEndpoint: POST /api/accounts/{account}/breaker/resume probes THAT
// account (its own config dir; empty for the default) with the two-stage probe
// and closes the breaker only on a ready answer. Anything else is 409 with the
// probe's fixed reason and the breaker left open.
func TestResumeEndpoint(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	db, srv := accountsTestDB(t, "alerts-resume.db")
	opened := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	openTestBreaker(t, db, "nabu-org", store.BreakerKindAuth, claudeprobe.ReasonAccessRefused, opened)
	openTestBreaker(t, db, ingest.DefaultAccount, store.BreakerKindAuth, claudeprobe.ReasonNoLogin, opened)

	answer := claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonAccessRefused}
	calls, dirs := useProbeRun(t, func(string) claudeprobe.Result { return answer })
	resumeURL := func(account string) string { return srv.URL + "/api/accounts/" + account + "/breaker/resume" }

	// Still refused: 409, the reason, and the breaker exactly as it was.
	status, body := acctDo(t, http.MethodPost, resumeURL("nabu-org"), "")
	if status != http.StatusConflict {
		t.Fatalf("resume on a refused account = %d, want 409\n%s", status, body)
	}
	var refused struct {
		Error, Code, Account, Status, Reason string
	}
	if err := json.Unmarshal([]byte(body), &refused); err != nil {
		t.Fatalf("decode 409: %v\n%s", err, body)
	}
	if refused.Code != codeAccountNotReady || refused.Account != "nabu-org" || refused.Status != "no-login" ||
		refused.Reason != claudeprobe.ReasonAccessRefused || refused.Error != claudeprobe.ReasonAccessRefused {
		t.Errorf("409 body = %+v", refused)
	}
	if b := storedBreaker(t, db, "nabu-org"); !b.IsOpen() || b.OpenedAt != "2026-09-30T12:00:00Z" {
		t.Errorf("breaker after a refused resume = %+v, want the same opening", b)
	}
	if len(listAlertsOK(t, srv)) != 2 {
		t.Error("a refused resume resolved an alert")
	}

	// A probe that cannot answer proves nothing: 409, breaker untouched.
	answer = claudeprobe.Result{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonTimeout}
	status, body = acctDo(t, http.MethodPost, resumeURL("nabu-org"), "")
	if status != http.StatusConflict || !strings.Contains(body, claudeprobe.ReasonTimeout) {
		t.Errorf("resume on an unknown probe = %d, want 409 with the timeout reason\n%s", status, body)
	}
	if b := storedBreaker(t, db, "nabu-org"); !b.IsOpen() {
		t.Error("an unknown probe closed the breaker")
	}

	// Ready: 200, closed by probe, alert resolved, verdict stored.
	answer = claudeprobe.Result{Status: claudeprobe.StatusReady}
	status, body = acctDo(t, http.MethodPost, resumeURL("nabu-org"), "")
	if status != http.StatusOK {
		t.Fatalf("resume on a ready account = %d, want 200\n%s", status, body)
	}
	var ok resumeResponse
	if err := json.Unmarshal([]byte(body), &ok); err != nil {
		t.Fatalf("decode 200: %v\n%s", err, body)
	}
	if !ok.OK || ok.Account != "nabu-org" || ok.State != store.BreakerClosed {
		t.Errorf("200 body = %+v", ok)
	}
	if b := storedBreaker(t, db, "nabu-org"); b.IsOpen() || b.ClosedBy != store.BreakerClosedByProbe {
		t.Errorf("breaker = %+v, want closed by probe", b)
	}
	if v, found, err := store.GetAccountRunnable(db, "nabu-org"); err != nil || !found || v.Status != "ready" || v.Source != "probe" {
		t.Errorf("stored verdict = %+v found=%v err=%v, want ready/probe", v, found, err)
	}
	alerts := listAlertsOK(t, srv)
	if len(alerts) != 1 || alerts[0].Account != ingest.DefaultAccount {
		t.Errorf("alerts after the resume = %+v, want only the default account's", alerts)
	}

	// The default key is probed under the environment an UNBOUND project's run
	// gets. With nothing inherited that carries no config dir at all — absence
	// selects ~/.claude.
	if status, body := acctDo(t, http.MethodPost, resumeURL(ingest.DefaultAccount), ""); status != http.StatusOK {
		t.Fatalf("resume default = %d\n%s", status, body)
	}
	named := filepath.Join(home, ".claude-nabu-org")
	if want := []string{named, named, named, noConfigDir}; strings.Join(*dirs, "|") != strings.Join(want, "|") {
		t.Errorf("probed dirs = %v, want %v", *dirs, want)
	}
	if len(listAlertsOK(t, srv)) != 0 {
		t.Error("alerts remain after both accounts were resumed")
	}

	// A daemon that itself runs under a config dir (`install --claude-config-dir`):
	// unbound runs inherit it, so resuming the default key must probe THAT dir —
	// not ~/.claude, which those runs never touch. A named account still gets its
	// own dir, replacing the inherited one.
	inherited := filepath.Join(t.TempDir(), "daemon-config-dir")
	t.Setenv("CLAUDE_CONFIG_DIR", inherited)
	openTestBreaker(t, db, ingest.DefaultAccount, store.BreakerKindAuth, claudeprobe.ReasonNoLogin, opened)
	openTestBreaker(t, db, "nabu-org", store.BreakerKindAuth, claudeprobe.ReasonNoLogin, opened)
	for _, account := range []string{ingest.DefaultAccount, "nabu-org"} {
		if status, body := acctDo(t, http.MethodPost, resumeURL(account), ""); status != http.StatusOK {
			t.Fatalf("resume %s under an inherited config dir = %d\n%s", account, status, body)
		}
	}
	if got := (*dirs)[len(*dirs)-2:]; got[0] != inherited || got[1] != named {
		t.Errorf("probed dirs = %v, want [%s %s] — the default key must keep the inherited dir", got, inherited, named)
	}

	// An unknown account is refused before any probe runs.
	before := calls.Load()
	if status, _ := acctDo(t, http.MethodPost, resumeURL("ghost"), ""); status != http.StatusNotFound {
		t.Errorf("resume (unknown account) = %d, want 404", status)
	}
	// A foreign origin is refused by the same D4 fence as every other write.
	req, err := http.NewRequest(http.MethodPost, resumeURL("nabu-org"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin resume = %d, want 403", resp.StatusCode)
	}
	if n := calls.Load(); n != before {
		t.Errorf("a refused request ran the probe (%d → %d calls)", before, n)
	}
}

// accountBreakerBody is the 409 wire shape of an account-breaker refusal.
type accountBreakerBody struct {
	Error    string `json:"error"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Account  string `json:"account"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
	OpenedAt string `json:"openedAt"`
	ResetsAt string `json:"resetsAt"`
}

func decodeAccountBreaker(t *testing.T, resp *http.Response) accountBreakerBody {
	t.Helper()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	var b accountBreakerBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	if b.Error != codeAccountBreaker || b.Code != codeAccountBreaker {
		t.Errorf("error/code = %q/%q, want %q", b.Error, b.Code, codeAccountBreaker)
	}
	return b
}

// End to end through the REAL gate: the fixture project's account has an open
// breaker, so the phase run is refused 409 account-breaker naming the account,
// the kind and the fixed reason — and nothing is stamped on the phase.
func TestPhaseRun_AccountBreaker_409(t *testing.T) {
	srv, db, taskID, _ := epicFixture(t)
	p1, _ := fixturePhaseIDs(t, db, taskID)
	stub := &phaseStubRunner{}
	svc := attachPhaseRun(t, db, stub, true)
	account := runcore.QuotaAccountKey(runcore.AccountFor("/repo/p"))
	openTestBreaker(t, db, account, store.BreakerKindAuth, claudeprobe.ReasonAccessRefused,
		time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))

	b := decodeAccountBreaker(t, postPhase(t, phaseRunURL(srv, taskID, p1)))
	if b.Account != account || b.Kind != "auth" || b.Reason != claudeprobe.ReasonAccessRefused ||
		b.OpenedAt != "2026-09-30T12:00:00Z" || b.ResetsAt != "" {
		t.Errorf("body = %+v", b)
	}
	if b.Message == "" {
		t.Error("no human-readable message")
	}
	if svc.Slots.Count() != 0 || len(stub.dispatchedSpecs()) != 0 {
		t.Errorf("slots held = %d, spawns = %d, want 0 and 0", svc.Slots.Count(), len(stub.dispatchedSpecs()))
	}
	var state string
	if err := db.QueryRow(`SELECT run_state FROM epic_phases WHERE id=?`, p1).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "idle" {
		t.Errorf("run_state = %q, want idle — a paused account is not a failed phase", state)
	}
}

// The plan surface answers the same refusal with the same shape, reset time
// included for a quota opening; a bare sentinel still answers 409.
func TestRunPlan_AccountBreaker_409(t *testing.T) {
	srv, db, taskID := planFixtureWithReadme(t)
	svc := attachPlanRun(t, db, &planrunStubRunner{}, true)
	svc.AccountCheck = func(context.Context, *sql.DB, claudeacct.Resolution, time.Time) error {
		return &runcore.AccountBreakerError{Account: "work", Kind: "quota", Reason: claudeprobe.ReasonRateLimited,
			OpenedAt: "2026-09-30T12:00:00Z", ResetsAt: "2026-09-30T15:00:00Z"}
	}

	b := decodeAccountBreaker(t, postPlanRun(t, planRunURL(srv, taskID), ""))
	if b.Account != "work" || b.Kind != "quota" || b.Reason != claudeprobe.ReasonRateLimited ||
		b.ResetsAt != "2026-09-30T15:00:00Z" {
		t.Errorf("body = %+v", b)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM plan_runs WHERE workspace_task_id=?`, taskID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("plan_runs rows = %d, want 0", n)
	}

	svc.AccountCheck = func(context.Context, *sql.DB, claudeacct.Resolution, time.Time) error {
		return runcore.ErrAccountBreaker
	}
	if b := decodeAccountBreaker(t, postPlanRun(t, planRunURL(srv, taskID), "")); b.Account != "" || b.Message == "" {
		t.Errorf("bare sentinel body = %+v, want empty fields and a message", b)
	}
}

// TestProbeClosesAuthBreaker: the existing operator re-check closes an AUTH
// breaker when it comes back ready, leaves it open when it does not, and never
// touches a quota breaker — a working login says nothing about a usage limit.
func TestProbeClosesAuthBreaker(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	db, srv := accountsTestDB(t, "alerts-probe-close.db")
	opened := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	openTestBreaker(t, db, "nabu-org", store.BreakerKindAuth, claudeprobe.ReasonNoLogin, opened)
	openTestBreaker(t, db, ingest.DefaultAccount, store.BreakerKindQuota, claudeprobe.ReasonRateLimited, opened)

	answer := claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}
	useProbe(t, func(context.Context, string) claudeprobe.Result { return answer })

	if status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts/nabu-org/probe", ""); status != http.StatusOK {
		t.Fatalf("probe = %d\n%s", status, body)
	}
	if b := storedBreaker(t, db, "nabu-org"); !b.IsOpen() {
		t.Fatal("a no-login probe closed the breaker")
	}

	answer = claudeprobe.Result{Status: claudeprobe.StatusReady}
	for _, account := range []string{"nabu-org", ingest.DefaultAccount} {
		if status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts/"+account+"/probe", ""); status != http.StatusOK {
			t.Fatalf("probe %s = %d\n%s", account, status, body)
		}
	}
	if b := storedBreaker(t, db, "nabu-org"); b.IsOpen() || b.ClosedBy != store.BreakerClosedByProbe {
		t.Errorf("auth breaker = %+v, want closed by probe", b)
	}
	if b := storedBreaker(t, db, ingest.DefaultAccount); !b.IsOpen() {
		t.Errorf("quota breaker = %+v, want still open — a ready login does not lift a usage limit", b)
	}
	alerts := listAlertsOK(t, srv)
	if len(alerts) != 1 || alerts[0].Account != ingest.DefaultAccount {
		t.Errorf("alerts = %+v, want only the quota one", alerts)
	}
}

// TestLoginCompleteClosesAuthBreaker: finishing a login whose verification comes
// back ready closes the account's auth breaker ('login'); one that does not
// leaves it open.
func TestLoginCompleteClosesAuthBreaker(t *testing.T) {
	useTempCredentialStore(t)
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	db, srv := usageTestDB(t, "alerts-login-close.db", time.Now().UTC().Format("2006-01-02T15:04:05"))
	up := loginUpstream(t, 200)
	installLoginClient(t, usage.Source{Account: "nabu-org", ConfigDir: dirs["nabu-org"]}, up)
	openTestBreaker(t, db, "nabu-org", store.BreakerKindAuth, claudeprobe.ReasonNoLogin,
		time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))

	useProbeResult(t, claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}, nil)
	if got := completeLogin(t, srv, "nabu-org"); got.Runnable != "no-login" {
		t.Fatalf("complete = %+v, want a no-login verdict", got)
	}
	if b := storedBreaker(t, db, "nabu-org"); !b.IsOpen() {
		t.Fatal("a login that did not verify closed the breaker")
	}

	useProbeResult(t, claudeprobe.Result{Status: claudeprobe.StatusReady}, nil)
	if got := completeLogin(t, srv, "nabu-org"); got.Runnable != "ready" {
		t.Fatalf("complete = %+v, want ready", got)
	}
	if b := storedBreaker(t, db, "nabu-org"); b.IsOpen() || b.ClosedBy != store.BreakerClosedByLogin {
		t.Errorf("breaker = %+v, want closed by login", b)
	}
}

// TestAlertsAreNotLintFindings: an alert is stored as a config_lint_findings row
// (AlertRules) but is not config lint. The System page's severity badges and the
// hub's lint count must leave it out — its target matches no component, so a
// badge that counted it would filter to a list where nothing shows.
func TestAlertsAreNotLintFindings(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "alerts-lint.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at, resolved_at) VALUES
	      ('hook:1', 'hook_no_timeout', 'warn', 'no timeout set', '2026-09-30T00:00:00Z', NULL),
	      (?, ?, 'error', 'paused', '2026-09-30T00:00:00Z', NULL),
	      ('auto-mode-classifier', 'auto_mode_no_verdict', 'warn', 'no verdict', '2026-09-30T00:00:00Z', NULL)`,
		store.AccountBreakerTarget("work"), store.AccountBreakerRule); err != nil {
		t.Fatal(err)
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	var s struct {
		Lint struct {
			Error int64 `json:"error"`
			Warn  int64 `json:"warn"`
		} `json:"lint"`
	}
	getJSON(t, srv.URL+"/api/system/summary", &s)
	if s.Lint.Error != 0 || s.Lint.Warn != 1 {
		t.Errorf("summary.lint = %+v, want error=0 warn=1 (the hook finding only)", s.Lint)
	}
	var hub struct {
		LintFindings int64 `json:"lintFindings"`
	}
	getJSON(t, srv.URL+"/api/system/hub/summary", &hub)
	if hub.LintFindings != 1 {
		t.Errorf("hub lintFindings = %d, want 1 (the hook finding only)", hub.LintFindings)
	}
	// Both alerts are still alerts.
	if got := listAlertsOK(t, srv); len(got) != 2 {
		t.Errorf("alerts = %d, want 2", len(got))
	}
}
