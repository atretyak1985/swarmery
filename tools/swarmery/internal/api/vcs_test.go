package api

// Tests for GET /api/projects/{id}/vcs (vcs.go). Every `git`/`gh` call goes
// through one scripted repoprovider.FakeExec, the GitLab prober is a stub, and
// the credential env is nil — no test reaches a network, the operator's gh
// login or the daemon's credential store.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// vcsTestToken is a token shape credstore.Redact masks.
const vcsTestToken = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// vcsStubProber answers IsGitLab with a fixed value.
type vcsStubProber bool

func (p vcsStubProber) IsGitLab(context.Context, string) bool { return bool(p) }

// vcsFakeClock is the cache's injectable clock.
type vcsFakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *vcsFakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *vcsFakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// vcsFixture is a server whose project 1 lives in a temp dir, with the vcs
// endpoint's process boundary, prober, env and cache swapped for fakes.
type vcsFixture struct {
	srv   string
	fake  *repoprovider.FakeExec
	clock *vcsFakeClock
}

func newVcsFixture(t *testing.T, fake *repoprovider.FakeExec) (*vcsFixture, func(query string, args ...any)) {
	t.Helper()
	srv, db := reviewServer(t, t.TempDir())
	prevExec, prevProber, prevEnv, prevCache := vcsExec, vcsProber, vcsEnv, projectVcsCache
	clock := &vcsFakeClock{t: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	vcsExec = fake
	vcsProber = vcsStubProber(false)
	vcsEnv = func(string) []string { return nil }
	projectVcsCache = newVcsCache()
	projectVcsCache.now = clock.now
	t.Cleanup(func() {
		vcsExec, vcsProber, vcsEnv, projectVcsCache = prevExec, prevProber, prevEnv, prevCache
	})
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	return &vcsFixture{srv: srv.URL, fake: fake, clock: clock}, exec
}

// get calls the endpoint for project 1 and decodes both the typed DTO and the
// raw object (for key-shape assertions).
func (f *vcsFixture) get(t *testing.T) (vcsDTO, map[string]any) {
	t.Helper()
	return f.getQuery(t, "")
}

// getQuery is get with a raw query string ("fresh=1").
func (f *vcsFixture) getQuery(t *testing.T, query string) (vcsDTO, map[string]any) {
	t.Helper()
	url := fmt.Sprintf("%s/api/projects/1/vcs", f.srv)
	if query != "" {
		url += "?" + query
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(raw)
	var dto vcsDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		t.Fatal(err)
	}
	return dto, raw
}

func (f *vcsFixture) calls() int {
	return len(f.fake.Calls)
}

// githubCLIOK is a fake for a github.com remote and a working gh CLI login.
func githubCLIOK(remote string) *repoprovider.FakeExec {
	return &repoprovider.FakeExec{Out: map[string]string{
		"git remote": remote + "\n",
		"gh auth":    "gho_cli_token\n",
		"gh api":     `{"login":"octocat"}`,
	}}
}

func TestProjectVcsGitHubOKViaCLI(t *testing.T) {
	f, _ := newVcsFixture(t, githubCLIOK("https://github.com/acme/widgets.git"))
	dto, raw := f.get(t)

	for _, k := range []string{"provider", "host", "terms", "remote", "auth", "baseBranch", "allowPushToBase", "source", "cliLogin"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("response lacks key %q: %v", k, raw)
		}
	}
	if dto.Provider != repoprovider.KindGitHub || dto.Host != "github.com" || dto.Source != repoprovider.SourceHost {
		t.Errorf("provider/host/source = %q/%q/%q", dto.Provider, dto.Host, dto.Source)
	}
	if dto.Terms != repoprovider.TermsFor(repoprovider.KindGitHub) {
		t.Errorf("terms = %+v", dto.Terms)
	}
	if !dto.Remote.Present || dto.Remote.Protocol != "https" || dto.Remote.URL != "https://github.com/acme/widgets.git" {
		t.Errorf("remote = %+v", dto.Remote)
	}
	want := vcsAuthDTO{Status: repoprovider.AuthOK, Login: "octocat", Source: repoprovider.SourceCLI}
	if dto.Auth != want {
		t.Errorf("auth = %+v, want %+v", dto.Auth, want)
	}
}

