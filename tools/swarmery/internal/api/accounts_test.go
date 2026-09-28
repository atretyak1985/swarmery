package api

// Tests for the account-management endpoints (accounts.go).
//
// # Isolation contract
//
// These endpoints CREATE and DELETE directories under $HOME, so the bar is the
// highest in this package:
//
//   - attachHomeAccounts points $HOME at a t.TempDir for the duration of one
//     test. claudeacct resolves every config dir through os.UserHomeDir, so no
//     Provision, Remove or Discover in these tests can see — let alone remove —
//     the operator's real ~/.claude.
//   - the same helper installs a credential resolver that answers "not
//     connected" for everything. Without it the DEFAULT account's real
//     resolution ends at the macOS login Keychain, and every `connected`
//     assertion would depend on whether the machine running the test is logged
//     in. Tests that need a real resolution opt in explicitly, and only for a
//     NON-default account, whose resolution is a plain file read.
//   - the credential store stays inside TestMain's temp dir (usage_login_test.go),
//     narrowed per test with useTempCredentialStore where it matters.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// Fixture credential material. The `sk-ant` shape is deliberate: it makes
// TestAccountResponsesCarryNoSecrets fail loudly if a token ever reaches a body.
const (
	acctFixtureToken = "sk-ant-oat01-FAKE-ACCOUNT-ACCESS-TOKEN"
	acctFixtureTier  = "default_claude_max_20x"
)

// ── plumbing ───────────────────────────────────────────────────────────────

// useAccountCreds installs the credential resolver for one test.
func useAccountCreds(t *testing.T, fn func(context.Context, usage.Source) (*usage.Creds, error)) {
	t.Helper()
	prev := accountCreds
	accountCreds = fn
	t.Cleanup(func() { accountCreds = prev })
}

// noAccountCreds is the default answer: nothing is connected.
func noAccountCreds(context.Context, usage.Source) (*usage.Creds, error) {
	return nil, usage.ErrNoCreds
}

// attachHomeAccounts points $HOME at a temp dir, materialises one config dir per
// named account (with its projects/ tree, so discovery sees it), attaches those
// roots as the daemon's live ingest roots, and installs the not-connected
// credential resolver. Returns the home dir and each account's config dir.
//
// Pass no accounts for a machine with nothing installed at all.
func attachHomeAccounts(t *testing.T, accounts ...string) (string, map[string]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	dirs := make(map[string]string, len(accounts))
	roots := make([]string, 0, len(accounts))
	for _, a := range accounts {
		name := ".claude"
		if a != ingest.DefaultAccount {
			name = ".claude-" + a
		}
		dir := filepath.Join(home, name)
		root := filepath.Join(dir, "projects")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatalf("create %s: %v", root, err)
		}
		dirs[a] = dir
		roots = append(roots, root)
	}

	prev := transcriptsRoots
	AttachProjectsRoots(roots)
	t.Cleanup(func() { AttachProjectsRoots(prev) })
	useAccountCreds(t, noAccountCreds)
	return home, dirs
}

// accountsTestDB opens a throwaway store with one projects row per given path
// and serves the full API off it.
func accountsTestDB(t *testing.T, name string, projectPaths ...string) (*sql.DB, *httptest.Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC().Format("2006-01-02T15:04:05")
	for i, p := range projectPaths {
		if _, err := db.Exec(
			`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (?, ?, ?, ?, ?)`,
			i+1, p, fmt.Sprintf("p%d", i+1), fmt.Sprintf("P%d", i+1), now); err != nil {
			t.Fatalf("insert project %s: %v", p, err)
		}
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return db, srv
}

// acctDo issues a request with an optional JSON body and returns status + body.
func acctDo(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new %s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(raw)
}

// listAccountsOK does GET /api/accounts and decodes it, failing on non-200.
func listAccountsOK(t *testing.T, srv *httptest.Server) []accountDTO {
	t.Helper()
	status, body := acctDo(t, http.MethodGet, srv.URL+"/api/accounts", "")
	if status != http.StatusOK {
		t.Fatalf("GET /api/accounts = %d, want 200\n%s", status, body)
	}
	var resp accountsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode accounts: %v\n%s", err, body)
	}
	return resp.Accounts
}

// accountNamed returns the row for key, failing the test when it is absent.
func accountNamed(t *testing.T, rows []accountDTO, key string) accountDTO {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	t.Fatalf("account %q missing; got %v", key, keys)
	return accountDTO{}
}

// writeCLICreds writes a `claude`-shaped credential file into an account's
// config dir — the CLI's own plaintext fallback shape (parseCreds reads it).
func writeCLICreds(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write credential: %v", err)
	}
}

// ── GET /api/accounts ──────────────────────────────────────────────────────

