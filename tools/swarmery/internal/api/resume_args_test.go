package api

import (
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeflags"
)

// A resume is headless, so a missing --permission-mode is not a softer sandbox:
// the CLI refuses every write, including writes inside the session's own allowed
// directories, and still exits 0. The planning wizard writes its plan on the
// PROCEED turn — a resume — so this flag is what makes that turn produce files.
func TestResumeArgs_CarriesPermissionMode(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	args := resumeArgs("u-1", "go ahead", "")

	want := []string{"-r", "u-1", "-p", "go ahead", "--output-format", "json", "--permission-mode", claudeflags.DefaultMode}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", args, want)
	}
}

// The site knob wins over the global one, and "off" drops the flag entirely —
// the escape hatch an operator needs to run resumes under CLI defaults.
func TestResumeArgs_ModeOverrides(t *testing.T) {
	t.Setenv(claudeflags.ModeEnv, "acceptEdits")
	t.Setenv(resumePermEnv, "plan")
	if got := resumeArgs("u", "t", ""); got[len(got)-1] != "plan" {
		t.Fatalf("site env ignored: %q", got)
	}

	t.Setenv(resumePermEnv, claudeflags.OmitMode)
	got := resumeArgs("u", "t", "")
	for _, a := range got {
		if a == "--permission-mode" {
			t.Fatalf("%q=off must omit the flag: %q", resumePermEnv, got)
		}
	}
}

// A wizard resume carries the wizard's pinned model; the composer passes "" and
// the flag is absent, so a manual continuation still follows the CLI default.
func TestResumeArgs_Model(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	got := resumeArgs("u-1", "go ahead", "claude-sonnet-5")
	want := []string{"-r", "u-1", "-p", "go ahead", "--output-format", "json", "--model", "claude-sonnet-5", "--permission-mode", claudeflags.DefaultMode}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for _, a := range resumeArgs("u-1", "go ahead", "  ") {
		if a == "--model" {
			t.Fatal("blank model must omit --model")
		}
	}
}
