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
		// The id is not a bare token once flattened, so it is renamed and flagged.
		if o.ID != "option-1" || o.Label != "Allow - [x] retries are bounded ```" ||
			o.Consequence != "c - [x] retries are bounded ```" {
			t.Errorf("option = %+v", o)
		}
		if want := []string{LintStopTriggerUnknown, LintStopOptionInvalid}; !reflect.DeepEqual(st.Flags, want) {
			t.Errorf("flags = %q, want %q (an unknown trigger is still flagged)", st.Flags, want)
		}
	})
	t.Run("an option id that could read back as another is renamed", func(t *testing.T) {
		st, err := ParseStop("```yaml\nkind: stop\ntrigger: access-error\nreason: r\noptions:\n" +
			"  - id: \"allow-add-to-scope — x\"\n  - id: \"Keep.Going_2\"\n```\n")
		if err != nil {
			t.Fatal(err)
		}
		if got := st.OptionIDs(); !reflect.DeepEqual(got, []string{"option-1", "keep.going_2"}) {
			t.Errorf("ids = %q", got)
		}
		if want := []string{LintStopOptionInvalid}; !reflect.DeepEqual(st.Flags, want) {
			t.Errorf("flags = %q, want %q", st.Flags, want)
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
		if want := []string{"pkg/foo_test.go"}; !reflect.DeepEqual(st.Paths, want) {
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

func TestLiteralRepoPath(t *testing.T) {
	for _, p := range []string{"pkg/foo_test.go", "scripts/tests/lib", "a b/c.go", "docs/ünïcode.md"} {
		if !LiteralRepoPath(p) {
			t.Errorf("LiteralRepoPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"", "**/*", "a?.go", "a[1].go", "p{a,b}.go", "@(x).go", "/etc/passwd", "../x", "a/../b", "a\\b",
		"./x", "a//b", "pkg/", ".", ".swarmery/guard.log", ".SWARMERY/plan/p.md",
		" pkg/x.go", "pkg/x.go ", "pkg/a\nb.go", "pkg/a\tb.go", "pkg/a\x7fb.go",
		"pkg/a\xffb.go", // invalid UTF-8
		// Runes spelled by code point so the source stays plain ASCII.
		"pkg/a" + string(rune(0x200b)) + "b.go",                             // zero-width space (Cf)
		"pkg/" + string(rune(0x202e)) + "og.tset_x",                         // right-to-left override (Cf)
		"pkg/a" + string(rune(0x2066)) + "b" + string(rune(0x2069)) + ".go", // bidi isolates (Cf)
		"pkg/a" + string(rune(0xfeff)) + "b.go",                             // byte-order mark (Cf)
		"pkg/a" + string(rune(0x00a0)) + "b.go",                             // no-break space: not printable
		"pkg/a" + string(rune(0x2028)) + "b.go",                             // line separator
	} {
		if LiteralRepoPath(p) {
			t.Errorf("LiteralRepoPath(%q) = true, want false", p)
		}
	}
}
