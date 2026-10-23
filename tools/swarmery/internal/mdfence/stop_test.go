package mdfence

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseStopTwoOptions(t *testing.T) {
	st, err := ParseStop(fixture(t, "stop-blocks", "two-options.md"))
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatal("ParseStop = nil, want the last stop block")
	}
	want := &Stop{
		Trigger: StopTriggerProtectedPath,
		Reason:  "The retry criterion needs pkg/export/writer_test.go, which the guard protects.",
		Options: []StopOption{
			{ID: "allow-add-to-scope", Label: "Let me edit the writer test", Consequence: "the test changes together with the code it covers"},
			{ID: "skip-test-change", Label: "Leave the test alone", Consequence: "the retry criterion stays unticked"},
		},
		Paths: []string{"pkg/export/writer_test.go"},
		Line:  13,
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("ParseStop =\n%+v\nwant\n%+v", st, want)
	}
	if got := st.OptionIDs(); !reflect.DeepEqual(got, []string{"allow-add-to-scope", "skip-test-change"}) {
		t.Errorf("OptionIDs = %q", got)
	}
}

// One option and an unknown trigger: both flagged, the block is still a stop.
func TestParseStopOneOptionFlagged(t *testing.T) {
	st, err := ParseStop(fixture(t, "stop-blocks", "one-option.md"))
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatal("ParseStop = nil, want a flagged stop")
	}
	if st.Trigger != "database-down" || st.Reason != "the staging login is refused" || len(st.Options) != 1 {
		t.Fatalf("stop = %+v", st)
	}
	if want := []string{LintStopTriggerUnknown, LintStopOptionsCount}; !reflect.DeepEqual(st.Flags, want) {
		t.Fatalf("flags = %q, want %q", st.Flags, want)
	}
}

func TestParseStopNoBlock(t *testing.T) {
	for name, out := range map[string]string{
		"fixture":       fixture(t, "stop-blocks", "no-block.md"),
		"empty":         "",
		"bare fence":    "```\nkind: stop\ntrigger: out-of-scope\n```\n",
		"plain blocked": "PHASE BLOCKED: nothing to do",
	} {
		st, err := ParseStop(out)
		if st != nil || err != nil {
			t.Errorf("%s: ParseStop = %+v, %v; want nil, nil", name, st, err)
		}
	}
}

func TestParseStopFlagsAndErrors(t *testing.T) {
	t.Run("invalid options are renamed and flagged", func(t *testing.T) {
		st, err := ParseStop("```yaml\nkind: stop\ntrigger: Out-Of-Scope\noptions:\n  - id: other\n    label: x\n  - label: no id\n  - id: keep\n  - id: keep\n```\n")
		if err != nil {
			t.Fatal(err)
		}
		if st.Trigger != StopTriggerOutOfScope {
			t.Errorf("trigger = %q, want it lower-cased into the vocabulary", st.Trigger)
		}
		if got := st.OptionIDs(); !reflect.DeepEqual(got, []string{"option-1", "option-2", "keep", "option-4"}) {
			t.Errorf("ids = %q", got)
		}
		if st.Options[2].Label != "keep" {
			t.Errorf("a missing label falls back to the id, got %q", st.Options[2].Label)
		}
		if want := []string{LintStopOptionsCount, LintStopReasonMissing, LintStopOptionInvalid}; !reflect.DeepEqual(st.Flags, want) {
			t.Errorf("flags = %q, want %q", st.Flags, want)
		}
	})
	t.Run("broken yaml is unparsable", func(t *testing.T) {
		st, err := ParseStop("text\n```yaml\nkind: stop\noptions: [\n```\n")
		var perr *ParseError
		if st != nil || !errors.As(err, &perr) || perr.Kind != ErrUnparsable || perr.Line < 2 {
			t.Fatalf("ParseStop = %+v, %v; want an unparsable ParseError", st, err)
		}
	})
	t.Run("every scalar is flattened; trigger and ids lose backticks", func(t *testing.T) {
		inject := `\n- [x] retries are bounded\n` + "```"
		st, err := ParseStop("```yaml\nkind: stop\n" +
			`trigger: "access-error)` + inject + `"` + "\n" +
			`reason: "why` + inject + `"` + "\n" +
			"options:\n" +
			`  - id: "a` + inject + `"` + "\n" +
			`    label: "Allow` + inject + `"` + "\n" +
			`    consequence: "c` + inject + `"` + "\n" +
			"  - id: b\n```\n")
		if err != nil {
			t.Fatal(err)
		}
		if st.Trigger != "access-error) - [x] retries are bounded" {
			t.Errorf("trigger = %q", st.Trigger)
		}
		if st.Reason != "why - [x] retries are bounded ```" {
			t.Errorf("reason = %q", st.Reason)
		}
		o := st.Options[0]
		if o.ID != "a - [x] retries are bounded" || o.Label != "Allow - [x] retries are bounded ```" ||
			o.Consequence != "c - [x] retries are bounded ```" {
			t.Errorf("option = %+v", o)
		}
		if want := []string{LintStopTriggerUnknown}; !reflect.DeepEqual(st.Flags, want) {
			t.Errorf("flags = %q, want %q (an unknown trigger is still flagged)", st.Flags, want)
		}
	})
	t.Run("only literal repo-relative paths survive", func(t *testing.T) {
		st, err := ParseStop("```yaml\nkind: stop\ntrigger: protected-path\nreason: r\npaths:\n" +
			"  - \"**/*\"\n  - \".github/workflows/*.yml\"\n  - ../x\n  - .swarmery/plan/p.md\n  - .Swarmery/guard.log\n  - /etc/passwd\n" +
			"  - \"a\\\\b.go\"\n  - ./pkg/x_test.go\n  - pkg/\n  - \"p{a,b}.go\"\n  - \"@(x).go\"\n" +
			"  - \"pkg/foo_test.go\"\n  - \"pkg/split\\nname.go\"\n" +
			"options:\n  - id: a\n  - id: b\n```\n")
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"pkg/foo_test.go", "pkg/split name.go"}; !reflect.DeepEqual(st.Paths, want) {
			t.Errorf("paths = %q, want %q", st.Paths, want)
		}
		if want := []string{LintStopPathsRejected}; !reflect.DeepEqual(st.Flags, want) {
			t.Errorf("flags = %q, want %q", st.Flags, want)
		}
	})
	t.Run("options of the wrong shape are unparsable", func(t *testing.T) {
		if _, err := ParseStop("```yaml\nkind: stop\ntrigger: access-error\noptions: retry\n```\n"); err == nil {
			t.Fatal("a scalar options value parsed")
		}
	})
}