// TestAccountsListOnAnEmptyMachine: a machine with no config dir at all still
// reports the DEFAULT account. It is where the CLI looks with no
// CLAUDE_CONFIG_DIR set, so hiding it would hide the operator's primary login
// from its own management screen.
func TestAccountsListOnAnEmptyMachine(t *testing.T) {
	home, _ := attachHomeAccounts(t)
	_, srv := accountsTestDB(t, "accounts-empty.db")

	rows := listAccountsOK(t, srv)
	if len(rows) != 1 {
		t.Fatalf("got %d accounts, want exactly the default one: %+v", len(rows), rows)
	}
	def := rows[0]
	if def.Key != ingest.DefaultAccount || !def.IsDefault {
		t.Errorf("row = %+v, want the default account", def)
	}
	if def.ConfigDir != filepath.Join(home, ".claude") {
		t.Errorf("configDir = %s, want %s", def.ConfigDir, filepath.Join(home, ".claude"))
	}
	if def.Connected == nil || *def.Connected {
		t.Errorf("connected = %v, want false", def.Connected)
	}
	if def.Ingested {
		t.Error("ingested = true with no ingest roots configured")
	}
	if len(def.Projects) != 0 {
		t.Errorf("projects = %v, want empty", def.Projects)
	}
}

// TestAccountsListMultiAccount is the whole read contract on a machine with two
// subscriptions: every field, including the per-account project bindings.
func TestAccountsListMultiAccount(t *testing.T) {
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	projectA, projectB := t.TempDir(), t.TempDir()
	_, srv := accountsTestDB(t, "accounts-multi.db", projectA, projectB)

	// projectA is pinned to nabu-org; projectB is left unbound.
	if err := claudeacct.SetBinding(projectA, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	// nabu-org is connected with a known plan; the default account is not.
	useAccountCreds(t, func(_ context.Context, src usage.Source) (*usage.Creds, error) {
		if src.Account == "nabu-org" {
			return &usage.Creds{AccessToken: acctFixtureToken, RateLimitTier: acctFixtureTier}, nil
		}
		return nil, usage.ErrNoCreds
	})

	rows := listAccountsOK(t, srv)
	if len(rows) != 2 {
		t.Fatalf("got %d accounts, want 2: %+v", len(rows), rows)
	}

	def := accountNamed(t, rows, ingest.DefaultAccount)
	if !def.IsDefault || def.Connected == nil || *def.Connected || def.Plan != "" {
		t.Errorf("default row = %+v, want isDefault, not connected, no plan", def)
	}
	if !def.Ingested {
		t.Error("the default account is an attached ingest root but reports ingested=false")
	}
	if len(def.Projects) != 0 {
		t.Errorf("default projects = %v — only EXPLICIT bindings are listed", def.Projects)
	}

	named := accountNamed(t, rows, "nabu-org")
	if named.IsDefault {
		t.Error("nabu-org reports isDefault")
	}
	if named.ConfigDir != dirs["nabu-org"] {
		t.Errorf("configDir = %s, want %s", named.ConfigDir, dirs["nabu-org"])
	}
	if named.Connected == nil || !*named.Connected {
		t.Errorf("connected = %v, want true", named.Connected)
	}
	if named.Plan != acctFixtureTier {
		t.Errorf("plan = %q, want %q", named.Plan, acctFixtureTier)
	}
	if !named.Ingested {
		t.Error("nabu-org is an attached ingest root but reports ingested=false")
	}
	if !slices.Equal(named.Projects, []string{projectA}) {
		t.Errorf("projects = %v, want [%s]", named.Projects, projectA)
	}
}

// TestAccountsConnectedIsResolvedNotFileExistence pins the honesty property that
// the whole DTO rests on. The REAL usage.LoadCredsFor runs here (for the
// non-default account, whose resolution is a plain file read and so cannot reach
// the machine's Keychain), and a credential file that exists but does not parse
// must read as NOT connected — otherwise "connected" would mean "a file is
// there", which on macOS is neither necessary nor sufficient.
func TestAccountsConnectedIsResolvedNotFileExistence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		credsFile     string // "" = no file at all
		wantConnected bool
		wantPlan      string
	}{
		{name: "no credential at all", wantConnected: false},
		{
			name:      "a file that is not JSON",
			credsFile: "this is not a credential",
		},
		{
			name:      "well-formed JSON with no access token",
			credsFile: `{"claudeAiOauth":{"refreshToken":"only-a-refresh-token"}}`,
		},
		{
			name: "a real credential",
			credsFile: `{"claudeAiOauth":{"accessToken":"` + acctFixtureToken +
				`","rateLimitTier":"` + acctFixtureTier + `"}}`,
			wantConnected: true,
			wantPlan:      acctFixtureTier,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTempCredentialStore(t)
			_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
			_, srv := accountsTestDB(t, "accounts-connected.db")
			if tc.credsFile != "" {
				writeCLICreds(t, dirs["nabu-org"], tc.credsFile)
			}
			// The genuine resolver for the account under test; the default
			// account is neutralised because ITS chain ends at the Keychain.
			useAccountCreds(t, func(ctx context.Context, src usage.Source) (*usage.Creds, error) {
				if src.Account == ingest.DefaultAccount {
					return nil, usage.ErrNoCreds
				}
				return usage.LoadCredsFor(ctx, src)
			})

			row := accountNamed(t, listAccountsOK(t, srv), "nabu-org")
			if row.Connected == nil {
				t.Fatalf("connected = null, want %v", tc.wantConnected)
			}
			if *row.Connected != tc.wantConnected {
				t.Errorf("connected = %v, want %v", *row.Connected, tc.wantConnected)
			}
			if row.Plan != tc.wantPlan {
				t.Errorf("plan = %q, want %q", row.Plan, tc.wantPlan)
			}
		})
	}
}

