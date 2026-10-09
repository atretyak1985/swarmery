package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

// Every test drives a repoprovider.FakeExec: no real gh, git or network.

var target = repoprovider.Target{
	RepoDir:    "/repo",
	RemoteName: "origin",
	Remote:     repoprovider.Remote{Host: "github.com", Owner: "acme", Repo: "widgets", Protocol: "ssh"},
}

func storeEnv(host string) []string {
	return []string{"GH_TOKEN=ghp_storestorestorestorestorestore00", "GH_CONFIG_DIR=/secrets/vcs-gh-config"}
}

func noStoreEnv(host string) []string {
	return []string{"GH_CONFIG_DIR=/secrets/vcs-gh-config"}
}

func TestKindAndTerms(t *testing.T) {
	p := New(&repoprovider.FakeExec{}, nil)
	if p.Kind() != repoprovider.KindGitHub {
		t.Fatalf("Kind = %q", p.Kind())
	}
	terms := p.Terms()
	if terms != (repoprovider.Terms{Provider: "GitHub", Change: "Pull Request", ChangeShort: "PR"}) {
		t.Fatalf("Terms = %+v", terms)
	}
	if terms.Provider == "" || terms.Change == "" || terms.ChangeShort == "" {
		t.Fatal("empty Terms value")
	}
}

func TestPushNeverForces(t *testing.T) {
	f := &repoprovider.FakeExec{}
	p := New(f, storeEnv)
	if err := p.Push(context.Background(), target, "swarm/phase-7"); err != nil {
		t.Fatal(err)
	}
	if got := f.Calls[0]; got != "git push -u origin swarm/phase-7" {
		t.Fatalf("call = %q", got)
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "--force") || strings.Contains(c, "-f ") || strings.Contains(c, "+swarm") {
			t.Fatalf("push forced: %q", c)
		}
	}
	if f.Dirs[0] != "/repo" || !strings.HasPrefix(f.Envs[0][0], "GH_TOKEN=") {
		t.Fatalf("push dir/env: %q %v", f.Dirs[0], f.Envs[0])
	}
	// Empty remote name defaults to origin.
	f2 := &repoprovider.FakeExec{}
	if err := New(f2, nil).Push(context.Background(), repoprovider.Target{RepoDir: "/r"}, "b"); err != nil {
		t.Fatal(err)
	}
	if f2.Calls[0] != "git push -u origin b" {
		t.Fatalf("default remote: %q", f2.Calls[0])
	}
}

func TestPushRefusesSmuggledRefspecs(t *testing.T) {
	for _, b := range []string{"", "+main", "--force", "-f", "a:main", "a b"} {
		f := &repoprovider.FakeExec{}
		err := New(f, nil).Push(context.Background(), target, b)
		if !errors.Is(err, ErrInvalidRef) {
			t.Errorf("Push(%q) = %v, want ErrInvalidRef", b, err)
		}
		if len(f.Calls) != 0 {
			t.Errorf("Push(%q) ran git: %v", b, f.Calls)
		}
	}
	bad := target
	bad.RemoteName = "--mirror"
	if err := New(&repoprovider.FakeExec{}, nil).Push(context.Background(), bad, "b"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("bad remote: %v", err)
	}
}

func TestPushFailuresClassified(t *testing.T) {
	cases := map[string]error{
		" ! [rejected]  b -> b (non-fast-forward)":                         repoprovider.ErrRemoteDiverged,
		"remote: Permission to acme/widgets.git denied to bot.":            repoprovider.ErrNoPushAccess,
		"fatal: Authentication failed for 'https://github.com/acme/w.git'": repoprovider.ErrNotAuthenticated,
		"fatal: 'origin' does not appear to be a git repository":           repoprovider.ErrNoRemote,
	}
	for stderr, want := range cases {
		f := &repoprovider.FakeExec{Errs: map[string]string{"git push": stderr}}
		err := New(f, nil).Push(context.Background(), target, "b")
		if !errors.Is(err, want) {
			t.Errorf("stderr %q: err = %v, want %v", stderr, err, want)
		}
	}
	f := &repoprovider.FakeExec{Missing: map[string]bool{"git": true}}
	if err := New(f, nil).Push(context.Background(), target, "b"); !errors.Is(err, repoprovider.ErrBinaryMissing) {
		t.Fatalf("missing git: %v", err)
	}
}

