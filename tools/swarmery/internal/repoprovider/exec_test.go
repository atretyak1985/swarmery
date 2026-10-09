package repoprovider

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// OSExec is exercised against /bin/sh only — never a real git/gh/glab.
func TestOSExecSeparatesStreamsAndAppliesEnv(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr, err := OSExec{}.Run(context.Background(), dir,
		[]string{"REPOPROVIDER_TEST_VAR=delta"},
		"sh", "-c", `printf '%s|%s' "$REPOPROVIDER_TEST_VAR" "$(pwd -P)"; printf 'oops' >&2`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stdout, "delta|") {
		t.Fatalf("stdout = %q (env delta not applied)", stdout)
	}
	if stderr != "oops" {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestOSExecNilEnvInheritsAndNilCtx(t *testing.T) {
	t.Setenv("REPOPROVIDER_TEST_INHERIT", "kept")
	//nolint:staticcheck // a nil ctx is tolerated on purpose
	stdout, _, err := OSExec{}.Run(nil, "", nil, "sh", "-c", `printf '%s' "$REPOPROVIDER_TEST_INHERIT"`)
	if err != nil || stdout != "kept" {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
}

func TestOSExecTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := OSExec{}.Run(ctx, "", nil, "sleep", "5")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error does not wrap DeadlineExceeded: %v", err)
	}
}

func TestOSExecLook(t *testing.T) {
	if err := (OSExec{}).Look("sh"); err != nil {
		t.Fatalf("Look(sh) = %v", err)
	}
	err := OSExec{}.Look("repoprovider-no-such-binary-xyz")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("Look(missing) = %v", err)
	}
	_, _, err = OSExec{}.Run(context.Background(), "", nil, "repoprovider-no-such-binary-xyz")
	if !errors.Is(Classify("", err), ErrBinaryMissing) {
		t.Fatalf("missing binary not classified: %v", err)
	}
}

func TestFakeExecScripting(t *testing.T) {
	f := &FakeExec{
		Out:     map[string]string{"git remote": "git@github.com:acme/widgets.git\n"},
		Errs:    map[string]string{"git push": "! [rejected] (fetch first)"},
		Missing: map[string]bool{"glab": true},
	}
	ctx := context.Background()
	out, _, err := f.Run(ctx, "/repo", []string{"A=1"}, "git", "remote", "get-url", "origin")
	if err != nil || out != "git@github.com:acme/widgets.git\n" {
		t.Fatalf("scripted out: %q %v", out, err)
	}
	_, errOut, err := f.Run(ctx, "/repo", nil, "git", "push", "-u", "origin", "b")
	if err == nil || errOut != "! [rejected] (fetch first)" {
		t.Fatalf("scripted err: %q %v", errOut, err)
	}
	if _, _, err := f.Run(ctx, "", nil, "glab", "mr"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing binary Run: %v", err)
	}
	if err := f.Look("glab"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing binary Look: %v", err)
	}
	if err := f.Look("gh"); err != nil {
		t.Fatalf("present binary Look: %v", err)
	}
	if out, _, err := f.Run(ctx, "", nil, "gh"); err != nil || out != "" {
		t.Fatalf("unscripted call: %q %v", out, err)
	}
	if !f.Ran("git push -u origin b") || f.Ran("--force") {
		t.Fatalf("Ran: %v", f.Calls)
	}
	if f.Dirs[0] != "/repo" || f.Envs[0][0] != "A=1" {
		t.Fatalf("dirs/envs not recorded: %v %v", f.Dirs, f.Envs)
	}

	f.Fn = func(_ string, _ []string, name string, args []string) (string, string, error, bool) {
		if FakeKey(name, args) == "gh api" {
			return `{"login":"x"}`, "", nil, true
		}
		return "", "", nil, false
	}
	if out, _, _ := f.Run(ctx, "", nil, "gh", "api", "user"); out != `{"login":"x"}` {
		t.Fatalf("Fn not consulted: %q", out)
	}
	if out, _, _ := f.Run(ctx, "", nil, "git", "remote"); out == "" {
		t.Fatal("Fn handled=false did not fall through")
	}
	if FakeKey("git", nil) != "git" {
		t.Fatal("FakeKey with no args")
	}
}

func TestFirstURL(t *testing.T) {
	cases := map[string]string{
		"Creating pull request…\nhttps://github.com/acme/widgets/pull/12\n": "https://github.com/acme/widgets/pull/12",
		"see https://gitlab.com/g/p/-/merge_requests/3).":                   "https://gitlab.com/g/p/-/merge_requests/3",
		"http://git.corp/x/y/pull/1,":                                       "http://git.corp/x/y/pull/1",
		"no url here":                                                       "",
		"":                                                                  "",
	}
	for in, want := range cases {
		if got := FirstURL(in); got != want {
			t.Errorf("FirstURL(%q) = %q, want %q", in, got, want)
		}
	}
}
