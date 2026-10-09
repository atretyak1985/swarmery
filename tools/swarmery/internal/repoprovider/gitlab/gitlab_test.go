package gitlab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider"
)

// Every test drives a repoprovider.FakeExec: no real glab, git or network.

const host = "gitlab.example.com"

var target = repoprovider.Target{
	RepoDir:    "/repo",
	RemoteName: "origin",
	Remote:     repoprovider.Remote{Host: host, Owner: "group/sub", Repo: "widgets", Protocol: "ssh"},
}

func storeEnv(string) []string {
	return []string{"GITLAB_TOKEN=glpat-storestorestorestorestore", "GLAB_CONFIG_DIR=/secrets/vcs-glab-config"}
}

// noStoreEnv is credstore.Env's shape when only a GitHub token is stored for
// the host: config dirs, no GitLab token.
func noStoreEnv(string) []string {
	return []string{"GH_TOKEN=ghp_x", "GLAB_CONFIG_DIR=/secrets/vcs-glab-config"}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// script answers glab subcommands by their first TWO words ("mr create",
// "mr view", "repo view") — FakeExec's own keying stops at the first.
func script(answers map[string]func() (string, string, error)) func(string, []string, string, []string) (string, string, error, bool) {
	return func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
		if name != glabBinary || len(args) < 2 {
			return "", "", nil, false
		}
		if fn, ok := answers[args[0]+" "+args[1]]; ok {
			out, errOut, err := fn()
			return out, errOut, err, true
		}
		return "", "", nil, false
	}
}

func ok(stdout string) func() (string, string, error) {
	return func() (string, string, error) { return stdout, "", nil }
}

func fail(stderr string) func() (string, string, error) {
	return func() (string, string, error) { return "", stderr, fmt.Errorf("exit status 1") }
}

func callWith(f *repoprovider.FakeExec, prefix string) (string, []string) {
	for i, c := range f.Calls {
		if strings.HasPrefix(c, prefix) {
			return c, f.Envs[i]
		}
	}
	return "", nil
}

func TestKindAndTerms(t *testing.T) {
	p := New(&repoprovider.FakeExec{}, nil)
	if p.Kind() != repoprovider.KindGitLab {
		t.Fatalf("Kind = %q", p.Kind())
	}
	if got := p.Terms(); got != (repoprovider.Terms{Provider: "GitLab", Change: "Merge Request", ChangeShort: "MR"}) {
		t.Fatalf("Terms = %+v", got)
	}
}