func TestProjectVcsGitHubMissing(t *testing.T) {
	fake := &repoprovider.FakeExec{
		Out:  map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Errs: map[string]string{"gh auth": "no oauth token found for github.com"},
	}
	f, _ := newVcsFixture(t, fake)
	dto, _ := f.get(t)
	want := vcsAuthDTO{Status: repoprovider.AuthMissing, Source: repoprovider.SourceNone}
	if dto.Auth != want {
		t.Errorf("auth = %+v, want %+v", dto.Auth, want)
	}
	if fake.Ran("gh api") {
		t.Error("probed `gh api user` without any token")
	}
}

func TestProjectVcsSSHRemoteWithoutTokenIsMissing(t *testing.T) {
	fake := &repoprovider.FakeExec{
		Out:  map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
		Errs: map[string]string{"gh auth": "no oauth token found for github.com"},
	}
	f, _ := newVcsFixture(t, fake)
	dto, _ := f.get(t)
	if dto.Auth.Status != repoprovider.AuthMissing {
		t.Errorf("auth.status = %q, want missing", dto.Auth.Status)
	}
	if dto.Remote.Protocol != "ssh" || !dto.Remote.Present {
		t.Errorf("remote = %+v, want present ssh", dto.Remote)
	}
	if dto.Provider != repoprovider.KindGitHub || dto.Host != "github.com" {
		t.Errorf("provider/host = %q/%q", dto.Provider, dto.Host)
	}
}

func TestProjectVcsStripsRemoteCredentials(t *testing.T) {
	f, _ := newVcsFixture(t, githubCLIOK("https://x-access-token:"+vcsTestToken+"@github.com/acme/widgets.git"))
	dto, _ := f.get(t)
	if strings.Contains(dto.Remote.URL, vcsTestToken) || strings.Contains(dto.Remote.URL, "x-access-token") {
		t.Errorf("remote.url leaks credentials: %q", dto.Remote.URL)
	}
	if dto.Remote.URL != "https://github.com/acme/widgets.git" {
		t.Errorf("remote.url = %q", dto.Remote.URL)
	}
}

func TestProjectVcsCachedFor60s(t *testing.T) {
	f, _ := newVcsFixture(t, githubCLIOK("https://github.com/acme/widgets.git"))
	f.get(t)
	first := f.calls()
	if first == 0 {
		t.Fatal("the first call executed nothing")
	}

	f.clock.advance(59 * time.Second)
	f.get(t)
	if got := f.calls(); got != first {
		t.Fatalf("second call within 60s re-executed: %d calls, want %d (%v)", got, first, f.fake.Calls)
	}

	InvalidateVcsCache(1)
	f.get(t)
	afterInvalidate := f.calls()
	if afterInvalidate <= first {
		t.Fatalf("call after InvalidateVcsCache did not re-execute (%d calls)", afterInvalidate)
	}

	f.clock.advance(61 * time.Second)
	f.get(t)
	if got := f.calls(); got <= afterInvalidate {
		t.Fatalf("call after the TTL did not re-execute (%d calls)", got)
	}
}

// ?fresh=1 (the banner's Re-check) re-probes inside the TTL and refreshes the
// cache for the reads that follow.
func TestProjectVcsFreshBypassesCache(t *testing.T) {
	f, _ := newVcsFixture(t, githubCLIOK("https://github.com/acme/widgets.git"))
	f.get(t)
	first := f.calls()

	f.clock.advance(5 * time.Second)
	f.getQuery(t, "fresh=1")
	afterFresh := f.calls()
	if afterFresh <= first {
		t.Fatalf("fresh=1 within the TTL did not re-execute (%d calls, want > %d)", afterFresh, first)
	}

	f.get(t)
	if got := f.calls(); got != afterFresh {
		t.Errorf("a plain read after fresh=1 re-executed: %d calls, want %d", got, afterFresh)
	}
}

