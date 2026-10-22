package playbooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDocPlaybook(t *testing.T) {
	for _, tc := range []struct {
		name, doc, want string
	}{
		{"own line", "# Phase 3\n**Playbook:** long-tests\n", "long-tests"},
		{"joined header", "# P\n**Repo:** `x` · **Playbook:** `Long-Tests` · **Depends on:** 1\n", "long-tests"},
		{"absent", "# P\nStatus: Pending\n", ""},
		{"inside a later section", "# P\n## Agent prompt\n**Playbook:** long-tests\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseDocPlaybook(tc.doc); got != tc.want {
				t.Errorf("ParseDocPlaybook = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPhasePlaybookReadsTheDoc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "phase-1-x.md")
	if err := os.WriteFile(path, []byte("# P\n**Playbook:** long-tests\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := PhasePlaybook(path); got != "long-tests" {
		t.Errorf("PhasePlaybook = %q, want long-tests", got)
	}
	if got := PhasePlaybook(filepath.Join(t.TempDir(), "missing.md")); got != "" {
		t.Errorf("unreadable doc: PhasePlaybook = %q, want \"\"", got)
	}
}