func TestAuthStatus(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		env     func(string) []string
		f       *repoprovider.FakeExec
		want    repoprovider.AuthStatus
		wantErr bool
	}{
		{
			name: "store token ok",
			env:  storeEnv,
			f:    &repoprovider.FakeExec{Out: map[string]string{"glab api": `{"id":7,"username":"robot"}`}},
			want: repoprovider.AuthStatus{Status: repoprovider.AuthOK, Login: "robot", Source: repoprovider.SourceStore},
		},
		{
			name: "store token 401",
			env:  storeEnv,
			f:    &repoprovider.FakeExec{Errs: map[string]string{"glab api": "glab: 401 Unauthorized (HTTP 401)"}},
			want: repoprovider.AuthStatus{Status: repoprovider.AuthExpired, Source: repoprovider.SourceStore},
		},
		{
			name: "cli login ok",
			env:  noStoreEnv,
			f:    &repoprovider.FakeExec{Out: map[string]string{"glab api": `{"username":"me"}`}},
			want: repoprovider.AuthStatus{Status: repoprovider.AuthOK, Login: "me", Source: repoprovider.SourceCLI},
		},
		{
			name: "cli login rejected",
			env:  nil,
			f:    &repoprovider.FakeExec{Errs: map[string]string{"glab api": "HTTP 401: 401 Unauthorized"}},
			want: repoprovider.AuthStatus{Status: repoprovider.AuthExpired, Source: repoprovider.SourceCLI},
		},
		{
			name: "no store no cli login",
			env:  nil,
			f:    &repoprovider.FakeExec{Errs: map[string]string{"glab auth": "No token found for gitlab.example.com. Run glab auth login"}},
			want: repoprovider.AuthStatus{Status: repoprovider.AuthMissing, Source: repoprovider.SourceNone},
		},
		{
			name: "glab missing",
			env:  storeEnv,
			f:    &repoprovider.FakeExec{Missing: map[string]bool{"glab": true}},
			want: repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceNone},
		},
		{
			name:    "unclassified failure",
			env:     storeEnv,
			f:       &repoprovider.FakeExec{Errs: map[string]string{"glab api": "dial tcp: lookup gitlab.example.com: no such host"}},
			want:    repoprovider.AuthStatus{Status: repoprovider.AuthUnknown, Source: repoprovider.SourceStore},
			wantErr: true,
		},
	}
	for _, c := range cases {
		got, err := New(c.f, c.env).AuthStatus(ctx, host)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
		}
		if got != c.want {
			t.Errorf("%s: AuthStatus = %+v, want %+v", c.name, got, c.want)
		}
	}

	// The api call carries --hostname and the store env; the CLI probe runs
	// with nil env and never asks for the token.
	f := &repoprovider.FakeExec{}
	if _, err := New(f, nil).AuthStatus(ctx, host); err != nil {
		t.Fatal(err)
	}
	if f.Calls[0] != "glab auth status --hostname "+host || f.Envs[0] != nil {
		t.Fatalf("cli probe = %q env %v", f.Calls[0], f.Envs[0])
	}
	if f.Calls[1] != "glab api user --hostname "+host {
		t.Fatalf("api call = %q", f.Calls[1])
	}
	for _, c := range f.Calls {
		if strings.Contains(c, "--show-token") {
			t.Fatalf("AuthStatus asked glab for the token: %q", c)
		}
	}
	f2 := &repoprovider.FakeExec{}
	if _, err := New(f2, storeEnv).AuthStatus(ctx, host); err != nil {
		t.Fatal(err)
	}
	if len(f2.Calls) != 1 || !strings.HasPrefix(f2.Envs[0][0], "GITLAB_TOKEN=") {
		t.Fatalf("store path calls %v envs %v", f2.Calls, f2.Envs)
	}
}

func TestPushUsesSharedGitPush(t *testing.T) {
	f := &repoprovider.FakeExec{}
	if err := New(f, storeEnv).Push(context.Background(), target, "swarm/phase-6"); err != nil {
		t.Fatal(err)
	}
	if f.Calls[0] != "git push -u origin swarm/phase-6" || f.Dirs[0] != "/repo" {
		t.Fatalf("push = %q in %q", f.Calls[0], f.Dirs[0])
	}
	if !strings.HasPrefix(f.Envs[0][0], "GITLAB_TOKEN=") {
		t.Fatalf("push env = %v", f.Envs[0])
	}
	// No GitLab token in the delta ⇒ nil env (operator's own git credentials).
	f2 := &repoprovider.FakeExec{}
	if err := New(f2, noStoreEnv).Push(context.Background(), target, "b"); err != nil {
		t.Fatal(err)
	}
	if f2.Envs[0] != nil {
		t.Fatalf("push without store env = %v, want nil", f2.Envs[0])
	}
	if err := New(&repoprovider.FakeExec{}, nil).Push(context.Background(), target, "+main"); !errors.Is(err, repoprovider.ErrInvalidRef) {
		t.Fatalf("forced refspec: %v", err)
	}
}