// TestAccountsKillSwitchLeavesConnectedUnknown: SWARMERY_USAGE_OAUTH=0 switches
// credential resolution off wholesale. The list must keep working with
// `connected` UNKNOWN (null) — reporting false would state that every account is
// disconnected, which is a different and untrue claim, and erroring would take
// the whole management screen down with the kill switch.
func TestAccountsKillSwitchLeavesConnectedUnknown(t *testing.T) {
	useTempCredentialStore(t)
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	_, srv := accountsTestDB(t, "accounts-killswitch.db")
	// A credential that WOULD resolve, so "unknown" cannot be confused with
	// "there was nothing to find".
	writeCLICreds(t, dirs["nabu-org"],
		`{"claudeAiOauth":{"accessToken":"`+acctFixtureToken+`","rateLimitTier":"`+acctFixtureTier+`"}}`)
	useAccountCreds(t, usage.LoadCredsFor)
	t.Setenv("SWARMERY_USAGE_OAUTH", "0")

	status, body := acctDo(t, http.MethodGet, srv.URL+"/api/accounts", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the kill switch must not break the list\n%s", status, body)
	}
	var resp accountsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	for _, row := range resp.Accounts {
		if row.Connected != nil {
			t.Errorf("%s: connected = %v, want null (unknown)", row.Key, *row.Connected)
		}
		if row.Plan != "" {
			t.Errorf("%s: plan = %q, want empty while resolution is off", row.Key, row.Plan)
		}
	}
	if !strings.Contains(body, `"connected":null`) {
		t.Errorf("body does not serialise the unknown state as null:\n%s", body)
	}
}

// ── POST /api/accounts ─────────────────────────────────────────────────────

// TestAccountsProvision is the delegated-login contract: the config dir appears,
// the response hands back the exact command the operator runs, and it says out
// loud that the account is not being ingested yet.
func TestAccountsProvision(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount)
	_, srv := accountsTestDB(t, "accounts-provision.db")

	status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts", `{"key":"nabu-org"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", status, body)
	}
	var resp provisionResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}

	dir := filepath.Join(home, ".claude-nabu-org")
	if resp.Account.Key != "nabu-org" || resp.Account.ConfigDir != dir {
		t.Errorf("account = %+v, want nabu-org at %s", resp.Account, dir)
	}
	if want := "CLAUDE_CONFIG_DIR=" + dir + " claude"; resp.LoginCommand != want {
		t.Errorf("loginCommand = %q, want %q", resp.LoginCommand, want)
	}
	// The account was provisioned AFTER the daemon resolved its ingest roots, so
	// it is genuinely not being watched — and the response must say so.
	if resp.Account.Ingested {
		t.Error("ingested = true for an account provisioned after startup")
	}
	if !strings.Contains(strings.ToLower(resp.Hint), "restart") {
		t.Errorf("hint = %q, want it to tell the operator to restart the daemon", resp.Hint)
	}

	// VARIANT A: provisioning creates directories and writes NO credential.
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("config dir mode = %v (err %v), want 0700", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "projects")); err != nil {
		t.Errorf("projects dir missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); !os.IsNotExist(err) {
		t.Errorf("provisioning wrote a credential file (stat err = %v)", err)
	}

	// It is now discoverable, and once the daemon restarts onto the new root it
	// reports as ingested — the hint above is the only thing standing between.
	if row := accountNamed(t, listAccountsOK(t, srv), "nabu-org"); row.Ingested {
		t.Error("ingested flipped without the roots changing")
	}
	prev := transcriptsRoots
	AttachProjectsRoots(append(slices.Clone(prev), filepath.Join(dir, "projects")))
	t.Cleanup(func() { AttachProjectsRoots(prev) })
	if row := accountNamed(t, listAccountsOK(t, srv), "nabu-org"); !row.Ingested {
		t.Error("ingested is still false after the account's root was attached")
	}
}

