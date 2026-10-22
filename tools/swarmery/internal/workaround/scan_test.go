package workaround

import (
	"reflect"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
)

// d23 is the shipped list — the tests run against what the daemon matches.
func d23(t *testing.T) []string {
	t.Helper()
	c, err := guards.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	return c.WorkaroundWords
}

func words(hits []Hit) []string {
	out := []string{}
	for _, h := range hits {
		out = append(out, h.Word)
	}
	return out
}

func TestScanWholeWords(t *testing.T) {
	list := d23(t)
	cases := []struct {
		name string
		text string
		want []string
	}{
		// English.
		{"en word", "I used Edit instead.", []string{"instead"}},
		{"en case-insensitive", "INSTEAD of the hook, a flag.", []string{"instead"}},
		{"en sub-word prefix", "the insteadof directive", []string{}},
		{"en sub-word suffix", "a reworkaround is not one", []string{}},
		{"en skip vs skipped", "I skipped it, then skip the rest.", []string{"skipped", "skip"}},
		{"en skip inside skipping", "skipping is not skip-list", []string{}},
		{"en hyphenated entry", "Those failures are pre-existing.", []string{"pre-existing"}},
		{"en hyphen joins words", "a non-flaky test", []string{}},
		{"en phrase", "Leave it as is for now.", []string{"for now"}},
		{"en phrase across whitespace", "for\n now", []string{"for now"}},
		{"en phrase half", "for nowhere and now for", []string{}},
		{"en punctuation bounds", "(bypass), flaky!", []string{"bypass", "flaky"}},
		// Ukrainian.
		{"uk word", "Я знайшов обхід для цього.", []string{"обхід"}},
		{"uk sub-word", "Це обхідний шлях.", []string{}},
		{"uk case-insensitive", "ЗАМІСТЬ хука — прапорець.", []string{"замість"}},
		{"uk phrase", "Поки що лишаю як є.", []string{"поки що"}},
		{"uk three-word phrase", "Тест вже був зламаний до мене.", []string{"вже був зламаний"}},
		{"uk phrase broken", "вже був незламаний", []string{}},
		{"uk inflection is another word", "тимчасового рішення", []string{}},
		{"uk exact form", "Я тимчасово вимкнув тест.", []string{"тимчасово"}},
		{"uk apostrophe word", "пропустив'ятий", []string{}},
		// Code is not prose.
		{"inline code", "Pass `--skip` to the runner.", []string{}},
		{"fenced block", "Run it:\n```\ngo test -skip flaky\n```\nDone.", []string{}},
		{"prose around code", "Use `x` instead of `y`.", []string{"instead"}},
		{"unclosed backtick is prose", "a ` stray tick, then instead", []string{"instead"}},
		// Mixed language in one text.
		{"mixed", "Flaky test. Пропускаю його.", []string{"flaky", "пропускаю"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := words(Scan(c.text, list))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Scan(%q) = %v, want %v", c.text, got, c.want)
			}
		})
	}
}

func TestScanContextIsOneSentenceEachSide(t *testing.T) {
	text := "The reclaim refused the branch. So I checked it out instead of re-cutting it! Tests pass.\nNext line."
	hits := Scan(text, []string{"instead"})
	want := []Hit{{
		Word:     "instead",
		Sentence: "So I checked it out instead of re-cutting it!",
		Before:   "The reclaim refused the branch.",
		After:    "Tests pass.",
	}}
	if !reflect.DeepEqual(hits, want) {
		t.Fatalf("hits = %+v\nwant %+v", hits, want)
	}
}

func TestScanSentenceBoundaries(t *testing.T) {
	// A dot inside a file name, a number or code does not end a sentence; a
	// newline always does; a sentence that opens with code keeps the code.
	text := "I edited section.go and coverage is 92.1 now.\n`Reuse` instead of `t.Skip()` here? yes"
	hits := Scan(text, []string{"instead"})
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	h := hits[0]
	if h.Sentence != "`Reuse` instead of `t.Skip()` here?" {
		t.Errorf("Sentence = %q", h.Sentence)
	}
	if h.Before != "I edited section.go and coverage is 92.1 now." || h.After != "yes" {
		t.Errorf("Before = %q, After = %q", h.Before, h.After)
	}
}

func TestScanContextSkipsCode(t *testing.T) {
	text := "```\ngo test ./...\n```\nThe failure is pre-existing.\n- **Tests:**"
	hits := Scan(text, []string{"pre-existing"})
	if len(hits) != 1 || hits[0].Before != "" || hits[0].After != "- **Tests:**" {
		t.Fatalf("hits = %+v — code lines must not be context, a markdown label is prose", hits)
	}
}

func TestScanOneHitPerWordAndSentence(t *testing.T) {
	hits := Scan("skip this, skip that. Then skip.", []string{"skip"})
	if got := len(hits); got != 2 {
		t.Fatalf("hits = %+v, want one per sentence", hits)
	}
	if hits[0].Sentence != "skip this, skip that." || hits[1].Sentence != "Then skip." {
		t.Errorf("sentences = %q, %q", hits[0].Sentence, hits[1].Sentence)
	}
}

func TestScanClipsLongSentences(t *testing.T) {
	long := "instead"
	for i := 0; i < 100; i++ {
		long += " word"
	}
	hits := Scan(long, []string{"instead"})
	if len(hits) != 1 {
		t.Fatal(hits)
	}
	if r := []rune(hits[0].Sentence); len(r) != contextMaxRunes || r[len(r)-1] != '…' {
		t.Errorf("sentence not clipped to %d runes: %d", contextMaxRunes, len(r))
	}
}

func TestScanEmpty(t *testing.T) {
	if Scan("", []string{"instead"}) != nil || Scan("instead", nil) != nil || Scan("instead", []string{"  "}) != nil {
		t.Error("an empty text or list must find nothing")
	}
	if !Compile(nil).Empty() || Compile([]string{"x"}).Empty() {
		t.Error("Empty() wrong")
	}
}

// The shipped list is D23 exactly: en + uk, one array.
func TestShippedListIsD23(t *testing.T) {
	want := []string{
		"workaround", "instead", "bypass", "pre-existing", "skip", "skipped", "flaky", "for now", "temporarily",
		"обхід", "обійти", "замість", "тимчасово", "поки що", "пропускаю", "пропустив", "нестабільний", "вже був зламаний",
	}
	if got := d23(t); !reflect.DeepEqual(got, want) {
		t.Errorf("guards.json workaroundWords = %q\nwant D23 %q", got, want)
	}
}
