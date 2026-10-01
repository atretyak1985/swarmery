package hookshim

import (
	"encoding/json"
	"testing"
)

func TestInjectSessionStartExtrasLaunchAccount(t *testing.T) {
	for dir, want := range map[string]string{
		"":                          "default",
		"/Users/u/.claude":          "default",
		"/Users/u/.claude-work":     "work",
		"/Users/u/.claude-science/": "science",
	} {
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		out, err := injectSessionStartExtras([]byte(`{"session_id":"s","cwd":"/p"}`))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		if m["launchAccount"] != want {
			t.Errorf("CLAUDE_CONFIG_DIR=%q: launchAccount = %v, want %q", dir, m["launchAccount"], want)
		}
		if m["session_id"] != "s" {
			t.Error("the original payload was not kept")
		}
	}
}
