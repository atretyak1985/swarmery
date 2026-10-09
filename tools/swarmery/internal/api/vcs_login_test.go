package api

// Tests for the dashboard sign-in endpoints (vcs_login.go). The code host's
// OAuth endpoints are an httptest server behind deviceflow.Default, every
// `git`/`gh`/`glab` call is a scripted repoprovider.FakeExec, and the
// credential store lives in a temp SWARMERY_SECRETS_DIR — no test reaches a
// network, the operator's CLI login or ~/.swarmery.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/deviceflow"
)

// Secrets the tests watch for in every response body and in the log.
const (
	loginDeviceCode  = "devcode-SECRET-0123456789"
	loginGrantedTok  = "granted-device-token-abcdef0123456789" // no known token shape: masked only by value
	loginPastedTok   = "pasted-valid-token-abcdef0123456789"
	loginRejectedTok = "pasted-rejected-token-abcdef01234567"
	loginClientID    = "Iv1.testclientid"
)

var loginSecrets = []string{loginDeviceCode, loginGrantedTok, loginPastedTok, loginRejectedTok}

// syncBuffer is a log sink safe for the server goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeDeviceHost scripts a code host's device-flow endpoints for both
// providers: one start answer, then queued poll answers (pending once drained).
type fakeDeviceHost struct {
	mu     sync.Mutex
	start  string
	polls  []string
	paths  []string
	forms  []url.Values
	starts int
}

func (d *fakeDeviceHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.paths = append(d.paths, r.URL.Path)
	d.forms = append(d.forms, r.PostForm)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/login/device/code", "/oauth/authorize_device":
		d.starts++
		io.WriteString(w, d.start)
	case "/login/oauth/access_token", "/oauth/token":
		if len(d.polls) == 0 {
			io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		next := d.polls[0]
		d.polls = d.polls[1:]
		io.WriteString(w, next)
	default:
		http.NotFound(w, r)
	}
}

func (d *fakeDeviceHost) queue(bodies ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.polls = append(d.polls, bodies...)
}

func (d *fakeDeviceHost) lastForm() url.Values {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.forms) == 0 {
		return nil
	}
	return d.forms[len(d.forms)-1]
}

const githubStartBody = `{"device_code":"` + loginDeviceCode + `","user_code":"WDJB-MJHT",` +
	`"verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`

const gitlabStartBody = `{"device_code":"` + loginDeviceCode + `","user_code":"ABCD-EFGH",` +
	`"verification_uri":"https://gitlab.com/oauth/device",` +
	`"verification_uri_complete":"https://gitlab.com/oauth/device?user_code=ABCD-EFGH","expires_in":300,"interval":5}`

// vcsLoginFixture is a server whose project 1 lives in project, with the vcs
// process boundary, credential store, device host and log swapped for fakes.
type vcsLoginFixture struct {
	srv     string
	project string
	secrets string
	fake    *repoprovider.FakeExec
	host    *fakeDeviceHost
	logs    *syncBuffer
	bodies  []string // every response body read, for the never-echoed checks
}

// loginExec scripts a remote and a provider CLI that accepts exactly the
// granted and the pasted-valid tokens.
func loginExec(remote string) *repoprovider.FakeExec {
	valid := map[string]bool{loginGrantedTok: true, loginPastedTok: true}
	return &repoprovider.FakeExec{
		Out: map[string]string{"git remote": remote + "\n"},
		Errs: map[string]string{
			"gh auth":   "no oauth token found for github.com",
			"glab auth": "No token provided",
		},
		Fn: func(_ string, env []string, name string, args []string) (string, string, error, bool) {
			if len(args) == 0 || args[0] != "api" {
				return "", "", nil, false
			}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				if (k == credstore.GitHubTokenKey || k == credstore.GitLabTokenKey) && valid[v] {
					if name == "glab" {
						return `{"username":"tanuki"}`, "", nil, true
					}
					return `{"login":"octocat"}`, "", nil, true
				}
			}
			return "", "HTTP 401: Bad credentials", fmt.Errorf("exit status 1"), true
		},
	}
}