// TestAccountsProvisionIsIdempotent: a retried click is not an error, and it
// does not disturb a config dir the CLI has already logged into.
func TestAccountsProvisionIsIdempotent(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount)
	_, srv := accountsTestDB(t, "accounts-provision-idem.db")

	if status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts", `{"key":"nabu-org"}`); status != http.StatusOK {
		t.Fatalf("first POST = %d\n%s", status, body)
	}
	marker := filepath.Join(home, ".claude-nabu-org", ".claude.json")
	if err := os.WriteFile(marker, []byte(`{"cli":"state"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts", `{"key":"nabu-org"}`)
	if status != http.StatusOK {
		t.Fatalf("second POST = %d, want 200\n%s", status, body)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("re-provisioning destroyed the CLI's own state: %v", err)
	}
	if n := len(listAccountsOK(t, srv)); n != 2 {
		t.Errorf("got %d accounts after provisioning the same key twice, want 2", n)
	}
}

// TestAccountsProvisionRejectsBadInput: the key becomes a directory name under
// $HOME, and the default account is never provisioned. Nothing may be created by
// any of these.
func TestAccountsProvisionRejectsBadInput(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty key", `{"key":""}`},
		{"path separator", `{"key":"a/b"}`},
		{"parent traversal", `{"key":"../escape"}`},
		{"leading dot", `{"key":".hidden"}`},
		{"embedded space", `{"key":"has space"}`},
		{"the default account", `{"key":"default"}`},
		{"malformed body", `{"key":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _ := attachHomeAccounts(t, ingest.DefaultAccount)
			_, srv := accountsTestDB(t, "accounts-bad-key.db")

			status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400\n%s", status, body)
			}
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != ".claude" {
				names := make([]string, 0, len(entries))
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("$HOME holds %v after a rejected request, want only .claude", names)
			}
		})
	}
}

// ── DELETE /api/accounts/{account} ─────────────────────────────────────────

// TestAccountsDeleteRemovesTheConfigDir is the happy path, plus the report of
// bindings the removal left dangling.
func TestAccountsDeleteRemovesTheConfigDir(t *testing.T) {
	home, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	_, srv := accountsTestDB(t, "accounts-delete.db", project)
	if err := claudeacct.SetBinding(project, "nabu-org"); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}

	status, body := acctDo(t, http.MethodDelete, srv.URL+"/api/accounts/nabu-org", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200\n%s", status, body)
	}
	var resp removeAccountResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if !resp.OK {
		t.Error("ok = false")
	}
	if !slices.Equal(resp.DanglingBindings, []string{project}) {
		t.Errorf("danglingBindings = %v, want [%s] — a project left pointing at a "+
			"removed account must be reported, not silently rewritten", resp.DanglingBindings, project)
	}
	if _, err := os.Stat(dirs["nabu-org"]); !os.IsNotExist(err) {
		t.Errorf("the config dir survived (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects")); err != nil {
		t.Errorf("removing an account touched ~/.claude: %v", err)
	}
}

// TestAccountsDeleteRefusesTheDefaultAccount is the guard that matters most on
// this route: DELETE ends in os.RemoveAll, and ~/.claude is the primary login
// with the operator's credential in it.
func TestAccountsDeleteRefusesTheDefaultAccount(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	_, srv := accountsTestDB(t, "accounts-delete-default.db")
	writeCLICreds(t, filepath.Join(home, ".claude"),
		`{"claudeAiOauth":{"accessToken":"NOT-A-REAL-TOKEN"}}`)

	status, body := acctDo(t, http.MethodDelete, srv.URL+"/api/accounts/default", "")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\n%s", status, body)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err != nil {
		t.Errorf("the default account's credential was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects")); err != nil {
		t.Errorf("the default account's transcripts were removed: %v", err)
	}
}

// TestAccountsDeleteRejectsUnknownAccounts: the same allow-list the login routes
// enforce, so the route cannot be aimed at an arbitrary name.
func TestAccountsDeleteRejectsUnknownAccounts(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount)
	_, srv := accountsTestDB(t, "accounts-delete-unknown.db")

	status, body := acctDo(t, http.MethodDelete, srv.URL+"/api/accounts/not-an-account", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404\n%s", status, body)
	}
	if !strings.Contains(body, "unknown account") {
		t.Errorf("body = %s, want an 'unknown account' error", body)
	}
}

// ── GET/PUT /api/projects/{id}/account ─────────────────────────────────────

