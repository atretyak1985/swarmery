package repoprovider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var pushTarget = Target{
	RepoDir:    "/repo",
	RemoteName: "origin",
	Remote:     Remote{Host: "gitlab.example.com", Owner: "group/sub", Repo: "widgets", Protocol: "ssh"},
}

func TestGitPushPlainUpstreamPush(t *testing.T) {
	f := &FakeExec{}
	env := []string{"GITLAB_TOKEN=x"}
	if err := GitPush(context.Background(), f, env, pushTarget, "swarm/phase-6"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0] != "git push -u origin swarm/phase-6" {
		t.Fatalf("calls = %v", f.Calls)
	}
	if f.Dirs[0] != "/repo" || strings.Join(f.Envs[0], "|") != "GITLAB_TOKEN=x" {
		t.Fatalf("dir/env = %q %v", f.Dirs[0], f.Envs[0])
	}
	// Empty remote name ⇒ DefaultRemote.
	f2 := &FakeExec{}
	if err := GitPush(context.Background(), f2, nil, Target{RepoDir: "/r"}, "b"); err != nil {
		t.Fatal(err)
	}
	if f2.Calls[0] != "git push -u origin b" {
		t.Fatalf("default remote: %q", f2.Calls[0])
	}
}

func TestGitPushRefusesSmuggledRefspecs(t *testing.T) {
	for _, b := range []string{"", "+main", "--force", "-f", "a:main", "a b", "a\tb"} {
		f := &FakeExec{}
		if err := GitPush(context.Background(), f, nil, pushTarget, b); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("GitPush(%q) = %v, want ErrInvalidRef", b, err)
		}
		if len(f.Calls) != 0 {
			t.Errorf("GitPush(%q) ran git: %v", b, f.Calls)
		}
	}
	bad := pushTarget
	bad.RemoteName = "--mirror"
	if err := GitPush(context.Background(), &FakeExec{}, nil, bad, "b"); !errors.Is(err, ErrInvalidRef) {
		t.Fatalf("bad remote: %v", err)
	}
}

func TestGitPushGitLabFailuresClassified(t *testing.T) {
	cases := map[string]error{
		"remote: You are not allowed to push code to this project.\nfatal: unable to access":                                      ErrNoPushAccess,
		"remote: HTTP Basic: Access denied\nfatal: Authentication failed for 'https://gitlab.example.com/group/sub/widgets.git/'": ErrNotAuthenticated,
		"error: HTTP 401 from gitlab.example.com":                                                                                 ErrNotAuthenticated,
		" ! [rejected]        b -> b (non-fast-forward)":                                                                          ErrRemoteDiverged,
	}
	for stderr, want := range cases {
		f := &FakeExec{Errs: map[string]string{"git push": stderr}}
		if err := GitPush(context.Background(), f, nil, pushTarget, "b"); !errors.Is(err, want) {
			t.Errorf("stderr %q: err = %v, want %v", stderr, err, want)
		}
	}
}

// A failing push whose stderr echoes a GitLab personal access token must come
// back with the token masked (SC-3 guard): the classified error is what a
// caller turns into a 422 body.
func TestGitPushFailureRedactsGitLabToken(t *testing.T) {
	tok := "glpat-" + strings.Repeat("Z", 24)
	stderr := "remote: You are not allowed to push code to this project.\n" +
		"fatal: unable to access 'https://oauth2:" + tok + "@gitlab.example.com/group/sub/widgets.git/': The requested URL returned error: 403"
	f := &FakeExec{Errs: map[string]string{"git push": stderr}}
	err := GitPush(context.Background(), f, nil, pushTarget, "b")
	if !errors.Is(err, ErrNoPushAccess) {
		t.Fatalf("err = %v, want ErrNoPushAccess", err)
	}
	if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), "glpat-ZZZ") {
		t.Fatalf("token leaked: %v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("token not masked: %v", err)
	}
}

func TestNetCtxKeepsCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, done := NetCtx(ctx)
	defer done()
	if got != ctx {
		t.Fatal("NetCtx replaced a caller deadline")
	}
	nc, done2 := NetCtx(context.Background())
	defer done2()
	dl, ok := nc.Deadline()
	if !ok || time.Until(dl) > NetTimeout || time.Until(dl) < NetTimeout-5*time.Second {
		t.Fatalf("NetCtx deadline = %v (ok=%v), want ≈ NetTimeout", dl, ok)
	}
}
