package probe

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// The judge prompt must not say which answer is the edited one. These words
// would each give it away (or name the machinery); none may appear anywhere in
// a rendered prompt built from neutral task and answer text.
var hintWords = regexp.MustCompile(`(?i)\b(base|baseline|variant|proposal|proposed|patch(ed)?|diff|candidate|control|treatment|new version|old version|current version|first answer|second answer)\b|\.md\b|plugins/|agents/|latency|duration|seconds|\bms\b|cost|usd|\$`)

func TestJudgePromptCarriesNoSideHint(t *testing.T) {
	pr := RenderJudgePrompt("Summarize the release notes.", "Answer one.", "Answer two.")
	if m := hintWords.FindAllString(pr, -1); len(m) > 0 {
		t.Fatalf("rendered judge prompt leaks a side/ordering/timing hint: %q\n---\n%s", m, pr)
	}
	for _, must := range []string{
		JudgePreamble,
		`<answer id="A">` + "\nAnswer one.\n</answer>",
		`<answer id="B">` + "\nAnswer two.\n</answer>",
		"valid and expected result",
		"1. Delivery", "2. Fidelity", "3. Internal consistency", "4. Usable as-is", "5. Finish",
		"at most 6 short lines", "CHOICE: TIE",
	} {
		if !strings.Contains(pr, must) {
			t.Errorf("judge prompt lacks %q", must)
		}
	}
	if strings.Index(pr, "1. Delivery") > strings.Index(pr, "5. Finish") {
		t.Error("decision order changed")
	}
}

// The prompt the Service actually sends, end to end through Run: the fakes'
// outputs are named base-output/variant-output on purpose, so strip exactly
// those two payloads and nothing else may hint.
func TestServiceJudgePromptCarriesNoSideHint(t *testing.T) {
	db := openDB(t)
	id := insertProposal(t, db, "proposed", "agent", target, "", demoDiff)
	s, _, _, judge := newService(t, db)
	p, _ := s.LoadProposal(id)
	if _, err := s.Run(context.Background(), "j", p); err != nil {
		t.Fatal(err)
	}
	if len(judge.prompts) == 0 {
		t.Fatal("judge never ran")
	}
	for _, pr := range judge.prompts {
		neutral := strings.NewReplacer("base-output", "X", "variant-output", "Y").Replace(pr)
		if m := hintWords.FindAllString(neutral, -1); len(m) > 0 {
			t.Errorf("service judge prompt leaks %q", m)
		}
		if strings.Contains(pr, "demo") && !strings.Contains(pr, "task for demo") {
			t.Error("agent name leaked outside the task text")
		}
	}
}

func TestRenderKeepsPlaceholdersInsideTextLiteral(t *testing.T) {
	pr := RenderJudgePrompt("task {{A}}", "a says {{B}}", "b")
	if !strings.Contains(pr, "task {{A}}") || !strings.Contains(pr, "a says {{B}}") {
		t.Errorf("single-pass replacement broke: %s", pr)
	}
}

func TestParseVerdict(t *testing.T) {
	for _, tc := range []struct {
		raw, choice, reason string
		err                 bool
	}{
		{"A delivers.\nCHOICE: A", "A", "A delivers.", false},
		{"x\n**CHOICE: b**\n", "B", "x", false},
		{"CHOICE: A\nchanged my mind\nCHOICE: TIE", "TIE", "CHOICE: A\nchanged my mind", false},
		{"1\n2\n3\n4\n5\n6\n7\n8\nChoice: B", "B", "3\n4\n5\n6\n7\n8", false},
		{"no verdict", "", "", true},
		{"CHOICE: C", "", "", true},
	} {
		c, r, err := ParseVerdict(tc.raw)
		if (err != nil) != tc.err || c != tc.choice || r != tc.reason {
			t.Errorf("%q → (%q, %q, %v)", tc.raw, c, r, err)
		}
	}
}

func TestJudgeModelKnob(t *testing.T) {
	t.Setenv(JudgeModelEnv, "")
	if JudgeModel() != "claude-sonnet-5" || DefaultJudgeModel != "claude-sonnet-5" {
		t.Errorf("default judge model = %s", JudgeModel())
	}
	t.Setenv(JudgeModelEnv, " claude-opus-5-5 ")
	if JudgeModel() != "claude-opus-5-5" {
		t.Errorf("env override = %s", JudgeModel())
	}
}

func TestParseCLIJSON(t *testing.T) {
	out, usd, err := parseCLIJSON(`{"type":"result","result":"ok\nCHOICE: A","is_error":false,"total_cost_usd":0.0123}`)
	if err != nil || out != "ok\nCHOICE: A" || usd != 0.0123 {
		t.Errorf("(%q, %v, %v)", out, usd, err)
	}
	if _, usd, err := parseCLIJSON(`{"is_error":true,"subtype":"error_max_turns","total_cost_usd":0.5}`); err == nil || usd != 0.5 {
		t.Errorf("error result: %v %v", usd, err)
	}
	if _, _, err := parseCLIJSON("not json"); err == nil {
		t.Error("garbage accepted")
	}
}
