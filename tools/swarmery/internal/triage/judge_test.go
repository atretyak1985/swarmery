package triage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func judgeItem() Item {
	return Item{Kind: "friction", Instruction: "Classify this friction.", Evidence: "tool X failed twice",
		Parts: []Part{{Ref: "f1", Label: "first", Allowed: []string{"noise", "fixable"}}, {Ref: "f2", Allowed: []string{"noise"}}}}
}

func cannedJudge(out string, err error) (ClaudeJudge, *string) {
	var got string
	return ClaudeJudge{Model: "m", Run: func(_ context.Context, prompt string) (string, error) {
		got = prompt
		return out, err
	}}, &got
}

func TestJudgeParsesValidEnvelope(t *testing.T) {
	out := `{"type":"result","subtype":"success","is_error":false,` +
		`"result":"{\"values\":{\"f1\":\"noise\",\"f2\":\"noise\"},\"reason\":\"flaky\",\"payload\":{\"k\":1}}",` +
		`"session_id":"abc-123","total_cost_usd":0.042}`
	j, prompt := cannedJudge(out, nil)
	a, err := j.Judge(context.Background(), judgeItem())
	if err != nil {
		t.Fatal(err)
	}
	if a.Values["f1"] != "noise" || a.Values["f2"] != "noise" || a.Reason != "flaky" ||
		a.SessionUUID != "abc-123" || a.CostUSD != 0.042 || string(a.Payload) != `{"k":1}` {
		t.Fatalf("answer = %+v", a)
	}
	for _, want := range []string{"Classify this friction.", "tool X failed twice", `ref "f1" (first)`, `"fixable"`, `"skip"`} {
		if !strings.Contains(*prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, *prompt)
		}
	}
}

func TestJudgeProseAroundJSON(t *testing.T) {
	out := `{"result":"Sure! Here it is:\n{\"values\":{\"f1\":\"fixable\",\"f2\":\"noise\"},\"reason\":\"r\"}\nHope that helps {x}."}`
	j, _ := cannedJudge(out, nil)
	a, err := j.Judge(context.Background(), judgeItem())
	if err != nil || a.Values["f1"] != "fixable" || a.SessionUUID != "" || a.CostUSD != 0 {
		t.Fatalf("answer = %+v, %v", a, err)
	}
}

func TestJudgeMissingAndExtraRef(t *testing.T) {
	// A missing ref is not an error: the answer carries only the present one,
	// and the service skips the missing part.
	j, _ := cannedJudge(`{"result":"{\"values\":{\"f1\":\"noise\",\"f2\":\"  \"}}"}`, nil)
	if a, err := j.Judge(context.Background(), judgeItem()); err != nil || len(a.Values) != 1 || a.Values["f1"] != "noise" {
		t.Fatalf("missing ref = %+v, %v", a, err)
	}
	j, _ = cannedJudge(`{"result":"{\"values\":{\"f1\":\"noise\",\"f2\":\"noise\",\"zz\":\"x\"}}"}`, nil)
	a, err := j.Judge(context.Background(), judgeItem())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Values["zz"]; ok || len(a.Values) != 2 {
		t.Fatalf("extra ref kept: %v", a.Values)
	}
}

func TestJudgeErrorEnvelopeAndFailures(t *testing.T) {
	cases := map[string]struct {
		out string
		err error
	}{
		"is_error":  {out: `{"is_error":true,"result":"rate limited"}`},
		"empty":     {out: "  "},
		"no json":   {out: `{"result":"I cannot decide."}`},
		"bad json":  {out: `{"result":"{\"values\": oops"}`},
		"run error": {err: errors.New("spawn failed")},
	}
	for name, c := range cases {
		j, _ := cannedJudge(c.out, c.err)
		if _, err := j.Judge(context.Background(), judgeItem()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestJudgeTextOutputFallback(t *testing.T) {
	j, _ := cannedJudge(`{"values":{"f1":"noise","f2":"noise"},"reason":"plain"}`, nil)
	// A bare answer object is not an envelope with a result: it decodes as an
	// envelope with an empty result, which carries no answer.
	if _, err := j.Judge(context.Background(), judgeItem()); err == nil {
		t.Fatal("bare object without result accepted")
	}
	j, _ = cannedJudge("Answer: {\"values\":{\"f1\":\"noise\",\"f2\":\"noise\"}}", nil)
	if a, err := j.Judge(context.Background(), judgeItem()); err != nil || a.Values["f2"] != "noise" {
		t.Fatalf("text fallback = %+v, %v", a, err)
	}
}

func TestJudgeModel(t *testing.T) {
	if got := (ClaudeJudge{Model: "x"}).model(); got != "x" {
		t.Fatalf("explicit model = %q", got)
	}
	t.Setenv("SWARMERY_TRIAGE_MODEL", "env-model")
	if got := (ClaudeJudge{}).model(); got != "env-model" {
		t.Fatalf("env model = %q", got)
	}
	t.Setenv("SWARMERY_TRIAGE_MODEL", "")
	if got := (ClaudeJudge{}).model(); got == "" {
		t.Fatal("default model empty")
	}
}
