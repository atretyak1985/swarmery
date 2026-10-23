package mdfence

import (
	"reflect"
	"testing"
)

func TestParseStopTwoOptions(t *testing.T) {
	st, perr := ParseStop(fixture(t, "stop-blocks", "two-options.md"))
	if perr != nil || st == nil {
		t.Fatalf("ParseStop = %v, %v; want a block", st, perr)
	}
	if st.Trigger != StopProtectedPath {
		t.Errorf("Trigger = %q, want %q", st.Trigger, StopProtectedPath)
	}
	if st.Reason == "" {
		t.Error("Reason is empty")
	}
	want := []StopOption{
		{ID: "allow", Label: "Allow editing scripts/tests/run.sh", Consequence: "the re-run adds the suite to the script and ticks the criterion"},
		{ID: "skip", Label: "Keep the script protected", Consequence: "the suite is not wired into CI; the criterion stays open"},
	}
	if !reflect.DeepEqual(st.Options, want) {
		t.Errorf("Options = %+v, want %+v", st.Options, want)
	}
	if !reflect.DeepEqual(st.Paths, []string{"scripts/tests/run.sh"}) {
		t.Errorf("Paths = %v", st.Paths)
	}
	if len(st.Lints) != 0 {
		t.Errorf("Lints = %v, want none", st.Lints)
	}
}

func TestParseStopOneOptionFlagged(t *testing.T) {
	st, perr := ParseStop(fixture(t, "stop-blocks", "one-option.md"))
	if perr != nil || st == nil {
		t.Fatalf("ParseStop = %v, %v; want a block", st, perr)
	}
	if len(st.Options) != 1 || st.Options[0].ID != "relogin" {
		t.Errorf("Options = %+v, want the one relogin option", st.Options)
	}
	// Both slips are named, and neither drops the block: the operator still answers it.
	if !reflect.DeepEqual(st.Lints, []string{LintStopTriggerUnknown, LintStopOptionsCount}) {
		t.Errorf("Lints = %v, want trigger-unknown + options-count", st.Lints)
	}
	if st.Trigger != "login-expired" {
		t.Errorf("Trigger = %q, want the unknown trigger kept verbatim", st.Trigger)
	}
}

func TestParseStopNoBlock(t *testing.T) {
	st, perr := ParseStop(fixture(t, "stop-blocks", "no-block.md"))
	if st != nil || perr != nil {
		t.Fatalf("ParseStop = %+v, %v; want nil, nil", st, perr)
	}
	if st, perr := ParseStop(""); st != nil || perr != nil {
		t.Fatalf("ParseStop(\"\") = %+v, %v; want nil, nil", st, perr)
	}
}

func TestParseStopUnparsable(t *testing.T) {
	cases := map[string]string{
		"no reason":       "```yaml\nkind: stop\ntrigger: out-of-scope\noptions:\n  - id: a\n    label: A\n```\n",
		"options scalar":  "```yaml\nkind: stop\ntrigger: out-of-scope\nreason: r\noptions: yes\n```\n",
		"option no label": "```yaml\nkind: stop\ntrigger: out-of-scope\nreason: r\noptions:\n  - id: a\n```\n",
		"broken yaml":     "```yaml\nkind: stop\ntrigger: [\n```\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			st, perr := ParseStop(in)
			if perr == nil || perr.Kind != ErrUnparsable || st != nil {
				t.Fatalf("ParseStop = %+v, %v; want unparsable", st, perr)
			}
		})
	}
}

// The last block wins: a run that revised its question mid-message asks the
// revised one.
func TestParseStopLastBlockWins(t *testing.T) {
	in := "```yaml\nkind: stop\ntrigger: out-of-scope\nreason: first\noptions:\n  - {id: a, label: A}\n  - {id: b, label: B}\n```\n" +
		"```yaml\nkind: stop\ntrigger: access-error\nreason: second\noptions:\n  - {id: a, label: A}\n  - {id: b, label: B}\n```\n"
	st, perr := ParseStop(in)
	if perr != nil || st == nil || st.Reason != "second" || st.Trigger != StopAccessError {
		t.Fatalf("ParseStop = %+v, %v; want the second block", st, perr)
	}
}

func TestStopTriggerFor(t *testing.T) {
	for in, want := range map[string]string{
		"out-of-scope change":     StopOutOfScope,
		"protected path":          StopProtectedPath,
		"access or login error":   StopAccessError,
		"criterion impossible":    StopCriterionImpossible,
		"the moon is not aligned": "",
	} {
		if got := StopTriggerFor(in); got != want {
			t.Errorf("StopTriggerFor(%q) = %q, want %q", in, got, want)
		}
	}
	if !AllowsScope("allow") || !AllowsScope("Allow-add-to-scope") || AllowsScope("skip") {
		t.Error("AllowsScope misreads its ids")
	}
}
