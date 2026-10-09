package runcore

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// scrubFixture is a composed spawn env carrying every scrubbed key between
// ordinary entries (literal non-secrets), plus look-alikes that must survive.
func scrubFixture() []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"GH_TOKEN=fake-gh-token",
		"HOME=/home/op",
		"GITHUB_TOKEN=fake-github-token",
		"GH_ENTERPRISE_TOKEN=fake-ghe-token",
		"GLAB_TOKEN=fake-glab-token",
		"CLAUDE_CONFIG_DIR=/home/op/.claude-work",
		"GITLAB_TOKEN=fake-gitlab-token",
		"GH_CONFIG_DIR=/secrets/gh",
		"GLAB_CONFIG_DIR=/secrets/glab",
		"GH_TOKEN_HINT=not-a-token-key", // prefix look-alike: kept
		"MY_GH_TOKEN=kept",              // suffix look-alike: kept
		"LANG=C.UTF-8",
	}
}

func TestScrubVCSTokensFlagOffIsIdentity(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes"} {
		t.Run("flag="+v, func(t *testing.T) {
			t.Setenv(ScrubVCSTokensEnv, v)
			in := scrubFixture()
			out := scrubVCSTokens(in)
			if !reflect.DeepEqual(in, out) {
				t.Fatalf("flag %q changed the env:\n in=%v\nout=%v", v, in, out)
			}
			// Same backing array: the off path must not even copy.
			if len(out) > 0 && &out[0] != &in[0] {
				t.Error("flag off returned a copy, want the same slice")
			}
		})
	}
}

func TestScrubVCSTokensFlagOnDropsEveryKey(t *testing.T) {
	t.Setenv(ScrubVCSTokensEnv, "1")
	in := scrubFixture()
	snapshot := append([]string(nil), in...)
	out := scrubVCSTokens(in)

	for _, kv := range out {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GLAB_TOKEN", "GITLAB_TOKEN",
			"GH_CONFIG_DIR", "GLAB_CONFIG_DIR":
			t.Errorf("%s survived the scrub", key)
		}
	}
	want := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/op",
		"CLAUDE_CONFIG_DIR=/home/op/.claude-work",
		"GH_TOKEN_HINT=not-a-token-key",
		"MY_GH_TOKEN=kept",
		"LANG=C.UTF-8",
	}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("kept entries (in order) = %v, want %v", out, want)
	}
	// The caller's slice is not mutated.
	if !reflect.DeepEqual(in, snapshot) {
		t.Errorf("input mutated: %v", in)
	}
}

// TestStart_ScrubFlagReachesTheChild drives the real ClaudeRunner against a
// fake `claude` that reports the GH_TOKEN it saw: the wiring in Start, not just
// the helper, is what keeps a token out of an agent.
func TestStart_ScrubFlagReachesTheChild(t *testing.T) {
	const absent = "<absent>"
	child := func(t *testing.T) string {
		t.Helper()
		res, err := ClaudeRunner{Engine: "test"}.Start(context.Background(), Spec{
			Prompt: "p", SessionUUID: "u-scrub", Cwd: t.TempDir(),
			Bin:           fakeBin(t, `printf '%s\n' "${GH_TOKEN-`+absent+`}"`+"\n"),
			Timeout:       30 * time.Second,
			CaptureStdout: true,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		return strings.TrimSpace(res.Output)
	}
	t.Setenv("GH_TOKEN", "fake-daemon-token")

	t.Run("off", func(t *testing.T) {
		t.Setenv(ScrubVCSTokensEnv, "")
		if got := child(t); got != "fake-daemon-token" {
			t.Errorf("flag off: child GH_TOKEN = %q, want the inherited value", got)
		}
	})
	t.Run("on", func(t *testing.T) {
		t.Setenv(ScrubVCSTokensEnv, "1")
		if got := child(t); got != absent {
			t.Errorf("flag on: child GH_TOKEN = %q, want it absent", got)
		}
	})
}

func TestScrubVCSTokensNilEnv(t *testing.T) {
	t.Setenv(ScrubVCSTokensEnv, "1")
	if out := scrubVCSTokens(nil); len(out) != 0 {
		t.Errorf("scrub(nil) = %v, want empty", out)
	}
}