// TestProjectAccountBindingLifecycle: unbound → bound → unbound, with `source`
// telling "nothing is chosen" apart from "the default is chosen" at every step.
func TestProjectAccountBindingLifecycle(t *testing.T) {
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	_, srv := accountsTestDB(t, "project-account.db", project)
	url := srv.URL + "/api/projects/1/account"

	get := func() accountBindingDTO {
		t.Helper()
		status, body := acctDo(t, http.MethodGet, url, "")
		if status != http.StatusOK {
			t.Fatalf("GET = %d, want 200\n%s", status, body)
		}
		var row accountBindingDTO
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			t.Fatalf("decode: %v\n%s", err, body)
		}
		return row
	}

	// 1. No binding at all.
	row := get()
	if row.Account != "" || row.Effective != ingest.DefaultAccount || row.Source != "default" {
		t.Errorf("unbound row = %+v, want {account:\"\" effective:default source:default}", row)
	}
	if row.ConfigDir != dirs[ingest.DefaultAccount] {
		t.Errorf("configDir = %s, want %s", row.ConfigDir, dirs[ingest.DefaultAccount])
	}

	// 2. Bound to a named account.
	status, body := acctDo(t, http.MethodPut, url, `{"account":"nabu-org"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d, want 200\n%s", status, body)
	}
	row = get()
	if row.Account != "nabu-org" || row.Effective != "nabu-org" || row.Source != "binding" {
		t.Errorf("bound row = %+v, want nabu-org from a binding", row)
	}
	if row.ConfigDir != dirs["nabu-org"] {
		t.Errorf("configDir = %s, want %s", row.ConfigDir, dirs["nabu-org"])
	}
	if got := claudeacct.Binding(project); got != "nabu-org" {
		t.Errorf("on-disk binding = %q, want nabu-org", got)
	}

	// 3. Pinned to the DEFAULT account: identical effect to step 1, different
	//    meaning — which is exactly why `source` exists.
	if status, body := acctDo(t, http.MethodPut, url, `{"account":"default"}`); status != http.StatusOK {
		t.Fatalf("PUT default = %d\n%s", status, body)
	}
	row = get()
	if row.Account != ingest.DefaultAccount || row.Source != "binding" {
		t.Errorf("pinned row = %+v, want source=binding for an explicit default", row)
	}

	// 4. Cleared.
	if status, body := acctDo(t, http.MethodPut, url, `{"account":""}`); status != http.StatusOK {
		t.Fatalf("PUT clear = %d\n%s", status, body)
	}
	row = get()
	if row.Account != "" || row.Source != "default" {
		t.Errorf("cleared row = %+v, want an empty binding falling back to default", row)
	}
	if got := claudeacct.Binding(project); got != "" {
		t.Errorf("on-disk binding = %q after clearing, want empty", got)
	}
}

// TestProjectAccountRejectsUnknownAccount: binding to an account that does not
// exist would point every session at a config dir with no login in it, so it is
// a 400 and NOTHING is written.
func TestProjectAccountRejectsUnknownAccount(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount)
	project := t.TempDir()
	_, srv := accountsTestDB(t, "project-account-unknown.db", project)

	status, body := acctDo(t, http.MethodPut, srv.URL+"/api/projects/1/account",
		`{"account":"never-provisioned"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\n%s", status, body)
	}
	if !strings.Contains(body, "unknown account") {
		t.Errorf("body = %s, want it to name the problem", body)
	}
	if got := claudeacct.Binding(project); got != "" {
		t.Errorf("binding = %q, want nothing written", got)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "settings.local.json")); !os.IsNotExist(err) {
		t.Errorf("a rejected PUT still created a settings file (stat err = %v)", err)
	}
}

// TestProjectAccountUnknownProject: {id} must name an indexed project.
func TestProjectAccountUnknownProject(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount)
	_, srv := accountsTestDB(t, "project-account-404.db")

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		status, body := acctDo(t, method, srv.URL+"/api/projects/42/account", `{"account":""}`)
		if status != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404\n%s", method, status, body)
		}
	}
}