func newVcsLoginFixture(t *testing.T, remote string) *vcsLoginFixture {
	t.Helper()
	project := t.TempDir()
	srv, _ := reviewServer(t, project)

	secrets := t.TempDir()
	if err := os.Chmod(secrets, 0o700); err != nil { // t.TempDir is 0755 on macOS
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", secrets)
	t.Setenv("SWARMERY_GITHUB_CLIENT_ID", loginClientID)
	t.Setenv("SWARMERY_GITLAB_CLIENT_ID", loginClientID)

	fake := loginExec(remote)
	host := &fakeDeviceHost{start: githubStartBody}
	hs := httptest.NewServer(host)
	t.Cleanup(hs.Close)

	prevExec, prevProber, prevEnv, prevCache, prevLogins, prevDF :=
		vcsExec, vcsProber, vcsEnv, projectVcsCache, vcsLogins, deviceflow.Default
	vcsExec = fake
	vcsProber = vcsStubProber(false)
	vcsEnv = credstore.Env
	projectVcsCache = newVcsCache()
	vcsLogins = newVcsLoginStore()
	deviceflow.Default = &deviceflow.Client{
		HTTP:    hs.Client(),
		BaseURL: func(repoprovider.Kind, string) string { return hs.URL },
	}
	logs := &syncBuffer{}
	prevOut := log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		vcsExec, vcsProber, vcsEnv, projectVcsCache, vcsLogins, deviceflow.Default =
			prevExec, prevProber, prevEnv, prevCache, prevLogins, prevDF
	})
	return &vcsLoginFixture{srv: srv.URL, project: project, secrets: secrets, fake: fake, host: host, logs: logs}
}

// do sends a request and returns the status and decoded JSON body (nil for an
// empty body), recording the raw body.
func (f *vcsLoginFixture) do(t *testing.T, method, path, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, f.srv+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	f.bodies = append(f.bodies, string(raw))
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: body is not JSON: %q", method, path, raw)
		}
	}
	return resp.StatusCode, out
}

func (f *vcsLoginFixture) startDevice(t *testing.T) (int, map[string]any) {
	t.Helper()
	return f.do(t, http.MethodPost, "/api/projects/1/vcs/login", `{"method":"device"}`)
}

func (f *vcsLoginFixture) poll(t *testing.T, loginID string) (int, map[string]any) {
	t.Helper()
	return f.do(t, http.MethodGet, "/api/projects/1/vcs/login/"+loginID, "")
}

func (f *vcsLoginFixture) vcs(t *testing.T) vcsDTO {
	t.Helper()
	code, raw := f.do(t, http.MethodGet, "/api/projects/1/vcs?fresh=1", "")
	if code != http.StatusOK {
		t.Fatalf("GET /vcs = %d", code)
	}
	b, _ := json.Marshal(raw)
	var dto vcsDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		t.Fatal(err)
	}
	return dto
}

func (f *vcsLoginFixture) storePath(host string) string {
	return filepath.Join(f.secrets, "vcs-"+host+".env")
}