func TestOpenChangeRequest(t *testing.T) {
	ctx := context.Background()
	mrURL := "https://gitlab.example.com/group/sub/widgets/-/merge_requests/42"
	cases := []struct {
		name     string
		req      repoprovider.ChangeRequest
		stdout   string
		stderr   string
		wantArgs string
		wantRepo bool // glab repo view consulted for the default branch
	}{
		{
			name:     "explicit base, ready",
			req:      repoprovider.ChangeRequest{Head: "swarm/phase-6", Base: "develop", Title: "Phase 6", Body: "Body text"},
			stdout:   "Creating merge request for swarm/phase-6 into develop in group/sub/widgets\n\n" + mrURL + "\n",
			wantArgs: "glab mr create --source-branch swarm/phase-6 --target-branch develop --title Phase 6 --description Body text --yes -R gitlab.example.com/group/sub/widgets",
		},
		{
			name:     "default base, draft, URL on stderr",
			req:      repoprovider.ChangeRequest{Head: "swarm/phase-6", Title: "T", Body: "B", Draft: true},
			stderr:   "Creating draft merge request…\n" + mrURL + ".\n",
			wantArgs: "glab mr create --source-branch swarm/phase-6 --target-branch trunk --title T --description B --draft --yes -R gitlab.example.com/group/sub/widgets",
			wantRepo: true,
		},
	}
	for _, c := range cases {
		f := &repoprovider.FakeExec{Fn: script(map[string]func() (string, string, error){
			"repo view": ok(`{"id":77,"default_branch":"trunk"}`),
			"mr create": func() (string, string, error) { return c.stdout, c.stderr, nil },
		})}
		ref, err := New(f, storeEnv).OpenChangeRequest(ctx, target, c.req)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ref.URL != mrURL || ref.Number != 42 || ref.Provider != repoprovider.KindGitLab {
			t.Errorf("%s: ref = %+v", c.name, ref)
		}
		call, env := callWith(f, "glab mr create")
		if call != c.wantArgs {
			t.Errorf("%s: call\n got %q\nwant %q", c.name, call, c.wantArgs)
		}
		if !strings.Contains(strings.Join(env, "|"), "GITLAB_HOST="+host) || !strings.HasPrefix(env[0], "GITLAB_TOKEN=") {
			t.Errorf("%s: env = %v", c.name, env)
		}
		repoCall, _ := callWith(f, "glab repo view")
		if (repoCall != "") != c.wantRepo {
			t.Errorf("%s: repo view call = %q, want consulted=%v", c.name, repoCall, c.wantRepo)
		}
		if c.wantRepo && repoCall != "glab repo view gitlab.example.com/group/sub/widgets --output json" {
			t.Errorf("%s: repo view = %q", c.name, repoCall)
		}
		for _, cl := range f.Calls {
			if strings.Contains(cl, "--remove-source-branch") || strings.Contains(cl, "--squash") {
				t.Errorf("%s: merge policy flag passed: %q", c.name, cl)
			}
		}
	}
}

func TestOpenChangeRequestDefaultBranchUnreadable(t *testing.T) {
	// repo view fails ⇒ --target-branch is omitted; glab applies the default.
	f := &repoprovider.FakeExec{Fn: script(map[string]func() (string, string, error){
		"repo view": fail("HTTP 404"),
		"mr create": ok("https://gitlab.example.com/group/sub/widgets/-/merge_requests/7\n"),
	})}
	ref, err := New(f, nil).OpenChangeRequest(context.Background(), target, repoprovider.ChangeRequest{Head: "b", Title: "T", Body: "B"})
	if err != nil || ref.Number != 7 {
		t.Fatalf("ref = %+v err = %v", ref, err)
	}
	call, env := callWith(f, "glab mr create")
	if strings.Contains(call, "--target-branch") {
		t.Fatalf("target branch guessed: %q", call)
	}
	// No store token: only GITLAB_HOST is set.
	if strings.Join(env, "|") != "GITLAB_HOST="+host {
		t.Fatalf("env = %v", env)
	}
}

