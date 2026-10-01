package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/channelprobe"
)

// hermeticDoctor points every input the doctor reads at temp state.
func hermeticDoctor(t *testing.T) (home, proj string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SWARMERY_SECRETS_DIR", filepath.Join(root, "secrets"))
	t.Setenv("SWARMERY_LAUNCH_PATH", "")
	t.Setenv("SWARMERY_CLAUDE_BIN", filepath.Join(root, "no-claude"))
	t.Setenv("SWARMERY_PROBES_DIR", filepath.Join(root, "probes"))
	t.Setenv("SWARMERY_DOCTOR_DIR", filepath.Join(root, "doctor"))
	t.Setenv("SWARMERY_PROBE_SCRIPT", "")
	proj = filepath.Join(root, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, proj
}

// The three arms parse; a report WITH findings still exits 0 (nil error).
func TestAccountDoctorArms(t *testing.T) {
	_, proj := hermeticDoctor(t)
	for _, args := range [][]string{
		{"--fast", "--json", "--path", proj},
		{"--json", "--path", proj},
		{"--fast", "--json", "--timeout", "2.5s", "--no-record", "--path", proj},
	} {
		var out bytes.Buffer
		if err := accountDoctor(args, &out); err != nil {
			t.Fatalf("accountDoctor(%v): %v", args, err)
		}
		var rep map[string]any
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("%v: not one JSON object: %v", args, err)
		}
		if _, ok := rep["defaultProfile"].(map[string]any); !ok {
			t.Errorf("%v: no defaultProfile for a default-bound path", args)
		}
	}
	// --probe with no harness anywhere is an error naming both sources — not a
	// usage error. (A tree where `make build` snapshotted the harness embeds one.)
	if channelprobe.HasHarness() {
		return
	}
	var out bytes.Buffer
	err := accountDoctor([]string{"--probe", "--path", proj}, &out)
	if err == nil || isUsage(err) || !strings.Contains(err.Error(), "SWARMERY_PROBE_SCRIPT") {
		t.Errorf("--probe without a harness = %v", err)
	}
}

// `account env` still prints zero or one line after this file's changes.
func TestAccountEnvStillOneLine(t *testing.T) {
	home, proj := hermeticDoctor(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude-work", "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.local.json"),
		[]byte(`{"swarmery":{"claudeAccount":"work"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := accountEnv([]string{"--path", proj}, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "CLAUDE_CONFIG_DIR=") {
		t.Errorf("account env = %q, want exactly one CLAUDE_CONFIG_DIR= line", out.String())
	}
	out.Reset()
	if err := accountEnv([]string{"--path", t.TempDir()}, &out); err != nil || out.Len() != 0 {
		t.Errorf("unbound account env = %q %v, want zero lines", out.String(), err)
	}
}

// failingWriter is a stdout whose reader is gone (a hook watchdog killed it).
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// The first-sight ledger is recorded only once the report is out: when the
// report cannot be written, the path stays unrecorded and the warning is
// shown next time instead of never.
func TestAccountDoctorRecordsFirstSightOnlyAfterTheReport(t *testing.T) {
	_, proj := hermeticDoctor(t)
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.local.json"),
		[]byte(`{"swarmery":{"estate":"e1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(proj, "fresh")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(os.Getenv("SWARMERY_DOCTOR_DIR"), "estate-seen.json")
	args := []string{"--fast", "--json", "--path", fresh}

	if err := accountDoctor(args, failingWriter{}); err == nil {
		t.Fatal("a report that could not be written returned nil")
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Errorf("the path was recorded although its report never went out (stat: %v)", err)
	}

	var out bytes.Buffer
	if err := accountDoctor(args, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"first-sight"`) {
		t.Fatalf("no first-sight finding in %s", out.String())
	}
	if _, err := os.Stat(ledger); err != nil {
		t.Errorf("the path was not recorded once its report went out: %v", err)
	}
}
