package runcore

import (
	"strings"
	"testing"
	"time"
)

// TestClassifyEnd covers every branch of the completion decision, with the
// transcript shapes the three engines actually produce.
func TestClassifyEnd(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		done, tot  int
		want       EndState
		wantReason string
	}{
		{
			name: "all criteria ticked is done",
			text: "I implemented the thing.\n\nPHASE DONE",
			done: 3, tot: 3, want: EndDone,
		},
		{
			name: "done sentinel with unticked criteria is NOT done",
			text: "Everything is finished.\n\nPHASE DONE",
			done: 2, tot: 3, want: EndContinue,
		},
		{
			name: "no sentinel and unticked criteria continues",
			text: "Here is my progress report. Next I would add the migration.",
			done: 0, tot: 4, want: EndContinue,
		},
		{
			name: "phase blocked wins over a full tick count",
			text: "- [x] all done\n\nPHASE BLOCKED: the schema does not match the doc",
			done: 3, tot: 3, want: EndBlocked,
			wantReason: "the schema does not match the doc",
		},
		{
			name:       "plan blocked carries its free-form qualifier",
			text:       "PLAN BLOCKED at phase 3: needs a human to approve the drop",
			done:       1, tot: 5, want: EndBlocked,
			wantReason: "needs a human to approve the drop",
		},
		{
			name:       "bare dispatch-style blocked",
			text:       "BLOCKED: file scope does not cover internal/store",
			done:       0, tot: 2, want: EndBlocked,
			wantReason: "file scope does not cover internal/store",
		},
		{
			name:       "markdown-wrapped blocked line still matches",
			text:       "summary\n\n**PHASE BLOCKED: the API it calls does not exist**",
			done:       0, tot: 1, want: EndBlocked,
			wantReason: "the API it calls does not exist**",
		},
		{
			name:       "the LAST blocked line wins",
			text:       "The contract says to end with PHASE BLOCKED: <reason>.\nPHASE BLOCKED: the real one",
			done:       0, tot: 1, want: EndBlocked,
			wantReason: "the real one",
		},
		{
			name: "a doc with no checkboxes settles rather than looping",
			text: "I did the work.",
			done: 0, tot: 0, want: EndDone,
		},
		{
			name: "no transcript at all with work left still continues",
			text: "",
			done: 1, tot: 2, want: EndContinue,
		},
		{
			name: "prose containing the word done is not a sentinel",
			text: "The migration is done but the tests are not written yet.",
			done: 1, tot: 2, want: EndContinue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ClassifyEnd(tc.text, tc.done, tc.tot)
			if got != tc.want {
				t.Errorf("ClassifyEnd = %q, want %q", got, tc.want)
			}
			if tc.wantReason != "" && reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
			if tc.want != EndBlocked && reason != "" {
				t.Errorf("reason = %q, want empty for a non-blocked end", reason)
			}
		})
	}
}

// TestClassifyEndBlockedWithoutReasonKeepsTheLine: a sentinel with an empty tail
// must still carry SOMETHING into run_error — "blocked, reason unknown" is
// useless to an operator.
func TestClassifyEndBlockedWithoutReasonKeepsTheLine(t *testing.T) {
	got, reason := ClassifyEnd("PHASE BLOCKED:", 0, 2)
	if got != EndBlocked {
		t.Fatalf("ClassifyEnd = %q, want blocked", got)
	}
	if reason != "PHASE BLOCKED:" {
		t.Errorf("reason = %q, want the whole line as a fallback", reason)
	}
}

func TestHasDoneLine(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"PHASE DONE", true},
		{"PLAN DONE", true},
		{"work log\n\nDONE", true},
		{"DONE.", true},
		{"**PLAN DONE**", true},
		{"the work is done", false},
		{"DONE: with caveats", false},
		{"", false},
	} {
		if got := HasDoneLine(tc.text); got != tc.want {
			t.Errorf("HasDoneLine(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestMaxContinuationsIsTwo pins the money bound. It is asserted rather than
// described because raising it is a cost decision, not a refactor.
func TestMaxContinuationsIsTwo(t *testing.T) {
	if MaxContinuations != 2 {
		t.Fatalf("MaxContinuations = %d, want 2", MaxContinuations)
	}
}

func TestContinuationMessage(t *testing.T) {
	msg := ContinuationMessage(
		[]string{"write the migration", "add the API route"},
		"PHASE BLOCKED",
		4*time.Minute, time.Hour)

	for _, want := range []string{
		"2 acceptance criteria are still unticked",
		"- write the migration",
		"- add the API route",
		"end with PHASE BLOCKED: <reason>",
		"elapsed 240s / 3600s",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("continuation message missing %q:\n%s", want, msg)
		}
	}
}

// TestContinuationMessageCapsTheList: a plan run can have a hundred unticked
// criteria; the nudge must not become another wall of context.
func TestContinuationMessageCapsTheList(t *testing.T) {
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, "criterion")
	}
	msg := ContinuationMessage(many, "PLAN BLOCKED at phase <n>", time.Minute, time.Hour)
	if !strings.Contains(msg, "40 acceptance criteria are still unticked") {
		t.Errorf("the count must be the TRUE count, not the shown count:\n%s", msg)
	}
	if n := strings.Count(msg, "\n- criterion"); n != continuationCriteriaCap {
		t.Errorf("listed %d criteria, want the %d cap", n, continuationCriteriaCap)
	}
	if !strings.Contains(msg, "and 28 more in the document") {
		t.Errorf("the elision must say how many were withheld:\n%s", msg)
	}
}

func TestElapsedLineOmitsAnUnknownBudget(t *testing.T) {
	if got := ElapsedLine(90*time.Second, 0); got != "elapsed 90s\n" {
		t.Errorf("ElapsedLine with no timeout = %q", got)
	}
}