func TestOpenChangeRequestFailures(t *testing.T) {
	ctx := context.Background()
	req := repoprovider.ChangeRequest{Head: "b", Base: "main", Title: "T", Body: "B"}
	cases := map[string]error{
		"remote: You are not allowed to push code to this project.":                                                     repoprovider.ErrNoPushAccess,
		"POST https://gitlab.example.com/api/v4/projects/77/merge_requests: 401 {message: 401 Unauthorized} (HTTP 401)": repoprovider.ErrNotAuthenticated,
		"glab: 403 Forbidden": repoprovider.ErrNoPushAccess,
	}
	for stderr, want := range cases {
		f := &repoprovider.FakeExec{Errs: map[string]string{"glab mr": stderr}}
		if _, err := New(f, nil).OpenChangeRequest(ctx, target, req); !errors.Is(err, want) {
			t.Errorf("stderr %q: err = %v, want %v", stderr, err, want)
		}
	}
	// Exit 0 without a URL.
	f := &repoprovider.FakeExec{Out: map[string]string{"glab mr": "Something happened"}}
	if _, err := New(f, nil).OpenChangeRequest(ctx, target, req); !errors.Is(err, ErrNoURL) {
		t.Fatalf("no URL: %v", err)
	}
	// glab missing.
	f = &repoprovider.FakeExec{Missing: map[string]bool{"glab": true}}
	if _, err := New(f, nil).OpenChangeRequest(ctx, target, req); !errors.Is(err, repoprovider.ErrBinaryMissing) {
		t.Fatalf("missing glab: %v", err)
	}
	// Smuggled refs never reach glab.
	for _, bad := range []repoprovider.ChangeRequest{{Head: "--force", Title: "T"}, {Head: "b", Base: "a:main", Title: "T"}} {
		f := &repoprovider.FakeExec{}
		if _, err := New(f, nil).OpenChangeRequest(ctx, target, bad); !errors.Is(err, repoprovider.ErrInvalidRef) || len(f.Calls) != 0 {
			t.Errorf("bad ref %+v: err = %v calls %v", bad, err, f.Calls)
		}
	}
	// A token echoed in a failure is masked.
	tok := "glpat-" + strings.Repeat("K", 24)
	f = &repoprovider.FakeExec{Errs: map[string]string{"glab mr": "error using token " + tok + ": HTTP 401"}}
	_, err := New(f, nil).OpenChangeRequest(ctx, target, req)
	if err == nil || strings.Contains(err.Error(), tok) || !strings.Contains(err.Error(), "***") {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestStatusNormalization(t *testing.T) {
	cases := []struct {
		fixture string
		want    repoprovider.ChangeStatus
	}{
		{"mr_opened_head_pipeline_running.json", repoprovider.ChangeStatus{
			State: repoprovider.StateOpen, Draft: true, CI: repoprovider.CIPending, Review: repoprovider.ReviewNone}},
		{"mr_merged_pipeline_success.json", repoprovider.ChangeStatus{
			State: repoprovider.StateMerged, CI: repoprovider.CIPassing, Review: repoprovider.ReviewApproved}},
		{"mr_closed_no_pipeline.json", repoprovider.ChangeStatus{
			State: repoprovider.StateClosed, CI: repoprovider.CINone, Review: repoprovider.ReviewNone}},
		{"mr_opened_pipeline_failed_unapproved.json", repoprovider.ChangeStatus{
			State: repoprovider.StateOpen, CI: repoprovider.CIFailing, Review: repoprovider.ReviewRequired}},
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, c := range cases {
		body := fixture(t, c.fixture)
		f := &repoprovider.FakeExec{Out: map[string]string{"glab mr": body}}
		p := New(f, storeEnv)
		p.now = func() time.Time { return now }
		got, err := p.Status(context.Background(), target, repoprovider.ChangeRef{Number: 42, Provider: repoprovider.KindGitLab})
		if err != nil {
			t.Fatalf("%s: %v", c.fixture, err)
		}
		c.want.CheckedAt = now
		if got != c.want {
			t.Errorf("%s: Status = %+v, want %+v", c.fixture, got, c.want)
		}
		if f.Calls[0] != "glab mr view 42 -R gitlab.example.com/group/sub/widgets --output json" {
			t.Errorf("%s: call = %q", c.fixture, f.Calls[0])
		}
		if !strings.Contains(strings.Join(f.Envs[0], "|"), "GITLAB_HOST="+host) {
			t.Errorf("%s: env = %v", c.fixture, f.Envs[0])
		}
	}
}

func TestCISummaryStatuses(t *testing.T) {
	cases := map[string]string{
		"success": repoprovider.CIPassing, "skipped": repoprovider.CIPassing,
		"failed": repoprovider.CIFailing, "canceled": repoprovider.CIFailing,
		"running": repoprovider.CIPending, "pending": repoprovider.CIPending,
		"created": repoprovider.CIPending, "manual": repoprovider.CIPending,
		"": repoprovider.CINone,
	}
	for status, want := range cases {
		if got := ciSummary(mrView{HeadPipeline: &pipelineRef{Status: status}}); got != want {
			t.Errorf("head_pipeline %q: %q, want %q", status, got, want)
		}
		if got := ciSummary(mrView{Pipeline: &pipelineRef{Status: status}}); got != want {
			t.Errorf("pipeline %q: %q, want %q", status, got, want)
		}
	}
	if got := ciSummary(mrView{}); got != repoprovider.CINone {
		t.Errorf("no pipeline: %q", got)
	}
	if got := mrState("locked"); got != repoprovider.StateOpen {
		t.Errorf("locked: %q", got)
	}
}

func TestStatusSelectorsAndErrors(t *testing.T) {
	ctx := context.Background()
	// No number: the iid is parsed from the URL.
	f := &repoprovider.FakeExec{Out: map[string]string{"glab mr": `{"state":"opened"}`}}
	if _, err := New(f, nil).Status(ctx, target, repoprovider.ChangeRef{URL: "https://gitlab.example.com/group/sub/widgets/-/merge_requests/9"}); err != nil {
		t.Fatal(err)
	}
	if f.Calls[0] != "glab mr view 9 -R gitlab.example.com/group/sub/widgets --output json" {
		t.Fatalf("call = %q", f.Calls[0])
	}
	if _, err := New(&repoprovider.FakeExec{}, nil).Status(ctx, target, repoprovider.ChangeRef{}); err == nil {
		t.Fatal("empty ref accepted")
	}
	f = &repoprovider.FakeExec{Out: map[string]string{"glab mr": "not json"}}
	if _, err := New(f, nil).Status(ctx, target, repoprovider.ChangeRef{Number: 1}); err == nil {
		t.Fatal("bad JSON accepted")
	}
	f = &repoprovider.FakeExec{Errs: map[string]string{"glab mr": "HTTP 401"}}
	if _, err := New(f, nil).Status(ctx, target, repoprovider.ChangeRef{Number: 1}); !errors.Is(err, repoprovider.ErrNotAuthenticated) {
		t.Fatalf("401: %v", err)
	}
	f = &repoprovider.FakeExec{Missing: map[string]bool{"glab": true}}
	if _, err := New(f, nil).Status(ctx, target, repoprovider.ChangeRef{Number: 1}); !errors.Is(err, repoprovider.ErrBinaryMissing) {
		t.Fatalf("missing glab: %v", err)
	}
}

func TestMRIIDAndRepoArg(t *testing.T) {
	iids := map[string]int{
		"https://gitlab.com/a/b/-/merge_requests/12":          12,
		"https://gitlab.com/a/b/merge_requests/13":            13,
		"https://gitlab.com/a/b/-/merge_requests/14/diffs":    14,
		"https://gitlab.com/a/b/-/merge_requests/15#note_1":   15,
		"https://gitlab.com/a/b/-/merge_requests/16?tab=diff": 16,
		"https://gitlab.com/a/b/-/merge_requests/x":           0,
		"https://gitlab.com/a/b":                              0,
	}
	for u, want := range iids {
		if got := mrIID(u); got != want {
			t.Errorf("mrIID(%q) = %d, want %d", u, got, want)
		}
	}
	if got := repoArg(repoprovider.Remote{Owner: "a", Repo: "b"}); got != "a/b" {
		t.Errorf("hostless repoArg = %q", got)
	}
	if got := repoArg(repoprovider.Remote{Host: "h"}); got != "" {
		t.Errorf("unparsed repoArg = %q", got)
	}
}