// Concurrent uncached reads of one project run ONE probe: the banner and the
// Plans page open together on a cold cache, and a Re-check (?fresh=1) landing
// mid-probe joins it instead of starting a second one.
func TestProjectVcsConcurrentColdReadsShareOneProbe(t *testing.T) {
	fake := githubCLIOK("https://github.com/acme/widgets.git")
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var remoteRuns atomic.Int32
	fake.Fn = func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
		if repoprovider.FakeKey(name, args) == "git remote" {
			remoteRuns.Add(1)
			once.Do(func() { close(entered) })
			<-release
		}
		return "", "", nil, false
	}
	f, _ := newVcsFixture(t, fake)
	joined := make(chan int64, 4)
	projectVcsCache.onJoin = func(id int64) { joined <- id }

	// Each read runs on its own goroutine, so it reports instead of failing.
	type result struct {
		dto vcsDTO
		err error
	}
	results := make(chan result, 3)
	read := func(query string) {
		url := f.srv + "/api/projects/1/vcs"
		if query != "" {
			url += "?" + query
		}
		resp, err := http.Get(url)
		if err != nil {
			results <- result{err: err}
			return
		}
		defer resp.Body.Close()
		var dto vcsDTO
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			err = json.NewDecoder(resp.Body).Decode(&dto)
		}
		results <- result{dto: dto, err: err}
	}
	go read("")
	<-entered // the first request owns the probe and is blocked inside it
	go read("")
	go read("fresh=1")
	for range 2 {
		select {
		case <-joined:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("a concurrent read did not join the in-flight probe")
		}
	}
	close(release)

	for range 3 {
		select {
		case r := <-results:
			if r.err != nil {
				t.Errorf("read: %v", r.err)
			} else if r.dto.Auth.Status != repoprovider.AuthOK || r.dto.Auth.Login != "octocat" {
				t.Errorf("auth = %+v, want ok/octocat", r.dto.Auth)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a read never returned")
		}
	}
	if n := remoteRuns.Load(); n != 1 {
		t.Errorf("the remote was read %d times across 3 concurrent reads, want 1 probe", n)
	}
}

// cliLogin names the provider's own CLI for the remote's host, and nothing
// for a host no CLI is driven for.
func TestProjectVcsCliLoginPerProvider(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		gitlab bool
		want   string
	}{
		{"github https", "https://github.com/acme/widgets.git", false, "gh auth login --hostname github.com"},
		{"github ssh", "git@github.com:acme/widgets.git", false, "gh auth login --hostname github.com"},
		{"gitlab", "https://gitlab.com/acme/widgets.git", false, "glab auth login --hostname gitlab.com"},
		{"self-hosted gitlab by probe", "https://git.example.com/acme/widgets.git", true, "glab auth login --hostname git.example.com"},
		{"unknown host", "https://git.example.com/acme/widgets.git", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := newVcsFixture(t, &repoprovider.FakeExec{Out: map[string]string{"git remote": tc.remote + "\n"}})
			vcsProber = vcsStubProber(tc.gitlab)
			dto, _ := f.get(t)
			if dto.CliLogin != tc.want {
				t.Errorf("cliLogin = %q, want %q (provider %q)", dto.CliLogin, tc.want, dto.Provider)
			}
		})
	}
}

func TestProjectVcsMarkAuthExpiredOverridesCachedOK(t *testing.T) {
	f, _ := newVcsFixture(t, githubCLIOK("https://github.com/acme/widgets.git"))
	if dto, _ := f.get(t); dto.Auth.Status != repoprovider.AuthOK {
		t.Fatalf("auth.status = %q, want ok", dto.Auth.Status)
	}
	calls := f.calls()

	f.clock.advance(time.Second)
	MarkVcsAuthExpired(1)
	dto, _ := f.get(t)
	if dto.Auth.Status != repoprovider.AuthExpired {
		t.Errorf("auth.status after MarkVcsAuthExpired = %q, want expired", dto.Auth.Status)
	}
	if f.calls() != calls {
		t.Error("MarkVcsAuthExpired forced a re-probe; it should overlay the cached answer")
	}

	// A probe that runs AFTER the mark and answers ok wins (the operator signed
	// in again).
	f.clock.advance(time.Second)
	InvalidateVcsCache(1)
	if dto, _ := f.get(t); dto.Auth.Status != repoprovider.AuthOK {
		t.Errorf("auth.status after a fresh ok probe = %q, want ok", dto.Auth.Status)
	}
}

