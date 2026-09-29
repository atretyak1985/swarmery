package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAccountDoctorFastJSONEstateWithoutStore drives the CLI surface end to end
// over a hermetic tree (HOME, secrets dir): an estate whose store file does not
// exist, with no pack referencing any ${VAR}, is ONE JSON object reporting a
// healthy zero — and never an error (D2a).
func TestAccountDoctorFastJSONEstateWithoutStore(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SWARMERY_SECRETS_DIR", filepath.Join(root, "secrets"))
	t.Setenv("SWARMERY_LAUNCH_PATH", "")
	proj := filepath.Join(root, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	binding := `{"swarmery":{"claudeAccount":"default","estate":"phase4x"}}`
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.local.json"), []byte(binding), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := accountDoctor([]string{"--fast", "--json", "--path", proj}, &out); err != nil {
		t.Fatalf("accountDoctor: %v", err)
	}
	if n := strings.Count(strings.TrimRight(out.String(), "\n"), "\n"); n != 0 {
		t.Errorf("stdout has %d extra line(s), want exactly one JSON object", n)
	}
	var rep map[string]any
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not one JSON object: %v", err)
	}
	if rep["credentials"] != float64(0) || rep["credentialStore"] != "" || rep["estate"] != "phase4x" || rep["estateRoot"] == "" {
		t.Errorf("report = %v", rep)
	}
	// findings is non-empty now (estate-unanchored per D5, the fixed info
	// findings); the escalation lists stay empty arrays, never null.
	if _, ok := rep["findings"].([]any); !ok {
		t.Errorf("findings = %#v, want an array", rep["findings"])
	}
	for _, k := range []string{"varsExpected", "varsPresent", "varsMissing", "staleDuplicates"} {
		arr, ok := rep[k].([]any)
		if !ok || len(arr) != 0 {
			t.Errorf("%s = %#v, want an empty array (never null)", k, rep[k])
		}
	}

	out.Reset()
	if err := accountDoctor([]string{"--fast", "--path", proj}, &out); err != nil || !strings.Contains(out.String(), "estate:       phase4x") {
		t.Errorf("text form = %q, %v", out.String(), err)
	}
}

// A stray argument, an unknown flag, a malformed duration and --fast with
// --probe are usage errors (exit 2) and write nothing to stdout.
func TestAccountDoctorUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"--fast", "extra"}, {"--fast", "--probe"}, {"--bogus"}, {"--timeout", "soon"}, {"--timeout", "-1s"}} {
		var out bytes.Buffer
		if err := accountDoctor(args, &out); !isUsage(err) {
			t.Errorf("accountDoctor(%v) = %v, want the usage error", args, err)
		}
		if out.Len() != 0 {
			t.Errorf("accountDoctor(%v) wrote to stdout on a usage error: %q", args, out.String())
		}
	}
}
