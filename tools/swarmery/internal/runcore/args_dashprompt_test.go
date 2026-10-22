package runcore

import (
	"slices"
	"testing"
)

// A prompt is data. The claude CLI's -p is a boolean (--print) and the prompt
// is a positional argument, so a prompt that begins with a dash and sits among
// the flags is parsed as an option. The prompt therefore goes last, after the
// end-of-options marker, with every flag — the variadic --disallowedTools
// included — before that marker.
func assertPromptIsTrailingData(t *testing.T, args []string, prompt string) {
	t.Helper()
	n := len(args)
	if n < 2 || args[n-2] != "--" || args[n-1] != prompt {
		t.Fatalf("argv must end with `-- <prompt>`: %q", args)
	}
	if i := slices.Index(args, "--"); i != n-2 {
		t.Errorf("exactly one end-of-options marker, right before the prompt: %q", args)
	}
	if slices.Contains(args[:n-2], prompt) {
		t.Errorf("prompt %q appears among the flags: %q", prompt, args)
	}
}

func TestArgsDashLedPromptStaysData(t *testing.T) {
	for _, prompt := range []string{"--add-dir=/", "--version", "-x", "mcp"} {
		t.Run(prompt, func(t *testing.T) {
			got := Args(Spec{
				Prompt:         prompt,
				SessionUUID:    "uuid-1",
				Model:          "m",
				PermissionMode: "acceptEdits",
				ExtraArgs:      []string{"--disallowedTools", "Edit,Write"},
			})
			assertPromptIsTrailingData(t, got, prompt)
			if got[0] != "-p" || got[1] != "--session-id" || got[2] != "uuid-1" {
				t.Errorf("argv head = %q, want `-p --session-id <uuid>`", got[:3])
			}
		})
	}
}

func TestArgsResumeDashLedPromptStaysData(t *testing.T) {
	got := Args(Spec{Prompt: "--add-dir=/", SessionUUID: "uuid-1", Resume: true, Effort: "high"})
	assertPromptIsTrailingData(t, got, "--add-dir=/")
	if got[0] != "-r" || got[1] != "uuid-1" || got[2] != "-p" {
		t.Errorf("resume argv head = %q, want `-r <uuid> -p`", got[:3])
	}
}