func TestPushFailureRedactsToken(t *testing.T) {
	tok := "ghp_" + strings.Repeat("Q", 36)
	f := &repoprovider.FakeExec{Errs: map[string]string{"git push": "fatal: could not read Username for 'https://" + tok + "@github.com'"}}
	err := New(f, nil).Push(context.Background(), target, "b")
	if err == nil || strings.Contains(err.Error(), tok) || !strings.Contains(err.Error(), "***") {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestOpenChangeRequest(t *testing.T) {
	for _, draft := range []bool{false, true} {
		f := &repoprovider.FakeExec{Out: map[string]string{
			"gh pr": "Creating pull request for swarm/phase-7 into main in acme/widgets\n\nhttps://github.com/acme/widgets/pull/42\n",
		}}
		ref, err := New(f, storeEnv).OpenChangeRequest(context.Background(), target, repoprovider.ChangeRequest{
			Head: "swarm/phase-7", Base: "main", Title: "Phase 7", Body: "body text", Draft: draft,
		})
		if err != nil {
			t.Fatal(err)
		}
		if ref != (repoprovider.ChangeRef{URL: "https://github.com/acme/widgets/pull/42", Number: 42, Provider: repoprovider.KindGitHub}) {
			t.Fatalf("ref = %+v", ref)
		}
		want := "gh pr create --head swarm/phase-7 --title Phase 7 --body body text --base main --repo acme/widgets"
		if draft {
			want += " --draft"
		}
		if f.Calls[0] != want {
			t.Fatalf("draft=%v call = %q\nwant %q", draft, f.Calls[0], want)
		}
		if f.Ran("--draft") != draft {
			t.Fatalf("draft=%v but --draft presence = %v", draft, f.Ran("--draft"))
		}
		if !f.Ran("gh pr create --head swarm/phase-7") {
			t.Fatal("board-land prefix contract broken")
		}
	}
}

func TestOpenChangeRequestURLOnStderrAndEnterprise(t *testing.T) {
	f := &repoprovider.FakeExec{Fn: func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
		return "", "https://ghe.corp/team/app/pull/7\n", nil, true
	}}
	tgt := repoprovider.Target{RepoDir: "/r", Remote: repoprovider.Remote{Host: "ghe.corp", Owner: "team", Repo: "app"}}
	ref, err := New(f, nil).OpenChangeRequest(context.Background(), tgt, repoprovider.ChangeRequest{Head: "b", Title: "t"})
	if err != nil || ref.Number != 7 {
		t.Fatalf("ref=%+v err=%v", ref, err)
	}
	if !f.Ran("--repo ghe.corp/team/app") || f.Ran("--base") {
		t.Fatalf("enterprise args: %v", f.Calls)
	}
	// Unparsed remote: no --repo at all.
	f2 := &repoprovider.FakeExec{Out: map[string]string{"gh pr": "https://github.com/a/b/pull/1"}}
	if _, err := New(f2, nil).OpenChangeRequest(context.Background(), repoprovider.Target{RepoDir: "/r"}, repoprovider.ChangeRequest{Head: "b"}); err != nil {
		t.Fatal(err)
	}
	if f2.Ran("--repo") {
		t.Fatalf("--repo without a parsed remote: %v", f2.Calls)
	}
}

func TestOpenChangeRequestFailures(t *testing.T) {
	ctx := context.Background()
	req := repoprovider.ChangeRequest{Head: "b", Title: "t"}

	missing := &repoprovider.FakeExec{Missing: map[string]bool{"gh": true}}
	_, err := New(missing, nil).OpenChangeRequest(ctx, target, req)
	if !errors.Is(err, repoprovider.ErrBinaryMissing) || len(missing.Calls) != 0 {
		t.Fatalf("missing gh: %v %v", err, missing.Calls)
	}

	notAuth := &repoprovider.FakeExec{Errs: map[string]string{"gh pr": "To get started with GitHub CLI, please run:  gh auth login"}}
	if _, err := New(notAuth, nil).OpenChangeRequest(ctx, target, req); !errors.Is(err, repoprovider.ErrNotAuthenticated) {
		t.Fatalf("not authenticated: %v", err)
	}

	forbidden := &repoprovider.FakeExec{Errs: map[string]string{"gh pr": "HTTP 403: Resource not accessible by integration"}}
	if _, err := New(forbidden, nil).OpenChangeRequest(ctx, target, req); !errors.Is(err, repoprovider.ErrNoPushAccess) {
		t.Fatalf("no access: %v", err)
	}

	tok := "github_pat_" + strings.Repeat("x", 40)
	noURL := &repoprovider.FakeExec{Out: map[string]string{"gh pr": "done, token " + tok}}
	_, err = New(noURL, nil).OpenChangeRequest(ctx, target, req)
	if !errors.Is(err, ErrNoURL) || strings.Contains(err.Error(), tok) {
		t.Fatalf("no URL: %v", err)
	}

	for _, bad := range []repoprovider.ChangeRequest{{Head: ""}, {Head: "-x"}, {Head: "b", Base: "+main"}} {
		f := &repoprovider.FakeExec{}
		if _, err := New(f, nil).OpenChangeRequest(ctx, target, bad); !errors.Is(err, ErrInvalidRef) || len(f.Calls) != 0 {
			t.Errorf("req %+v: %v %v", bad, err, f.Calls)
		}
	}
}

func TestPRNumber(t *testing.T) {
	cases := map[string]int{
		"https://github.com/a/b/pull/12":          12,
		"https://github.com/a/b/pull/12/files":    12,
		"https://github.com/a/b/pull/12#issue":    12,
		"https://github.com/a/b/pull/12?x=1":      12,
		"https://github.com/a/b/pull/abc":         0,
		"https://github.com/a/b/compare/main...b": 0,
	}
	for in, want := range cases {
		if got := prNumber(in); got != want {
			t.Errorf("prNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestStatusParsing(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		json string
		want repoprovider.ChangeStatus
	}{
		{"open draft no checks", `{"state":"OPEN","isDraft":true,"mergedAt":"","reviewDecision":"","statusCheckRollup":[],"url":"u"}`,
			repoprovider.ChangeStatus{State: "open", Draft: true, CI: "none", Review: "none"}},
		{"open passing approved", `{"state":"OPEN","isDraft":false,"reviewDecision":"APPROVED","statusCheckRollup":[
			{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"},
			{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SKIPPED"},
			{"__typename":"StatusContext","state":"SUCCESS"}]}`,
			repoprovider.ChangeStatus{State: "open", CI: "passing", Review: "approved"}},
		{"pending run", `{"state":"OPEN","reviewDecision":"REVIEW_REQUIRED","statusCheckRollup":[
			{"__typename":"CheckRun","status":"IN_PROGRESS","conclusion":""},
			{"__typename":"CheckRun","status":"COMPLETED","conclusion":"SUCCESS"}]}`,
			repoprovider.ChangeStatus{State: "open", CI: "pending", Review: "review_required"}},
		{"pending context", `{"state":"OPEN","statusCheckRollup":[{"state":"PENDING"}]}`,
			repoprovider.ChangeStatus{State: "open", CI: "pending", Review: "none"}},
		{"completed no conclusion", `{"state":"OPEN","statusCheckRollup":[{"__typename":"CheckRun","status":"COMPLETED"}]}`,
			repoprovider.ChangeStatus{State: "open", CI: "pending", Review: "none"}},
		{"failing beats pending", `{"state":"OPEN","reviewDecision":"CHANGES_REQUESTED","statusCheckRollup":[
			{"__typename":"CheckRun","status":"IN_PROGRESS"},
			{"__typename":"CheckRun","status":"COMPLETED","conclusion":"TIMED_OUT"}]}`,
			repoprovider.ChangeStatus{State: "open", CI: "failing", Review: "changes_requested"}},
		{"failing context", `{"state":"OPEN","statusCheckRollup":[{"__typename":"StatusContext","state":"ERROR"}]}`,
			repoprovider.ChangeStatus{State: "open", CI: "failing", Review: "none"}},
		{"merged", `{"state":"MERGED","mergedAt":"2026-10-09T10:00:00Z","statusCheckRollup":null}`,
			repoprovider.ChangeStatus{State: "merged", CI: "none", Review: "none"}},
		{"merged by state", `{"state":"MERGED"}`, repoprovider.ChangeStatus{State: "merged", CI: "none", Review: "none"}},
		{"closed", `{"state":"CLOSED"}`, repoprovider.ChangeStatus{State: "closed", CI: "none", Review: "none"}},
	}
	for _, c := range cases {
		f := &repoprovider.FakeExec{Out: map[string]string{"gh pr": c.json}}
		p := New(f, storeEnv)
		p.now = func() time.Time { return now }
		got, err := p.Status(context.Background(), target, repoprovider.ChangeRef{Number: 42})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		c.want.CheckedAt = now
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
		want := "gh pr view 42 --repo acme/widgets --json " + statusFields
		if f.Calls[0] != want {
			t.Errorf("%s: call = %q", c.name, f.Calls[0])
		}
	}
}

func TestStatusByURLAndFailures(t *testing.T) {
	ctx := context.Background()
	f := &repoprovider.FakeExec{Out: map[string]string{"gh pr": `{"state":"OPEN"}`}}
	if _, err := New(f, nil).Status(ctx, repoprovider.Target{}, repoprovider.ChangeRef{URL: "https://github.com/a/b/pull/3"}); err != nil {
		t.Fatal(err)
	}
	if f.Calls[0] != "gh pr view https://github.com/a/b/pull/3 --json "+statusFields {
		t.Fatalf("by URL: %q", f.Calls[0])
	}
	if _, err := New(&repoprovider.FakeExec{}, nil).Status(ctx, target, repoprovider.ChangeRef{}); err == nil {
		t.Fatal("empty ref accepted")
	}
	missing := &repoprovider.FakeExec{Missing: map[string]bool{"gh": true}}
	if _, err := New(missing, nil).Status(ctx, target, repoprovider.ChangeRef{Number: 1}); !errors.Is(err, repoprovider.ErrBinaryMissing) {
		t.Fatalf("missing gh: %v", err)
	}
	expired := &repoprovider.FakeExec{Errs: map[string]string{"gh pr": "HTTP 401: Bad credentials"}}
	if _, err := New(expired, nil).Status(ctx, target, repoprovider.ChangeRef{Number: 1}); !errors.Is(err, repoprovider.ErrNotAuthenticated) {
		t.Fatalf("401: %v", err)
	}
	garbage := &repoprovider.FakeExec{Out: map[string]string{"gh pr": "not json"}}
	if _, err := New(garbage, nil).Status(ctx, target, repoprovider.ChangeRef{Number: 1}); err == nil {
		t.Fatal("garbage JSON accepted")
	}
}

func TestAuthStatus(t *testing.T) {
	ctx := context.Background()
	exit1 := fmt.Errorf("exit status 1")
	type step struct {
		out    string
		stderr string
		err    error
	}
	script := func(steps map[string]step) *repoprovider.FakeExec {
		return &repoprovider.FakeExec{Fn: func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
			s, ok := steps[name+" "+strings.Join(args[:2], " ")]
			if !ok {
				return "", "", nil, false
			}
			return s.out, s.stderr, s.err, true
		}}
	}
	cases := []struct {
		name    string
		env     func(string) []string
		f       *repoprovider.FakeExec
		want    repoprovider.AuthStatus
		wantErr bool
	}{
		{"store ok", storeEnv, script(map[string]step{"gh api user": {out: `{"login":"octo"}`}}),
			repoprovider.AuthStatus{Status: "ok", Login: "octo", Source: "store"}, false},
		{"store expired", storeEnv, script(map[string]step{"gh api user": {stderr: "HTTP 401: Bad credentials", err: exit1}}),
			repoprovider.AuthStatus{Status: "expired", Source: "store"}, false},
		{"store other failure", storeEnv, script(map[string]step{"gh api user": {stderr: "dial tcp: no route", err: exit1}}),
			repoprovider.AuthStatus{Status: "unknown", Source: "store"}, true},
		{"cli ok", noStoreEnv, script(map[string]step{
			"gh auth token": {out: "gho_x\n"}, "gh api user": {out: `{"login":"me"}`}}),
			repoprovider.AuthStatus{Status: "ok", Login: "me", Source: "cli"}, false},
		{"cli expired", noStoreEnv, script(map[string]step{
			"gh auth token": {out: "gho_x\n"}, "gh api user": {stderr: "HTTP 401", err: exit1}}),
			repoprovider.AuthStatus{Status: "expired", Source: "cli"}, false},
		{"missing", noStoreEnv, script(map[string]step{
			"gh auth token": {stderr: "no oauth token found for github.com", err: exit1}}),
			repoprovider.AuthStatus{Status: "missing", Source: "none"}, false},
		{"gh missing", storeEnv, &repoprovider.FakeExec{Missing: map[string]bool{"gh": true}},
			repoprovider.AuthStatus{Status: "unknown", Source: "none"}, false},
	}
	for _, c := range cases {
		got, err := New(c.f, c.env).AuthStatus(ctx, "github.com")
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: got %+v err=%v, want %+v wantErr=%v", c.name, got, err, c.want, c.wantErr)
		}
	}
}

func TestAuthStatusEnvRouting(t *testing.T) {
	// Store path: gh api user runs WITH the store env. CLI path: gh auth token
	// and gh api user both run with a nil env (the operator's own login).
	f := &repoprovider.FakeExec{Out: map[string]string{"gh api": `{"login":"octo"}`}}
	if _, err := New(f, storeEnv).AuthStatus(context.Background(), "github.com"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "gh api user --hostname github.com" || len(f.Envs[0]) != 2 {
		t.Fatalf("store path: %v %v", f.Calls, f.Envs)
	}
	g := &repoprovider.FakeExec{Out: map[string]string{"gh auth": "tok\n", "gh api": `{"login":"me"}`}}
	if _, err := New(g, noStoreEnv).AuthStatus(context.Background(), "ghe.corp"); err != nil {
		t.Fatal(err)
	}
	if g.Calls[0] != "gh auth token --hostname ghe.corp" || len(g.Envs[0]) != 0 || len(g.Envs[1]) != 0 {
		t.Fatalf("cli path: %v %v", g.Calls, g.Envs)
	}
}

func TestNetCtxKeepsCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, c := netCtx(ctx)
	defer c()
	if got != ctx {
		t.Fatal("caller deadline replaced")
	}
	got2, c2 := netCtx(context.Background())
	defer c2()
	if _, ok := got2.Deadline(); !ok {
		t.Fatal("no NetTimeout applied")
	}
}

// isolatedSecrets points credstore at a fresh 0700 temp dir.
func isolatedSecrets(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", dir)
}

// runAll drives Push, OpenChangeRequest and Status and returns the env each
// recorded call ran with.
func runAll(t *testing.T, tgt repoprovider.Target) [][]string {
	t.Helper()
	f := &repoprovider.FakeExec{Fn: func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
		if name == "gh" && len(args) > 1 && args[1] == "view" {
			return `{"state":"OPEN"}`, "", nil, true
		}
		return "", "", nil, false
	}, Out: map[string]string{"gh pr": "https://x/a/b/pull/1"}}
	p := New(f, credstore.Env)
	ctx := context.Background()
	if err := p.Push(ctx, tgt, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.OpenChangeRequest(ctx, tgt, repoprovider.ChangeRequest{Head: "b", Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Status(ctx, tgt, repoprovider.ChangeRef{Number: 1}); err != nil {
		t.Fatal(err)
	}
	if len(f.Envs) != 3 {
		t.Fatalf("calls: %v", f.Calls)
	}
	return f.Envs
}

// Review fix 2: with no stored token, every call runs on the operator's own
// gh login (nil env), exactly like board land today.
func TestCallsUseNilEnvWithoutStore(t *testing.T) {
	isolatedSecrets(t)
	for i, env := range runAll(t, target) {
		if env != nil {
			t.Fatalf("call %d ran with env %v, want nil", i, env)
		}
	}
}

func TestCallsUseStoreEnvWhenTokenStored(t *testing.T) {
	isolatedSecrets(t)
	if err := credstore.Write("github.com", credstore.GitHubTokenKey, "stored-token-value"); err != nil {
		t.Fatal(err)
	}
	for i, env := range runAll(t, target) {
		joined := strings.Join(env, "\n")
		if !strings.Contains(joined, "GH_TOKEN=stored-token-value") || !strings.Contains(joined, "GH_CONFIG_DIR=") {
			t.Fatalf("call %d env = %v", i, env)
		}
		if strings.Contains(joined, "GH_ENTERPRISE_TOKEN") {
			t.Fatalf("call %d exported an enterprise token for github.com", i)
		}
	}
}

// Review fix 3: a GitHub Enterprise Server host gets GH_ENTERPRISE_TOKEN.
func TestCallsUseEnterpriseTokenOnGHES(t *testing.T) {
	isolatedSecrets(t)
	if err := credstore.Write("ghe.corp", credstore.GitHubTokenKey, "ghes-token-value"); err != nil {
		t.Fatal(err)
	}
	tgt := repoprovider.Target{RepoDir: "/r", Remote: repoprovider.Remote{Host: "ghe.corp", Owner: "t", Repo: "a"}}
	for i, env := range runAll(t, tgt) {
		if !strings.Contains(strings.Join(env, "\n"), "GH_ENTERPRISE_TOKEN=ghes-token-value") {
			t.Fatalf("call %d env = %v", i, env)
		}
	}
	// AuthStatus treats the enterprise token as a stored token.
	f := &repoprovider.FakeExec{Out: map[string]string{"gh api": `{"login":"e"}`}}
	st, err := New(f, credstore.Env).AuthStatus(context.Background(), "ghe.corp")
	if err != nil || st.Source != repoprovider.SourceStore || st.Status != repoprovider.AuthOK {
		t.Fatalf("auth = %+v %v", st, err)
	}
	if !hasToken([]string{"GH_ENTERPRISE_TOKEN=x"}) || hasToken([]string{"GH_CONFIG_DIR=/x"}) {
		t.Fatal("hasToken")
	}
}