// TestProjectAccountBindsAnUnOnboardedProject pins a DECISION, not an accident:
// with SWARMERY_ONBOARD_ROOTS unset, a PUT still binds a project whose path sits
// outside every configured root, creating .claude/settings.local.json there.
//
// It is reachable because ingest and wsingest mint a projects row for any path a
// transcript was seen under, so {id} can name a directory nobody onboarded. It
// is accepted because settings.local.json is machine-local and gitignored and
// the route is behind requireLocalOrigin — sibling routes that write SHARED,
// tracked files (putProjectConfig) gate on the roots instead. If this test ever
// has to change, the fence above putProjectAccount is what changed.
func TestProjectAccountBindsAnUnOnboardedProject(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")

	// No roots at all — the state a daemon runs in with SWARMERY_ONBOARD_ROOTS
	// unset, which is what disables the sibling config/plugin writes outright.
	prev := onboardCfg
	AttachOnboard(OnboardConfig{})
	t.Cleanup(func() { onboardCfg = prev })

	// A project dir under no root whatsoever, as a transcript-seen row would be.
	project := t.TempDir()
	_, srv := accountsTestDB(t, "project-account-unonboarded.db", project)

	status, body := acctDo(t, http.MethodPut, srv.URL+"/api/projects/1/account",
		`{"account":"nabu-org"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 — the roots fence is not meant to apply here\n%s", status, body)
	}
	if got := claudeacct.Binding(project); got != "nabu-org" {
		t.Errorf("on-disk binding = %q, want nabu-org", got)
	}
	settings := filepath.Join(project, ".claude", "settings.local.json")
	if _, err := os.Stat(settings); err != nil {
		t.Errorf("stat %s: %v — the binding must land in the machine-local file", settings, err)
	}
}

// TestProjectAccountReportsAnIgnoredBinding: a PUT whose binding the provenance
// gate then ignores still writes the file, but the answer must say the binding
// is not in effect and why — not a bare 200 that reads as success.
func TestProjectAccountReportsAnIgnoredBinding(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	// A .git FILE whose gitdir is gone: git answers with an error, not a
	// verdict, so the binding is unclassifiable and ignored (fail closed).
	if err := os.WriteFile(filepath.Join(project, ".git"), []byte("gitdir: /nonexistent/nowhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, srv := accountsTestDB(t, "project-account-ignored.db", project)

	status, body := acctDo(t, http.MethodPut, srv.URL+"/api/projects/1/account", `{"account":"nabu-org"}`)
	if status != http.StatusOK {
		t.Fatalf("PUT = %d, want 200\n%s", status, body)
	}
	var row accountBindingDTO
	if err := json.Unmarshal([]byte(body), &row); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	if row.Source != bindingSourceDefault || row.Effective != ingest.DefaultAccount {
		t.Errorf("row = %+v, want the default account in effect", row)
	}
	if !strings.Contains(row.IgnoredReason, "not trusted") {
		t.Errorf("ignoredReason = %q, want the gate's reason", row.IgnoredReason)
	}
}

// ── guards ─────────────────────────────────────────────────────────────────

// TestAccountsStateChangingRoutesRejectCrossOrigin: all three writes carry the
// same D4 origin hardening as every other mutating endpoint.
func TestAccountsStateChangingRoutesRejectCrossOrigin(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	_, srv := accountsTestDB(t, "accounts-origin.db", project)

	probeCalls := useProbe(t, func(context.Context, string) claudeprobe.Result {
		return claudeprobe.Result{Status: claudeprobe.StatusReady}
	})
	for _, tc := range []struct{ name, method, path, body string }{
		{"provision", http.MethodPost, "/api/accounts", `{"key":"evil"}`},
		{"remove", http.MethodDelete, "/api/accounts/nabu-org", ""},
		{"bind", http.MethodPut, "/api/projects/1/account", `{"account":"nabu-org"}`},
		{"probe", http.MethodPost, "/api/accounts/nabu-org/probe", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reader io.Reader
			if tc.body != "" {
				reader = strings.NewReader(tc.body)
			}
			req, err := http.NewRequest(tc.method, srv.URL+tc.path, reader)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Origin", "https://evil.example")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403 for a foreign origin", resp.StatusCode)
			}
		})
	}

	// None of the rejected calls had any effect.
	if n := probeCalls.Load(); n != 0 {
		t.Errorf("a cross-origin probe ran the CLI %d time(s)", n)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-evil")); !os.IsNotExist(err) {
		t.Errorf("a cross-origin provision created a config dir (stat err = %v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-nabu-org")); err != nil {
		t.Errorf("a cross-origin delete removed a config dir: %v", err)
	}
	if got := claudeacct.Binding(project); got != "" {
		t.Errorf("a cross-origin bind wrote %q", got)
	}
}

// TestAccountResponsesCarryNoSecrets is the token-leak guard for this surface —
// the twin of TestUsageLoginResponsesCarryNoSecrets on the login path.
//
// A credential that REALLY resolves is put in place first (the `connected:true`
// assertion below proves the scan is not vacuous), and then not one byte of it
// may appear in any of the five endpoints' bodies. These endpoints report ON
// credentials; they must never report credentials.
func TestAccountResponsesCarryNoSecrets(t *testing.T) {
	useTempCredentialStore(t)
	_, dirs := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	project := t.TempDir()
	_, srv := accountsTestDB(t, "accounts-secrets.db", project)

	const refresh = "sk-ant-ort01-FAKE-ACCOUNT-REFRESH-TOKEN"
	writeCLICreds(t, dirs["nabu-org"], `{"claudeAiOauth":{"accessToken":"`+acctFixtureToken+
		`","refreshToken":"`+refresh+`","rateLimitTier":"`+acctFixtureTier+`"}}`)
	useAccountCreds(t, func(ctx context.Context, src usage.Source) (*usage.Creds, error) {
		if src.Account == ingest.DefaultAccount {
			return nil, usage.ErrNoCreds
		}
		return usage.LoadCredsFor(ctx, src)
	})

	// Non-vacuity: the credential really does resolve through this endpoint.
	if row := accountNamed(t, listAccountsOK(t, srv), "nabu-org"); row.Connected == nil || !*row.Connected {
		t.Fatal("the fixture credential did not resolve; the scan below would prove nothing")
	}

	// The probe seam answers no-login, so the runnable fields (runnable,
	// runnableReason, runnableCheckedAt) are POPULATED in the scanned bodies —
	// the scan covers them non-vacuously.
	useProbe(t, func(context.Context, string) claudeprobe.Result {
		return claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}
	})

	bodies := map[string]string{}
	for _, call := range []struct{ name, method, path, body string }{
		{"GET /api/accounts", http.MethodGet, "/api/accounts", ""},
		{"GET project account", http.MethodGet, "/api/projects/1/account", ""},
		{"PUT project account", http.MethodPut, "/api/projects/1/account", `{"account":"nabu-org"}`},
		{"POST /api/accounts", http.MethodPost, "/api/accounts", `{"key":"second-org"}`},
		{"POST probe", http.MethodPost, "/api/accounts/nabu-org/probe", ""},
		{"GET /api/accounts after probe", http.MethodGet, "/api/accounts", ""},
		{"DELETE /api/accounts", http.MethodDelete, "/api/accounts/nabu-org", ""},
	} {
		status, body := acctDo(t, call.method, srv.URL+call.path, call.body)
		if status != http.StatusOK {
			t.Fatalf("%s = %d, want 200\n%s", call.name, status, body)
		}
		bodies[call.name] = body
	}

	for name, body := range bodies {
		for _, banned := range []string{
			acctFixtureToken, refresh, "sk-ant", "accessToken", "access_token",
			"refreshToken", "refresh_token", "claudeAiOauth", "credentials",
			"Bearer", "bearer",
		} {
			if strings.Contains(body, banned) {
				t.Errorf("%s body contains %q:\n%s", name, banned, body)
			}
		}
	}
}

// ── the runnable verdict (probe + stored rows) ──────────────────────────────

// useProbe installs the probe seam for one test and returns a live call
// counter. The default (claudeprobe.Probe) would spawn the real CLI — no test
// here may do that.
func useProbe(t *testing.T, fn func(context.Context, string) claudeprobe.Result) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prev := probeAccount
	probeAccount = func(ctx context.Context, dir string) claudeprobe.Result {
		calls.Add(1)
		return fn(ctx, dir)
	}
	t.Cleanup(func() { probeAccount = prev })
	return &calls
}

