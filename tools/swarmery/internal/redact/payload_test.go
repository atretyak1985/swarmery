package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/redact/redacttest"
)

func TestPayloadJSONKeepsCleanBytes(t *testing.T) {
	doc := `{"b": 1,   "a": {"command": "ls -la"}, "n": 12345678901234567890}`
	got, changed := PayloadJSON("Bash", doc)
	if changed || got != doc {
		t.Errorf("clean payload rewritten: changed=%v %q", changed, got)
	}
}

func TestPayloadJSONMasksLeavesAndStaysJSON(t *testing.T) {
	secrets := redacttest.Joined()
	in, _ := json.Marshal(map[string]any{
		"input":  map[string]any{"command": secrets},
		"result": map[string]any{"stdout": secrets, "n": 3},
		"raw":    json.RawMessage(`{"x": ` + string(mustJSON(t, secrets)) + `}`),
	})
	got, changed := PayloadJSON("Bash", string(in))
	if !changed {
		t.Fatal("payload with secrets reported unchanged")
	}
	if !json.Valid([]byte(got)) {
		t.Fatalf("redacted payload is not JSON: %s", got)
	}
	for _, s := range redacttest.Samples() {
		if strings.Contains(got, s.Needle) {
			t.Errorf("%s survived PayloadJSON", s.Class)
		}
	}
	if again, changed := PayloadJSON("Bash", got); changed || again != got {
		t.Errorf("PayloadJSON is not idempotent")
	}
}

func TestPayloadCodeFieldsUseLiteral(t *testing.T) {
	code := redacttest.SourceAssignment()
	github := redacttest.Samples()[1].Value
	in := map[string]any{
		"file_path":   "/repo/main.go",
		"new_string":  code + github,
		"description": code,
	}
	out, changed := Value("Edit", in)
	if !changed {
		t.Fatal("Edit payload with a token reported unchanged")
	}
	m := out.(map[string]any)
	if got := m["new_string"].(string); got != code+Mark {
		t.Errorf("new_string = %q, want the code kept and the token masked", got)
	}
	if got := m["description"].(string); got == code {
		t.Errorf("a non-code field of an Edit skipped the contextual rules")
	}
	if in["new_string"] != code+github {
		t.Errorf("Value modified its input")
	}
	// The same field of a tool that does not edit files gets every rule.
	out, _ = Value("Bash", map[string]any{"new_string": code})
	if out.(map[string]any)["new_string"] == code {
		t.Errorf("a non-editing tool's field skipped the contextual rules")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
