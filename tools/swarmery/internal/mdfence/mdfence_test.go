package mdfence

import (
	"reflect"
	"strings"
	"testing"
)

// This package is two load-bearing readers wide: wsingest counts a phase doc's
// checkboxes with it (so it decides what the dashboard calls progress), and
// runcore finds a run's `PHASE BLOCKED:` sentinel with it (so it decides
// whether a run is resumed). It had no direct tests — both callers exercised it
// only through their own fixtures, which is how a fence rule drifts without
// anyone noticing. These pin the rule itself.

// collect returns the lines ForEachLine yields, with their original indices.
func collect(text string) (idx []int, lines []string) {
	ForEachLine(text, func(i int, line string) {
		idx = append(idx, i)
		lines = append(lines, line)
	})
	return idx, lines
}

func TestForEachLine(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "no fences: every line is content",
			text: "alpha\nbeta\ngamma",
			want: []string{"alpha", "beta", "gamma"},
		},
		{
			name: "a backtick fence hides its body AND its own markers",
			text: "before\n```\nhidden\n```\nafter",
			want: []string{"before", "after"},
		},
		{
			name: "tilde fences count too",
			text: "before\n~~~\nhidden\n~~~\nafter",
			want: []string{"before", "after"},
		},
		{
			name: "a tilde marker cannot close a backtick fence",
			// Mixing the characters must not end the block early, or a code
			// sample containing ~~~ would leak its tail as document text.
			text: "before\n```\n~~~\nstill hidden\n```\nafter",
			want: []string{"before", "after"},
		},
		{
			name: "a shorter run cannot close a longer fence (CommonMark)",
			// This is the shape a phase doc uses to SHOW a generated template:
			// a ```` block quoting ```. Treating the inner marker as a close
			// would toggle twice and expose the quoted body as real content.
			text: "before\n````\n```\nquoted template\n```\n````\nafter",
			want: []string{"before", "after"},
		},
		{
			name: "a longer run does close a shorter fence",
			text: "before\n```\nhidden\n`````\nafter",
			want: []string{"before", "after"},
		},
		{
			name: "up to three leading spaces still opens a fence",
			text: "before\n   ```\nhidden\n   ```\nafter",
			want: []string{"before", "after"},
		},
		{
			name: "four leading spaces is an indented code line, not a fence",
			// It must NOT open a block — otherwise everything after an indented
			// snippet silently stops being content.
			text: "before\n    ```\nstill content\nafter",
			want: []string{"before", "    ```", "still content", "after"},
		},
		{
			name: "an unclosed fence swallows the rest",
			// Deliberate, and the reason EndsOpen exists: for a checklist this
			// is right (a half-pasted block is not the document's own text),
			// for a run's ending line it is not, and that caller compensates.
			text: "before\n```\nlog line\nPHASE BLOCKED: x",
			want: []string{"before"},
		},
		{
			name: "CRLF: the carriage return rides along, it does not break fencing",
			text: "before\r\n```\r\nhidden\r\n```\r\nafter",
			want: []string{"before\r", "after"},
		},
		{
			// strings.Split("", "\n") is []string{""}, so the callback fires once
			// with an empty line rather than not at all. Pinned rather than
			// "fixed": both callers are line matchers that find nothing in an
			// empty string anyway, and special-casing it would be a behaviour
			// change made for tidiness on a path that carries no risk.
			name: "empty text yields exactly one empty line, not zero",
			text: "",
			want: []string{""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := collect(tc.text)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestForEachLineReportsTheOriginalIndex: callers report line numbers to the
// operator, so a skipped fence must not shift the count of what follows.
func TestForEachLineReportsTheOriginalIndex(t *testing.T) {
	text := "zero\n```\ntwo\n```\nfour"
	idx, lines := collect(text)
	if want := []int{0, 4}; !reflect.DeepEqual(idx, want) {
		t.Errorf("indices = %v, want %v", idx, want)
	}
	if want := []string{"zero", "four"}; !reflect.DeepEqual(lines, want) {
		t.Errorf("lines = %q, want %q", lines, want)
	}
	// And the indices really do address those lines in the original text.
	all := strings.Split(text, "\n")
	for n, i := range idx {
		if all[i] != lines[n] {
			t.Errorf("index %d addresses %q, but the callback yielded %q", i, all[i], lines[n])
		}
	}
}

func TestEndsOpen(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"no fence at all", "alpha\nbeta", false},
		{"balanced fence", "a\n```\nb\n```\nc", false},
		{"opened and never closed", "a\n```\nb", true},
		{"closed by a longer run", "a\n```\nb\n````", false},
		{"inner shorter run does not close, so the outer stays open", "a\n````\n```\nb", true},
		{"tilde cannot close a backtick fence", "a\n```\n~~~\nb", true},
		{"two balanced fences", "a\n```\nb\n```\nc\n~~~\nd\n~~~\ne", false},
		{"empty text", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EndsOpen(tc.text); got != tc.want {
				t.Errorf("EndsOpen = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEndsOpenAgreesWithForEachLine: the two functions track fences with the
// same rule, and the whole point of EndsOpen is to tell a caller that
// ForEachLine just hid the tail of the document. If they ever disagree, the
// blocked-sentinel fallback fires on the wrong inputs.
func TestEndsOpenAgreesWithForEachLine(t *testing.T) {
	for _, text := range []string{
		"a\nb",
		"a\n```\nb\n```\nc",
		"a\n```\nb",
		"a\n````\n```\nb",
		"   ```\nb",
		"    ```\nb",
	} {
		lastLine := ""
		ForEachLine(text, func(_ int, line string) { lastLine = line })
		all := strings.Split(text, "\n")
		tailHidden := lastLine != all[len(all)-1]
		if got := EndsOpen(text); tailHidden != got && got {
			t.Errorf("text %q: EndsOpen=true but ForEachLine yielded the final line", text)
		}
	}
}