// TestAccountsRunnableIsNullWhenNeverProbed: no verdict row → runnable null,
// no reason, no timestamp — and, the SC-3 flip side, GET /api/accounts never
// runs a probe to fill the gap: the seam stays untouched.
func TestAccountsRunnableIsNullWhenNeverProbed(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	_, srv := accountsTestDB(t, "accounts-runnable-null.db")
	calls := useProbe(t, func(context.Context, string) claudeprobe.Result {
		t.Error("GET /api/accounts ran a probe")
		return claudeprobe.Result{Status: claudeprobe.StatusReady}
	})

	for _, key := range []string{ingest.DefaultAccount, "nabu-org"} {
		row := accountNamed(t, listAccountsOK(t, srv), key)
		if row.Runnable != nil {
			t.Errorf("%s: runnable = %v, want null (never probed)", key, *row.Runnable)
		}
		if row.RunnableReason != "" || row.RunnableCheckedAt != "" {
			t.Errorf("%s: reason/checkedAt = (%q, %q), want empty", key, row.RunnableReason, row.RunnableCheckedAt)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("GET /api/accounts spawned %d probe(s), want 0", n)
	}
}

// TestAccountsRunnableFromStoredVerdict: the list serves the persisted row —
// true for ready, false + reason for no-login, null (with reason and
// timestamp) for a stored unknown — and the row survives a daemon restart
// (SC-3): a SECOND server over the same database answers identically.
func TestAccountsRunnableFromStoredVerdict(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org", "third-org")
	db, srv := accountsTestDB(t, "accounts-runnable-stored.db")
	useProbe(t, func(context.Context, string) claudeprobe.Result {
		t.Error("GET /api/accounts ran a probe")
		return claudeprobe.Result{}
	})

	checked := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	for _, put := range []struct{ account, status, reason string }{
		{ingest.DefaultAccount, "ready", ""},
		{"nabu-org", "no-login", claudeprobe.ReasonNoLogin},
		{"third-org", "unknown", claudeprobe.ReasonTimeout},
	} {
		if err := store.PutAccountRunnable(db, put.account, put.status, put.reason, "probe", checked); err != nil {
			t.Fatalf("seed verdict for %s: %v", put.account, err)
		}
	}

	assertRows := func(t *testing.T, srv *httptest.Server) {
		t.Helper()
		rows := listAccountsOK(t, srv)
		def := accountNamed(t, rows, ingest.DefaultAccount)
		if def.Runnable == nil || !*def.Runnable || def.Runnable == def.Connected {
			t.Errorf("default: runnable = %v, want true (own pointer)", def.Runnable)
		}
		if def.RunnableCheckedAt != checked.Format(time.RFC3339) {
			t.Errorf("default: checkedAt = %q, want %q", def.RunnableCheckedAt, checked.Format(time.RFC3339))
		}
		nabu := accountNamed(t, rows, "nabu-org")
		if nabu.Runnable == nil || *nabu.Runnable {
			t.Errorf("nabu-org: runnable = %v, want false", nabu.Runnable)
		}
		if nabu.RunnableReason != claudeprobe.ReasonNoLogin {
			t.Errorf("nabu-org: reason = %q, want the fixed no-login phrase", nabu.RunnableReason)
		}
		third := accountNamed(t, rows, "third-org")
		if third.Runnable != nil {
			t.Errorf("third-org: runnable = %v, want null (stored unknown is not 'not ready')", *third.Runnable)
		}
		if third.RunnableReason != claudeprobe.ReasonTimeout || third.RunnableCheckedAt == "" {
			t.Errorf("third-org: (reason, checkedAt) = (%q, %q), want the stored reason and a timestamp",
				third.RunnableReason, third.RunnableCheckedAt)
		}
	}
	assertRows(t, srv)

	// "Daemon restart": a fresh server (fresh Handler, fresh in-memory state)
	// over the same database.
	h2, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("second server: %v", err)
	}
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	assertRows(t, srv2)
}

