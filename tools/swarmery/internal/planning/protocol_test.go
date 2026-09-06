package planning

import (
	"strings"
	"testing"
)

// minimalQuestion returns a JSON string for a valid PlanningQuestion payload.
func minimalQuestion(qType string) string {
	return `{"type":"question","data":{"id":"q1","type":"` + qType + `","question":"Which approach?","options":[{"id":"opt-a","label":"Option A"},{"id":"opt-b","label":"Option B"}]}}`
}

func TestParseTurn(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		wantNilQ     bool   // true ⇒ Question must be nil
		wantQID      string // non-empty ⇒ check Question.ID
		wantQType    string // non-empty ⇒ check Question.Type
		reasoningHas string // non-empty ⇒ Reasoning must contain this
		reasoningNot string // non-empty ⇒ Reasoning must NOT contain this (block removed)
		wantIsOther  bool   // check that an isOther option is present
	}{
		{
			name:         "clean fenced block",
			text:         "Here is the analysis.\n```json\n" + minimalQuestion("single_select") + "\n```",
			wantQID:      "q1",
			wantQType:    "single_select",
			reasoningHas: "Here is the analysis.",
			reasoningNot: "```json",
		},
		{
			name:         "bare JSON without fence",
			text:         minimalQuestion("single_select"),
			wantQID:      "q1",
			reasoningNot: `"type":"question"`,
		},
		{
			name:         "prose before and after block",
			text:         "Intro prose.\n```json\n" + minimalQuestion("single_select") + "\n```\nTrailing prose.",
			wantQID:      "q1",
			reasoningHas: "Trailing prose.",
		},
		{
			name: "truncated block repaired",
			// Unclosed brace — repairJSON should close it and the parse should succeed.
			text:      "Analysis.\n```json\n" + `{"type":"question","data":{"id":"q-trunc","type":"single_select","question":"Pick one?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"` + "\n```",
			wantQID:   "q-trunc",
			wantQType: "single_select",
		},
		{
			name: "trailing comma repaired",
			text: "Analysis.\n```json\n" +
				`{"type":"question","data":{"id":"q-comma","type":"single_select","question":"Choose?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"},],"runningPlan":{"title":"T","description":"D"},}}` +
				"\n```",
			wantQID:   "q-comma",
			wantQType: "single_select",
		},
		{
			name:         "no JSON at all fallback",
			text:         "Just some plain text with no JSON here.",
			wantNilQ:     true,
			reasoningHas: "Just some plain text",
		},
		{
			name: "wrong type field rejected",
			text: "```json\n" +
				`{"type":"answer","data":{"id":"q1","type":"single_select","question":"?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}}` +
				"\n```",
			wantNilQ: true,
		},
		{
			name: "fewer than 2 options rejected",
			text: "```json\n" +
				`{"type":"question","data":{"id":"q1","type":"single_select","question":"?","options":[{"id":"a","label":"A"}]}}` +
				"\n```",
			wantNilQ: true,
		},
		{
			name: "multi_select with isOther option",
			text: "```json\n" +
				`{"type":"question","data":{"id":"q-multi","type":"multi_select","question":"Select all that apply?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"},{"id":"other","label":"Other","isOther":true}]}}` +
				"\n```",
			wantQID:     "q-multi",
			wantQType:   "multi_select",
			wantIsOther: true,
		},
		{
			name: "trailing prose inside fence repaired",
			// Model emits a balanced JSON object followed by trailing prose inside
			// the ```json fence. repairJSON strategy (1) must truncate after the
			// last balanced closing brace so json.Unmarshal can succeed.
			text: "Here is my reasoning.\n```json\n" +
				`{"type":"question","data":{"id":"q-prose","type":"single_select","question":"Which approach?","options":[{"id":"a","label":"Option A"},{"id":"b","label":"Option B"}]}}` +
				"\nHope this helps!\n```",
			wantQID:      "q-prose",
			wantQType:    "single_select",
			reasoningHas: "Here is my reasoning.",
		},
		{
			name: "unicode and Ukrainian text intact",
			text: "Аналіз зроблено.\n```json\n" +
				`{"type":"question","data":{"id":"uk-q","type":"single_select","question":"Який підхід обрати?","description":"Опис питання","options":[{"id":"opt-a","label":"Варіант А","pros":["Швидко"],"cons":["Складно"]},{"id":"opt-b","label":"Варіант Б"}]}}` +
				"\n```",
			wantQID:      "uk-q",
			reasoningHas: "Аналіз зроблено.",
		},
		{
			// stripTrailingCommas must not corrupt a comma inside a string literal.
			// The option label contains "pick: [a, ]" — the comma before ] is INSIDE
			// a JSON string and must survive verbatim.  The trailing comma after the
			// last option IS a real trailing comma that must be stripped so
			// json.Unmarshal succeeds.  The description field also contains ", }" to
			// cover the brace variant of the same bug.
			name: "comma inside string literal preserved by stripTrailingCommas",
			text: "```json\n" +
				`{"type":"question","data":{"id":"q-str","type":"single_select","question":"Pick?","description":"example: {\"a\", }","options":[{"id":"a","label":"pick: [a, ]"},{"id":"b","label":"B"},]}}` +
				"\n```",
			wantQID:   "q-str",
			wantQType: "single_select",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTurn(tc.text)

			if tc.wantNilQ {
				if got.Question != nil {
					t.Errorf("want Question=nil, got id=%q", got.Question.ID)
				}
			} else {
				if got.Question == nil {
					t.Fatalf("want Question != nil, got nil (Reasoning=%q)", got.Reasoning)
				}
				if tc.wantQID != "" && got.Question.ID != tc.wantQID {
					t.Errorf("Question.ID: want %q, got %q", tc.wantQID, got.Question.ID)
				}
				if tc.wantQType != "" && got.Question.Type != tc.wantQType {
					t.Errorf("Question.Type: want %q, got %q", tc.wantQType, got.Question.Type)
				}
				if tc.wantIsOther {
					found := false
					for _, o := range got.Question.Options {
						if o.IsOther {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("want an isOther option in Question.Options, found none")
					}
				}
			}

			if tc.reasoningHas != "" && !strings.Contains(got.Reasoning, tc.reasoningHas) {
				t.Errorf("Reasoning should contain %q, got %q", tc.reasoningHas, got.Reasoning)
			}
			if tc.reasoningNot != "" && strings.Contains(got.Reasoning, tc.reasoningNot) {
				t.Errorf("Reasoning should NOT contain %q, got %q", tc.reasoningNot, got.Reasoning)
			}
		})
	}
}