// assertNoSecrets: no response body and no log line carries a token or the
// device code.
func (f *vcsLoginFixture) assertNoSecrets(t *testing.T) {
	t.Helper()
	logs := f.logs.String()
	for _, s := range loginSecrets {
		for _, b := range f.bodies {
			if strings.Contains(b, s) {
				t.Errorf("a response body carries secret %q: %s", s, b)
			}
		}
		if strings.Contains(logs, s) {
			t.Errorf("the log carries secret %q:\n%s", s, logs)
		}
	}
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func num(m map[string]any, k string) int {
	n, _ := m[k].(float64)
	return int(n)
}

func TestVcsLoginDeviceHappyPath(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	code, start := f.startDevice(t)
	if code != http.StatusAccepted {
		t.Fatalf("start = %d %v, want 202", code, start)
	}
	loginID := str(start, "loginId")
	if len(loginID) != 32 || str(start, "userCode") != "WDJB-MJHT" ||
		str(start, "verificationUri") != "https://github.com/login/device" ||
		num(start, "expiresIn") != 900 || num(start, "interval") != 5 {
		t.Fatalf("start body = %v", start)
	}
	if _, ok := start["verificationUriComplete"]; ok {
		t.Errorf("verificationUriComplete present without the host sending one: %v", start)
	}
	form := f.host.lastForm()
	if form.Get("client_id") != loginClientID || form.Get("scope") != "repo" {
		t.Errorf("start form = %v", form)
	}

	f.host.queue(`{"error":"authorization_pending"}`,
		`{"access_token":"`+loginGrantedTok+`","token_type":"bearer","scope":"repo"}`)
	if code, body := f.poll(t, loginID); code != http.StatusOK || str(body, "status") != "pending" || num(body, "interval") != 5 {
		t.Fatalf("first poll = %d %v, want 200 pending/5", code, body)
	}
	if got := f.host.lastForm().Get("device_code"); got != loginDeviceCode {
		t.Errorf("poll sent device_code %q", got)
	}
	code, body := f.poll(t, loginID)
	if code != http.StatusOK || str(body, "status") != "ok" || str(body, "login") != "octocat" {
		t.Fatalf("second poll = %d %v, want 200 ok/octocat", code, body)
	}

	info, err := os.Stat(f.storePath("github.com"))
	if err != nil {
		t.Fatalf("store file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("store mode = %04o, want 0600", perm)
	}
	vals, err := credstore.Load("github.com")
	if err != nil || vals[credstore.GitHubTokenKey] != loginGrantedTok {
		t.Errorf("store = %v, %v", vals, err)
	}

	dto := f.vcs(t)
	want := vcsAuthDTO{Status: repoprovider.AuthOK, Login: "octocat", Source: repoprovider.SourceStore}
	if dto.Auth != want {
		t.Errorf("GET /vcs auth = %+v, want %+v", dto.Auth, want)
	}
	// The login is spent: polling it again is unknown.
	if code, body := f.poll(t, loginID); code != http.StatusNotFound || str(body, "code") != codeLoginNotFound {
		t.Errorf("poll after ok = %d %v, want 404 %s", code, body, codeLoginNotFound)
	}
	if !strings.Contains(f.logs.String(), "signed in to github.com via device flow") {
		t.Errorf("no sign-in log line:\n%s", f.logs.String())
	}
	f.assertNoSecrets(t)
}

// A GitLab project starts on /oauth/authorize_device with scope api, passes the
// complete verification URI through, and stores GITLAB_TOKEN.
func TestVcsLoginDeviceGitLab(t *testing.T) {
	f := newVcsLoginFixture(t, "https://gitlab.com/acme/widgets.git")
	f.host.start = gitlabStartBody
	code, start := f.startDevice(t)
	if code != http.StatusAccepted {
		t.Fatalf("start = %d %v", code, start)
	}
	if str(start, "verificationUriComplete") != "https://gitlab.com/oauth/device?user_code=ABCD-EFGH" {
		t.Errorf("start body = %v", start)
	}
	if f.host.paths[0] != "/oauth/authorize_device" || f.host.lastForm().Get("scope") != "api" {
		t.Errorf("start went to %v with %v", f.host.paths, f.host.lastForm())
	}
	f.host.queue(`{"access_token":"` + loginGrantedTok + `","token_type":"Bearer","refresh_token":"r-secret-refresh-0001","expires_in":7200}`)
	code, body := f.poll(t, str(start, "loginId"))
	if code != http.StatusOK || str(body, "status") != "ok" || str(body, "login") != "tanuki" {
		t.Fatalf("poll = %d %v", code, body)
	}
	vals, err := credstore.Load("gitlab.com")
	if err != nil || vals[credstore.GitLabTokenKey] != loginGrantedTok {
		t.Errorf("store = %v, %v", vals, err)
	}
	for _, b := range f.bodies {
		if strings.Contains(b, "r-secret-refresh-0001") {
			t.Errorf("refresh token echoed: %s", b)
		}
	}
	f.assertNoSecrets(t)
}

func TestVcsLoginDeviceSlowDownRaisesInterval(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	_, start := f.startDevice(t)
	loginID := str(start, "loginId")
	f.host.queue(`{"error":"slow_down"}`)
	code, body := f.poll(t, loginID)
	if code != http.StatusOK || str(body, "status") != "pending" || num(body, "interval") != 10 {
		t.Fatalf("slow_down poll = %d %v, want pending/10", code, body)
	}
	// The raised interval sticks for the next steps.
	code, body = f.poll(t, loginID)
	if code != http.StatusOK || str(body, "status") != "pending" || num(body, "interval") != 10 {
		t.Fatalf("next poll = %d %v, want pending/10", code, body)
	}
	f.assertNoSecrets(t)
}

func TestVcsLoginDeviceExpired(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	_, start := f.startDevice(t)
	loginID := str(start, "loginId")
	f.host.queue(`{"error":"expired_token"}`)
	if code, body := f.poll(t, loginID); code != http.StatusOK || str(body, "status") != "expired" {
		t.Fatalf("poll = %d %v, want expired", code, body)
	}
	if code, _ := f.poll(t, loginID); code != http.StatusNotFound {
		t.Errorf("poll after expired = %d, want 404", code)
	}
	if _, err := os.Stat(f.storePath("github.com")); !os.IsNotExist(err) {
		t.Errorf("store written on an expired flow: %v", err)
	}
	f.assertNoSecrets(t)
}

// A login whose device code ran out answers "expired" without asking the host,
// and the sweep forgets it after the grace.
func TestVcsLoginDeviceExpiredByClock(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	clock := &vcsFakeClock{t: time.Now()}
	vcsLogins.now = clock.now
	_, start := f.startDevice(t)
	loginID := str(start, "loginId")
	l := vcsLogins.get(1, loginID)
	vcsLogins.mu.Lock()
	l.pending.ExpiresAt = clock.now().Add(-time.Second)
	vcsLogins.mu.Unlock()
	polls := len(f.host.paths)
	if code, body := f.poll(t, loginID); code != http.StatusOK || str(body, "status") != "expired" {
		t.Fatalf("poll = %d %v, want expired", code, body)
	}
	if len(f.host.paths) != polls {
		t.Error("an expired login still called the host")
	}

	_, start = f.startDevice(t)
	clock.advance(time.Hour)
	if vcsLogins.get(1, str(start, "loginId")) != nil {
		t.Error("a login past its expiry + grace was not swept")
	}
}

func TestVcsLoginDeviceDenied(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	_, start := f.startDevice(t)
	f.host.queue(`{"error":"access_denied"}`)
	if code, body := f.poll(t, str(start, "loginId")); code != http.StatusOK || str(body, "status") != "denied" {
		t.Fatalf("poll = %d %v, want denied", code, body)
	}
	if _, err := os.Stat(f.storePath("github.com")); !os.IsNotExist(err) {
		t.Errorf("store written on a denied flow: %v", err)
	}
	f.assertNoSecrets(t)
}

func TestVcsLoginDeviceUnconfigured(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	t.Setenv("SWARMERY_GITHUB_CLIENT_ID", "")
	code, body := f.startDevice(t)
	if code != http.StatusConflict || str(body, "code") != codeDeviceFlowUnconfigured {
		t.Fatalf("start = %d %v, want 409 %s", code, body, codeDeviceFlowUnconfigured)
	}
	hint := str(body, "hint")
	for _, want := range []string{"SWARMERY_GITHUB_CLIENT_ID", "docs/vcs-login.md", "Token"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint lacks %q: %q", want, hint)
		}
	}
	if str(body, "envVar") != "SWARMERY_GITHUB_CLIENT_ID" {
		t.Errorf("envVar = %q", str(body, "envVar"))
	}
	if f.host.starts != 0 {
		t.Error("an unconfigured start still called the host")
	}

	// GitLab names its own env var.
	g := newVcsLoginFixture(t, "https://gitlab.com/acme/widgets.git")
	t.Setenv("SWARMERY_GITLAB_CLIENT_ID", "")
	if _, body := g.startDevice(t); !strings.Contains(str(body, "hint"), "SWARMERY_GITLAB_CLIENT_ID") {
		t.Errorf("gitlab hint = %q", str(body, "hint"))
	}
}

// swarmery.vcs.clientIds.<host> in settings.local.json wins over the env.
func TestVcsLoginDeviceClientIDFromSettingsLocal(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	t.Setenv("SWARMERY_GITHUB_CLIENT_ID", "")
	if err := os.MkdirAll(filepath.Join(f.project, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"swarmery":{"vcs":{"provider":"github","clientIds":{"GitHub.com":"Iv1.fromsettings"}}}}`
	if err := os.WriteFile(filepath.Join(f.project, ".claude", "settings.local.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := f.startDevice(t); code != http.StatusAccepted {
		t.Fatalf("start = %d %v", code, body)
	}
	if got := f.host.lastForm().Get("client_id"); got != "Iv1.fromsettings" {
		t.Errorf("client_id = %q, want the settings.local.json override", got)
	}
}

func TestVcsLoginDeviceFlowDisabled(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	f.host.start = `{"error":"device_flow_disabled","error_description":"Device Flow must be explicitly enabled for this App"}`
	code, body := f.startDevice(t)
	if code != http.StatusConflict || str(body, "code") != codeDeviceFlowDisabled {
		t.Fatalf("start = %d %v, want 409 %s", code, body, codeDeviceFlowDisabled)
	}
	if !strings.Contains(str(body, "hint"), "Enable Device Flow") {
		t.Errorf("hint = %q", str(body, "hint"))
	}
}

func TestVcsLoginUnknownOrForeignLoginIs404(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	if code, body := f.poll(t, "deadbeef"); code != http.StatusNotFound || str(body, "code") != codeLoginNotFound {
		t.Errorf("unknown id = %d %v", code, body)
	}
	// Another project's login is not this project's.
	id, err := vcsLogins.add(2, deviceflow.Pending{ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := f.poll(t, id); code != http.StatusNotFound {
		t.Errorf("foreign id = %d, want 404", code)
	}
}

func TestVcsLoginPendingCapPerProject(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	for i := range vcsLoginCapPerProject {
		if code, body := f.startDevice(t); code != http.StatusAccepted {
			t.Fatalf("start %d = %d %v", i, code, body)
		}
	}
	if code, body := f.startDevice(t); code != http.StatusTooManyRequests || str(body, "code") != codeTooManyLogins {
		t.Errorf("start past the cap = %d %v, want 429 %s", code, body, codeTooManyLogins)
	}
}

func TestVcsLoginRejectsBadMethodAndForeignOrigin(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	if code, _ := f.do(t, http.MethodPost, "/api/projects/1/vcs/login", `{"method":"password"}`); code != http.StatusBadRequest {
		t.Errorf("bad method = %d, want 400", code)
	}
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/projects/1/vcs/login", `{"method":"device"}`},
		{http.MethodGet, "/api/projects/1/vcs/login/abc", ""},
		{http.MethodDelete, "/api/projects/1/vcs/token", ""},
	} {
		if code, _ := f.do(t, tc.method, tc.path, tc.body, "Origin", "http://evil.example"); code != http.StatusForbidden {
			t.Errorf("%s %s cross-origin = %d, want 403", tc.method, tc.path, code)
		}
	}
	if f.host.starts != 0 {
		t.Error("a refused request reached the host")
	}
}

func TestVcsTokenValidStoredAndReportedOK(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	code, body := f.do(t, http.MethodPost, "/api/projects/1/vcs/login",
		`{"method":"token","token":"  `+loginPastedTok+`\t"}`)
	if code != http.StatusOK || str(body, "status") != "ok" || str(body, "login") != "octocat" {
		t.Fatalf("token submit = %d %v, want 200 ok/octocat", code, body)
	}
	info, err := os.Stat(f.storePath("github.com"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store file = %v, %v; want mode 0600", info, err)
	}
	if vals, _ := credstore.Load("github.com"); vals[credstore.GitHubTokenKey] != loginPastedTok {
		t.Errorf("stored token = %v", vals)
	}
	// The validating call ran on a throwaway config dir, not the store's.
	validated := false
	for i, c := range f.fake.Calls {
		if !strings.HasPrefix(c, "gh api user") {
			continue
		}
		env := strings.Join(f.fake.Envs[i], "\n")
		if strings.Contains(env, "GH_TOKEN="+loginPastedTok) && !strings.Contains(env, f.secrets) {
			validated = true
			break
		}
	}
	if !validated {
		t.Errorf("no validating gh call with the candidate token outside the secrets dir: %v", f.fake.Calls)
	}
	dto := f.vcs(t)
	if dto.Auth.Status != repoprovider.AuthOK || dto.Auth.Source != repoprovider.SourceStore || dto.Auth.Login != "octocat" {
		t.Errorf("GET /vcs auth = %+v, want ok/store/octocat", dto.Auth)
	}
	f.assertNoSecrets(t)
}

func TestVcsTokenInvalidIs422AndWritesNothing(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	code, body := f.do(t, http.MethodPost, "/api/projects/1/vcs/login",
		`{"method":"token","token":"`+loginRejectedTok+`"}`)
	if code != http.StatusUnprocessableEntity || str(body, "code") != codeNotAuthenticated {
		t.Fatalf("token submit = %d %v, want 422 %s", code, body, codeNotAuthenticated)
	}
	entries, err := os.ReadDir(f.secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "vcs-") && strings.HasSuffix(e.Name(), ".env") {
			t.Errorf("a rejected token was stored: %s", e.Name())
		}
	}
	if code, _ := f.do(t, http.MethodPost, "/api/projects/1/vcs/login", `{"method":"token","token":""}`); code != http.StatusBadRequest {
		t.Errorf("empty token = %d, want 400", code)
	}
	f.assertNoSecrets(t)
}

// A token for a GitLab project validates through glab and lands as GITLAB_TOKEN.
func TestVcsTokenGitLab(t *testing.T) {
	f := newVcsLoginFixture(t, "https://gitlab.com/acme/widgets.git")
	code, body := f.do(t, http.MethodPost, "/api/projects/1/vcs/login",
		`{"method":"token","token":"`+loginPastedTok+`"}`)
	if code != http.StatusOK || str(body, "login") != "tanuki" {
		t.Fatalf("token submit = %d %v", code, body)
	}
	if vals, _ := credstore.Load("gitlab.com"); vals[credstore.GitLabTokenKey] != loginPastedTok {
		t.Errorf("stored = %v", vals)
	}
	f.assertNoSecrets(t)
}

func TestVcsTokenDeleteRemovesStore(t *testing.T) {
	f := newVcsLoginFixture(t, "https://github.com/acme/widgets.git")
	if code, body := f.do(t, http.MethodPost, "/api/projects/1/vcs/login",
		`{"method":"token","token":"`+loginPastedTok+`"}`); code != http.StatusOK {
		t.Fatalf("token submit = %d %v", code, body)
	}
	if dto := f.vcs(t); dto.Auth.Status != repoprovider.AuthOK {
		t.Fatalf("before delete auth = %+v", dto.Auth)
	}
	if code, _ := f.do(t, http.MethodDelete, "/api/projects/1/vcs/token", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204", code)
	}
	if _, err := os.Stat(f.storePath("github.com")); !os.IsNotExist(err) {
		t.Errorf("store file still present: %v", err)
	}
	// No fresh=1 here: the DELETE itself dropped the cached answer.
	code, raw := f.do(t, http.MethodGet, "/api/projects/1/vcs", "")
	auth, _ := raw["auth"].(map[string]any)
	if code != http.StatusOK || str(auth, "status") != repoprovider.AuthMissing {
		t.Errorf("GET /vcs after delete = %d %v, want auth missing", code, raw)
	}
	// Deleting again is idempotent.
	if code, _ := f.do(t, http.MethodDelete, "/api/projects/1/vcs/token", ""); code != http.StatusNoContent {
		t.Errorf("second DELETE = %d, want 204", code)
	}
	if !strings.Contains(f.logs.String(), "removed the stored token for github.com") {
		t.Errorf("no removal log line:\n%s", f.logs.String())
	}
	f.assertNoSecrets(t)
}

// An origin on a host no provider is known for has nothing to sign in to.
func TestVcsLoginUnknownProviderIs422(t *testing.T) {
	f := newVcsLoginFixture(t, "https://git.example.com/acme/widgets.git")
	code, body := f.startDevice(t)
	if code != http.StatusUnprocessableEntity || str(body, "code") != codeProviderUnknown {
		t.Errorf("start = %d %v, want 422 %s", code, body, codeProviderUnknown)
	}
}
