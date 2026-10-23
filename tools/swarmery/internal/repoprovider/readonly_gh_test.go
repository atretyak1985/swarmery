package repoprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repoprovider/credstore"
)

func tokenEnv(string) []string {
	return []string{"GH_TOKEN=ghp_testtoken0123456789abcdef", "GH_CONFIG_DIR=/tmp/isolated-gh"}
}

// TestReadOnlyGHRefusesWrites: every shape a write can take through gh is
// refused before a process starts — the -X POST the phase names, its attached
// and long spellings, the field/input flags that make gh switch to POST on its
// own, graphql (where mutations go), a method-override header, and any gh
// subcommand other than api.
func TestReadOnlyGHRefusesWrites(t *testing.T) {
	refused := [][]string{
		{"api", "-X", "POST", "repos/o/r/issues"},
		{"api", "-XPOST", "repos/o/r/issues"},
		{"api", "--method", "PATCH", "repos/o/r/issues/1"},
		{"api", "--method=delete", "repos/o/r/issues/1"},
		{"api", "-X", "PUT", "repos/o/r/pulls/1/merge"},
		{"api", "repos/o/r/issues/1/comments", "-f", "body=hi"},
		{"api", "repos/o/r/issues/1/comments", "-F", "body=@file"},
		{"api", "repos/o/r/issues/1/comments", "--raw-field", "body=hi"},
		{"api", "repos/o/r/issues/1/comments", "--field=body=hi"},
		{"api", "repos/o/r/issues/1/comments", "--input", "body.json"},
		{"api", "graphql", "-f", "query=mutation{}"},
		{"api", "/graphql"},
		{"api", "-H", "X-HTTP-Method-Override: POST", "repos/o/r/issues"},
		{"api", "repos/o/r/issues", "repos/o/r/pulls"},
		{"api"},
		{"pr", "create", "--title", "x"},
		{"issue", "comment", "1", "--body", "x"},
		{"pr", "merge", "1"},
		{"auth", "token"},
		{},
	}
	for _, args := range refused {
		fx := &FakeExec{}
		g, err := NewReadOnlyGH(fx, "github.com", tokenEnv)
		if err != nil {
			t.Fatal(err)
		}
		_, err = g.Run(context.Background(), args...)
		if !errors.Is(err, ErrWriteRefused) {
			t.Errorf("gh %s: err = %v, want ErrWriteRefused", strings.Join(args, " "), err)
		}
		if len(fx.Calls) != 0 {
			t.Errorf("gh %s: a process ran (%v) — the refusal must come first", strings.Join(args, " "), fx.Calls)
		}
	}
}

// TestReadOnlyGHAllowsReads: the reads the inbox needs run, with the stored
// token's env delta and no extra --hostname on github.com.
func TestReadOnlyGHAllowsReads(t *testing.T) {
	allowed := [][]string{
		{"api", "repos/o/r/issues?state=open&since=2026-10-22T00:00:00Z&per_page=100", "--paginate"},
		{"api", "--paginate", "--slurp", "repos/o/r/issues/comments?since=x"},
		{"api", "-X", "GET", "repos/o/r/pulls/comments"},
		{"api", "--method=get", "repos/o/r"},
		{"api", "-H", "Accept: application/vnd.github+json", "repos/o/r"},
		{"api", "repos/o/r", "--jq", ".full_name"},
	}
	for _, args := range allowed {
		fx := &FakeExec{Out: map[string]string{"gh api": "[]"}}
		g, err := NewReadOnlyGH(fx, "github.com", tokenEnv)
		if err != nil {
			t.Fatal(err)
		}
		out, err := g.Run(context.Background(), args...)
		if err != nil || out != "[]" {
			t.Errorf("gh %s: (%q, %v), want the read to run", strings.Join(args, " "), out, err)
			continue
		}
		if len(fx.Calls) != 1 || fx.Calls[0] != "gh "+strings.Join(args, " ") {
			t.Errorf("calls = %v, want exactly the argv given", fx.Calls)
		}
		if strings.Join(fx.Envs[0], " ") != strings.Join(tokenEnv(""), " ") {
			t.Errorf("env = %v, want the stored-token delta", fx.Envs[0])
		}
	}
}

func TestReadOnlyGHEnterpriseHostPinsHostname(t *testing.T) {
	fx := &FakeExec{Out: map[string]string{"gh api": "{}"}}
	g, err := NewReadOnlyGH(fx, "ghe.example.com", tokenEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.API(context.Background(), "repos/o/r"); err != nil {
		t.Fatal(err)
	}
	if !fx.Ran("gh api repos/o/r --hostname ghe.example.com") {
		t.Errorf("calls = %v, want --hostname pinned for an Enterprise host", fx.Calls)
	}
}

// TestReadOnlyGHNeedsStoredToken: no token in the delta ⇒ no reader. The
// operator's own CLI login is never borrowed.
func TestReadOnlyGHNeedsStoredToken(t *testing.T) {
	for _, env := range []func(string) []string{
		nil,
		func(string) []string { return nil },
		func(string) []string { return []string{"GH_CONFIG_DIR=/tmp/x", "GH_TOKEN="} },
		func(string) []string { return []string{"GITLAB_TOKEN=glpat-xxxxxxxxxxxxxxxxxxxxxxxx"} },
	} {
		if _, err := NewReadOnlyGH(&FakeExec{}, "github.com", env); !errors.Is(err, ErrNoStoredToken) {
			t.Errorf("err = %v, want ErrNoStoredToken", err)
		}
	}
}

// TestStoredReadOnlyGHUsesStoreAndIsolatedConfig: with a token in the daemon's
// store, the reader runs gh with that token and GH_CONFIG_DIR pointing inside
// the secrets dir — never the operator's own ~/.config/gh.
func TestStoredReadOnlyGHUsesStoreAndIsolatedConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARMERY_SECRETS_DIR", dir)

	if _, err := StoredReadOnlyGH(&FakeExec{}, "github.com"); !errors.Is(err, ErrNoStoredToken) {
		t.Fatalf("empty store: err = %v, want ErrNoStoredToken", err)
	}
	tok := "ghp_" + strings.Repeat("a", 36)
	if err := credstore.Write("github.com", credstore.GitHubTokenKey, tok); err != nil {
		t.Fatal(err)
	}
	fx := &FakeExec{Out: map[string]string{"gh api": "[]"}}
	g, err := StoredReadOnlyGH(fx, "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.API(context.Background(), "repos/o/r/issues"); err != nil {
		t.Fatal(err)
	}
	env := strings.Join(fx.Envs[0], "\n")
	if !strings.Contains(env, "GH_TOKEN="+tok) {
		t.Errorf("env lacks the stored token: %q", credstore.Redact(env))
	}
	if !strings.Contains(env, "GH_CONFIG_DIR="+filepath.Join(dir, "vcs-gh-config")) {
		t.Errorf("env lacks the isolated config dir under the secrets dir: %q", credstore.Redact(env))
	}
}

// TestReadOnlyGHErrorsAreRedacted: a failing call's error never carries the
// token, even when gh echoes it.
func TestReadOnlyGHErrorsAreRedacted(t *testing.T) {
	fx := &FakeExec{Errs: map[string]string{"gh api": "HTTP 401: Bad credentials (token ghp_testtoken0123456789abcdef)"}}
	g, err := NewReadOnlyGH(fx, "github.com", tokenEnv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.API(context.Background(), "repos/o/r/issues")
	if !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("err = %v, want ErrNotAuthenticated", err)
	}
	if err != nil && strings.Contains(err.Error(), "ghp_testtoken") {
		t.Errorf("token leaked into the error: %v", err)
	}
}
