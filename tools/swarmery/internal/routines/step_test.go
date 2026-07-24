package routines

import "testing"

func TestValidateSteps(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty string", "", true},
		{"empty array", "[]", true},
		{"bad json", "{not json", true},
		{"unknown type", `[{"type":"nope","name":"x"}]`, true},
		{"command ok", `[{"type":"command","name":"c","command":"echo hi"}]`, false},
		{"command missing command", `[{"type":"command","name":"c"}]`, true},
		{"ai-prompt ok", `[{"type":"ai-prompt","name":"a","prompt":"do it"}]`, false},
		{"ai-prompt missing prompt", `[{"type":"ai-prompt","name":"a"}]`, true},
		{"create-task ok", `[{"type":"create-task","name":"t","taskTitle":"T","taskPrompt":"P"}]`, false},
		{"create-task missing title", `[{"type":"create-task","name":"t","taskPrompt":"P"}]`, true},
		{"create-task missing prompt", `[{"type":"create-task","name":"t","taskTitle":"T"}]`, true},
		{"mixed ok", `[{"type":"command","name":"c","command":"ls"},{"type":"ai-prompt","name":"a","prompt":"p"}]`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateSteps(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateSteps(%q) err=%v, wantErr=%v", tc.raw, err, tc.wantErr)
			}
		})
	}
}

func TestMarshalStepsRoundTrip(t *testing.T) {
	steps := []Step{
		{Type: StepCommand, Name: "c", Command: "echo hi", TimeoutSec: 30, ContinueOnFailure: true},
		{Type: StepAIPrompt, Name: "a", Prompt: "summarize", Model: "sonnet"},
		{Type: StepCreateTask, Name: "t", TaskTitle: "Title", TaskPrompt: "Body", BoardColumn: "todo"},
	}
	raw, err := MarshalSteps(steps)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ValidateSteps(raw)
	if err != nil {
		t.Fatalf("round-trip validate: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("round-trip len = %d, want 3", len(got))
	}
	if got[0].Command != "echo hi" || !got[0].ContinueOnFailure || got[0].TimeoutSec != 30 {
		t.Errorf("command step lost fields: %+v", got[0])
	}
	if got[1].Model != "sonnet" || got[1].Prompt != "summarize" {
		t.Errorf("ai-prompt step lost fields: %+v", got[1])
	}
	if got[2].TaskTitle != "Title" || got[2].BoardColumn != "todo" {
		t.Errorf("create-task step lost fields: %+v", got[2])
	}
}