func TestProjectVcsMarkAuthExpiredKeepsMissing(t *testing.T) {
	fake := &repoprovider.FakeExec{
		Out:  map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Errs: map[string]string{"gh auth": "no oauth token found"},
	}
	f, _ := newVcsFixture(t, fake)
	f.get(t)
	f.clock.advance(time.Second)
	MarkVcsAuthExpired(1)
	if dto, _ := f.get(t); dto.Auth.Status != repoprovider.AuthMissing {
		t.Errorf("auth.status = %q, want missing (no login at all is the more precise answer)", dto.Auth.Status)
	}
}

func TestProjectVcsLandingErrorUpgradesUnknownToExpired(t *testing.T) {
	fake := &repoprovider.FakeExec{
		Out:     map[string]string{"git remote": "https://github.com/acme/widgets.git\n"},
		Missing: map[string]bool{"gh": true},
	}
	f, exec := newVcsFixture(t, fake)
	if dto, _ := f.get(t); dto.Auth.Status != repoprovider.AuthUnknown {
		t.Fatalf("auth.status without a landing error = %q, want unknown", dto.Auth.Status)
	}

	exec(`INSERT INTO tasks (id, project_id, title, prompt, status, created_at, started_at, source, external_id)
		VALUES (50, 1, 'Plan', 'goal', 'running', '2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z',
		'workspace', '2026-10-09-plan')`)
	exec(`INSERT INTO epic_phases (id, workspace_task_id, seq, name, doc_path, depends_on,
		checkboxes_total, checkboxes_done, run_state, landing_error)
		VALUES (501, 50, 1, 'Phase 1', '/ws/plan/phase-1.md', '[]', 1, 1, 'done',
		'not-authenticated: HTTP 401: Bad credentials')`)
	InvalidateVcsCache(1)
	if dto, _ := f.get(t); dto.Auth.Status != repoprovider.AuthExpired {
		t.Errorf("auth.status with a not-authenticated landing error = %q, want expired", dto.Auth.Status)
	}
}

func TestProjectVcsUnknownProvider(t *testing.T) {
	fake := &repoprovider.FakeExec{Out: map[string]string{
		"git remote": "https://git.example.com/acme/widgets.git\n",
	}}
	f, _ := newVcsFixture(t, fake)
	dto, _ := f.get(t)
	if dto.Provider != repoprovider.KindUnknown || dto.Source != repoprovider.SourceUnknown {
		t.Errorf("provider/source = %q/%q, want unknown/unknown", dto.Provider, dto.Source)
	}
	if dto.Terms.Provider == "" || dto.Terms.Change == "" || dto.Terms.ChangeShort == "" {
		t.Errorf("terms has an empty label: %+v", dto.Terms)
	}
	if dto.Auth.Status != repoprovider.AuthUnknown || dto.Auth.Source != repoprovider.SourceNone {
		t.Errorf("auth = %+v, want unknown/none", dto.Auth)
	}
	if !dto.Remote.Present || dto.Host != "git.example.com" {
		t.Errorf("remote/host = %+v/%q", dto.Remote, dto.Host)
	}
	if fake.Ran("gh ") || fake.Ran("glab ") {
		t.Errorf("an unknown provider ran a provider CLI: %v", fake.Calls)
	}
}

func TestProjectVcsNoRemote(t *testing.T) {
	fake := &repoprovider.FakeExec{Errs: map[string]string{
		"git remote": "error: No such remote 'origin'",
	}}
	f, _ := newVcsFixture(t, fake)
	dto, _ := f.get(t)
	if dto.Remote.Present || dto.Remote.URL != "" || dto.Remote.Protocol != "" {
		t.Errorf("remote = %+v, want absent", dto.Remote)
	}
	if dto.Provider != repoprovider.KindUnknown || dto.Terms.Change == "" {
		t.Errorf("provider/terms = %q/%+v", dto.Provider, dto.Terms)
	}
	if dto.Auth.Status != repoprovider.AuthUnknown {
		t.Errorf("auth.status = %q, want unknown", dto.Auth.Status)
	}
}

func TestProjectVcsUnknownProjectIs404(t *testing.T) {
	f, _ := newVcsFixture(t, &repoprovider.FakeExec{})
	resp, err := http.Get(f.srv + "/api/projects/999/vcs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
