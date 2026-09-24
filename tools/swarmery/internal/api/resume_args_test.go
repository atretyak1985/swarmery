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

	args := resumeArgs("u-1", "go ahead", resumeOrigin{})

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
	if got := resumeArgs("u", "t", resumeOrigin{}); got[len(got)-1] != "plan" {
		t.Fatalf("site env ignored: %q", got)
	}

	t.Setenv(resumePermEnv, claudeflags.OmitMode)
	got := resumeArgs("u", "t", resumeOrigin{})
	for _, a := range got {
		if a == "--permission-mode" {
			t.Fatalf("%q=off must omit the flag: %q", resumePermEnv, got)
		}
	}
}

// A wizard resume carries the wizard's pinned model. A blank one still omits the
// flag — that path now belongs to a session whose row records no model at all,
// not to the composer, which supplies the session's own model (see
// TestLookupResumeOrigin_*).
func TestResumeArgs_Model(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	got := resumeArgs("u-1", "go ahead", resumeOrigin{Model: "claude-sonnet-5"})
	want := []string{"-r", "u-1", "-p", "go ahead", "--output-format", "json", "--model", "claude-sonnet-5", "--permission-mode", claudeflags.DefaultMode}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	for _, a := range resumeArgs("u-1", "go ahead", resumeOrigin{Model: "  "}) {
		if a == "--model" {
			t.Fatal("blank model must omit --model")
		}
	}
}

// The three flags that make a resume a CONTINUATION rather than a new
// conversation sharing a transcript: the model the session speaks as, the agent
// the original run ran as, and the settings stack it ran under. Before this they
// were all dropped, so the composer's every reply ran as a generic assistant on
// the account default (Fable, ~2× Opus) inside an Opus-pinned session.
func TestResumeArgs_CarriesOriginFlags(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	got := resumeArgs("u-1", "go ahead", resumeOrigin{
		Model:          "claude-opus-5-5",
		Effort:         "high",
		Agent:          "tech-lead",
		SettingSources: "project,local",
	})
	want := []string{
		"-r", "u-1", "-p", "go ahead", "--output-format", "json",
		"--model", "claude-opus-5-5", "--effort", "high",
		"--agent", "tech-lead", "--setting-sources", "project,local",
		"--permission-mode", claudeflags.DefaultMode,
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A session swarmery did not spawn has no agent and no pinned settings stack,
// and must resume exactly as it did before origin flags existed — plus its own
// model. Every empty field omits its flag rather than emitting an empty value,
// which the CLI would reject.
func TestResumeArgs_EmptyOriginFieldsOmitTheirFlags(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	got := resumeArgs("u-1", "go ahead", resumeOrigin{Model: "claude-opus-5-5", Effort: "low"})
	for _, a := range got {
		if a == "--agent" || a == "--setting-sources" {
			t.Fatalf("empty origin field must omit its flag: %q", got)
		}
	}
}

// --settings is the flag that makes --agent resolvable for the two engines that
// run in a worktree under ~/.swarmery: nothing there walks up to the project's
// .claude/settings.json, so the project's enabled plugins — and the agent they
// ship — exist for that turn only because the file is lent explicitly. The
// composer's resume reconstructed --agent without it, which turned a reply that
// used to work on a multi-repo plan run into a spawn asking for an agent that
// cannot exist. It is emitted after --agent, exactly as runcore.Args does.
func TestResumeArgs_CarriesSettingsFile(t *testing.T) {
	t.Setenv(resumePermEnv, "")
	t.Setenv(claudeflags.ModeEnv, "")

	got := resumeArgs("u-1", "go ahead", resumeOrigin{
		Model:        "claude-opus-5-5",
		Effort:       "high",
		Agent:        "tech-lead",
		SettingsFile: "/p/.claude/settings.json",
	})
	want := []string{
		"-r", "u-1", "-p", "go ahead", "--output-format", "json",
		"--model", "claude-opus-5-5", "--effort", "high",
		"--agent", "tech-lead", "--settings", "/p/.claude/settings.json",
		"--permission-mode", claudeflags.DefaultMode,
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	// A blank one omits the flag rather than passing an empty path, which the
	// CLI would try to read as a settings file.
	for _, a := range resumeArgs("u-1", "go ahead", resumeOrigin{SettingsFile: "  "}) {
		if a == "--settings" {
			t.Fatal("blank settings file must omit --settings")
		}
	}
}

// sessions.model records what the TRANSCRIPT reported, which for a 1M-context
// run carries a `[1m]` suffix. Handing that back to --model is not a no-op — it
// is a request for a different context tier, which the CLI rejects or re-prices
// — so the resume takes the bare id and leaves how-it-was-launched alone.
func TestStripContextSuffix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"claude-opus-5-5[1m]", "claude-opus-5-5"},
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"  claude-sonnet-5  ", "claude-sonnet-5"},
		{"", ""},
		// A leading bracket is not a suffix marker — there is no model id before
		// it to keep, so the value passes through rather than becoming "".
		{"[1m]", "[1m]"},
	} {
		if got := stripContextSuffix(tc.in); got != tc.want {
			t.Errorf("stripContextSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The nil-DB path is the hermetic handler tests' path: it must still hand back a
// usable effort rather than an empty origin, because an empty Effort omits
// --effort and lands the run on the CLI's xhigh.
func TestLookupResumeOrigin_NilDBStillPinsEffort(t *testing.T) {
	t.Setenv(resumeEffortEnv, "")
	t.Setenv(claudeflags.EffortEnv, "")

	o := lookupResumeOrigin(nil, "u-1")
	if o.Effort != ResumeEffort {
		t.Fatalf("effort = %q, want %q", o.Effort, ResumeEffort)
	}
	if o.Model != "" || o.Agent != "" || o.SettingSources != "" {
		t.Fatalf("nil db must leave the derived fields empty: %+v", o)
	}
}