// TestStripTrailingCommasStringAware verifies that commas inside JSON string
// literals are never dropped, even when they appear before whitespace + `}` or
// `]` — the exact pattern that the naive (non-string-aware) implementation
// silently corrupts.
func TestStripTrailingCommasStringAware(t *testing.T) {
	// A real trailing comma that SHOULD be removed + commas inside strings that
	// must survive.
	input := `{"options":[{"id":"a","label":"pick: [a, ]"},{"id":"b","label":"B"},]}`
	got := stripTrailingCommas(input)

	// The trailing comma after {"id":"b","label":"B"} must be gone so the JSON parses.
	if strings.Contains(got, `"B"},]`) {
		t.Errorf("real trailing comma was not removed; got: %s", got)
	}
	// The comma inside the string literal "pick: [a, ]" must be preserved.
	if !strings.Contains(got, `"pick: [a, ]"`) {
		t.Errorf("comma inside string literal was corrupted; got: %s", got)
	}

	// Brace variant: ", }" inside a string value must not be touched.
	input2 := `{"desc":"example: {\"a\", }","x":1}`
	got2 := stripTrailingCommas(input2)
	if !strings.Contains(got2, `"example: {\"a\", }"`) {
		t.Errorf("comma inside string literal (brace variant) was corrupted; got: %s", got2)
	}
}

// TestParseTurnStringLiteralComma confirms that ParseTurn correctly round-trips
// an option label and description that contain ", ]" / ", }" when the block
// also has a real trailing comma requiring repair.
func TestParseTurnStringLiteralComma(t *testing.T) {
	text := "```json\n" +
		`{"type":"question","data":{"id":"q-str","type":"single_select","question":"Pick?","description":"example: {\"a\", }","options":[{"id":"a","label":"pick: [a, ]"},{"id":"b","label":"B"},]}}` +
		"\n```"
	got := ParseTurn(text)
	if got.Question == nil {
		t.Fatalf("want Question != nil, got nil (Reasoning=%q)", got.Reasoning)
	}
	if got.Question.Description != `example: {"a", }` {
		t.Errorf("Description corrupted: want %q, got %q", `example: {"a", }`, got.Question.Description)
	}
	if len(got.Question.Options) == 0 || got.Question.Options[0].Label != "pick: [a, ]" {
		label := ""
		if len(got.Question.Options) > 0 {
			label = got.Question.Options[0].Label
		}
		t.Errorf("Option[0].Label corrupted: want %q, got %q", "pick: [a, ]", label)
	}
}

// TestParseTurnReasoningTrim checks that Reasoning is always trimmed.
func TestParseTurnReasoningTrim(t *testing.T) {
	text := "  \n  plain text  \n  "
	got := ParseTurn(text)
	if got.Reasoning != "plain text" {
		t.Errorf("Reasoning not trimmed: %q", got.Reasoning)
	}
}

// TestParseTurnRunningPlan checks that RunningPlan is preserved when present.
func TestParseTurnRunningPlan(t *testing.T) {
	text := "```json\n" +
		`{"type":"question","data":{"id":"q1","type":"single_select","question":"?","options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"runningPlan":{"title":"My Plan","description":"A description","suggestedSize":"M"}}}` +
		"\n```"
	got := ParseTurn(text)
	if got.Question == nil {
		t.Fatal("want Question != nil")
	}
	if got.Question.RunningPlan == nil {
		t.Fatal("want RunningPlan != nil")
	}
	if got.Question.RunningPlan.Title != "My Plan" {
		t.Errorf("RunningPlan.Title: want %q, got %q", "My Plan", got.Question.RunningPlan.Title)
	}
	if got.Question.RunningPlan.SuggestedSize != "M" {
		t.Errorf("RunningPlan.SuggestedSize: want %q, got %q", "M", got.Question.RunningPlan.SuggestedSize)
	}
}

// The planner's lean survives the parse: "recommended":true on one option is
// what the dashboard's highlight reads, and its absence stays false (omitempty).
func TestParseTurnRecommendedOption(t *testing.T) {
	text := "```json\n" +
		`{"type":"question","data":{"id":"q1","type":"single_select","question":"?","options":[{"id":"a","label":"A","recommended":true},{"id":"b","label":"B"},{"id":"other","label":"Other","isOther":true}]}}` +
		"\n```"
	got := ParseTurn(text)
	if got.Question == nil {
		t.Fatal("want Question != nil")
	}
	if !got.Question.Options[0].Recommended {
		t.Error("option a: want Recommended")
	}
	if got.Question.Options[1].Recommended {
		t.Error("option b: want not Recommended")
	}
}