// TestAccountsProbeEndpoint: POST /api/accounts/{account}/probe runs the probe
// for THAT account's config dir (empty for the default — absence selects it),
// persists the verdict with source='probe', and returns it.
func TestAccountsProbeEndpoint(t *testing.T) {
	home, _ := attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	db, srv := accountsTestDB(t, "accounts-probe.db")

	var dirs []string
	useProbe(t, func(_ context.Context, dir string) claudeprobe.Result {
		dirs = append(dirs, dir)
		if dir == "" {
			return claudeprobe.Result{Status: claudeprobe.StatusReady}
		}
		return claudeprobe.Result{Status: claudeprobe.StatusNoLogin, Reason: claudeprobe.ReasonNoLogin}
	})

	// The default account: probed with an EMPTY dir, verdict true.
	status, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts/default/probe", "")
	if status != http.StatusOK {
		t.Fatalf("POST probe (default) = %d\n%s", status, body)
	}
	var resp struct {
		Runnable          *bool  `json:"runnable"`
		RunnableReason    string `json:"runnableReason"`
		RunnableCheckedAt string `json:"runnableCheckedAt"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode probe response: %v\n%s", err, body)
	}
	if resp.Runnable == nil || !*resp.Runnable || resp.RunnableCheckedAt == "" {
		t.Errorf("default probe response = %+v, want runnable=true with a timestamp", resp)
	}

	// A named account: probed with exactly its dir, verdict false + reason.
	status, body = acctDo(t, http.MethodPost, srv.URL+"/api/accounts/nabu-org/probe", "")
	if status != http.StatusOK {
		t.Fatalf("POST probe (nabu-org) = %d\n%s", status, body)
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode probe response: %v\n%s", err, body)
	}
	if resp.Runnable == nil || *resp.Runnable || resp.RunnableReason != claudeprobe.ReasonNoLogin {
		t.Errorf("nabu-org probe response = %+v, want runnable=false with the no-login reason", resp)
	}

	want := []string{"", filepath.Join(home, ".claude-nabu-org")}
	if !slices.Equal(dirs, want) {
		t.Errorf("probed dirs = %v, want %v (absence, not the dir, selects the default)", dirs, want)
	}

	// Both verdicts landed in the store with source='probe'.
	for key, wantStatus := range map[string]string{"default": "ready", "nabu-org": "no-login"} {
		row, ok, err := store.GetAccountRunnable(db, key)
		if err != nil || !ok {
			t.Fatalf("stored verdict for %s: (%v, %v)", key, ok, err)
		}
		if row.Status != wantStatus || row.Source != "probe" {
			t.Errorf("%s stored = %+v, want status=%s source=probe", key, row, wantStatus)
		}
	}

	// An unknown account is refused before any probe runs.
	status, _ = acctDo(t, http.MethodPost, srv.URL+"/api/accounts/ghost/probe", "")
	if status != http.StatusNotFound {
		t.Errorf("POST probe (unknown) = %d, want 404", status)
	}
	if len(dirs) != 2 {
		t.Errorf("unknown-account probe ran the CLI: dirs = %v", dirs)
	}
}

// TestAccountsProbeIsSingleFlight: a second concurrent probe for the SAME
// account joins the in-flight one — one CLI invocation, one shared verdict.
func TestAccountsProbeIsSingleFlight(t *testing.T) {
	attachHomeAccounts(t, ingest.DefaultAccount, "nabu-org")
	_, srv := accountsTestDB(t, "accounts-probe-flight.db")

	entered := make(chan struct{}) // closed when the leader is inside the probe
	release := make(chan struct{}) // the test holds the leader here
	calls := useProbe(t, func(context.Context, string) claudeprobe.Result {
		close(entered)
		<-release
		return claudeprobe.Result{Status: claudeprobe.StatusReady}
	})

	do := func() chan string {
		out := make(chan string, 1)
		go func() {
			_, body := acctDo(t, http.MethodPost, srv.URL+"/api/accounts/nabu-org/probe", "")
			out <- body
		}()
		return out
	}
	first := do()
	<-entered // the leader is provably mid-probe…
	second := do()

	// …so the second request can only either join the flight or start a second
	// probe. A second probe would panic on the doubly-closed `entered` channel
	// and bump the counter; joining is the only clean path.
	time.Sleep(50 * time.Millisecond)
	close(release)

	bodyA, bodyB := <-first, <-second
	if n := calls.Load(); n != 1 {
		t.Errorf("probe ran %d times for two concurrent requests, want 1", n)
	}
	if bodyA != bodyB {
		t.Errorf("concurrent probes returned different bodies:\nA: %s\nB: %s", bodyA, bodyB)
	}
	if !strings.Contains(bodyA, `"runnable":true`) {
		t.Errorf("shared verdict body = %s, want runnable true", bodyA)
	}
}
