package improve

import (
	"errors"
	"testing"
)

// A registered agent appears in the set and yields a full evidence bundle.
func TestEvidenceAndRegistrySetPresent(t *testing.T) {
	db := openDB(t)
	const body = "---\nname: tech-lead\n---\nbody"
	seedAgent(t, db, 1, "tech-lead", "local", "/repo/.claude/agents/tech-lead.md", body)
	s := &Service{DB: db}

	set, err := s.RegistryAgentSet()
	if err != nil {
		t.Fatalf("RegistryAgentSet: %v", err)
	}
	if _, ok := set["tech-lead"]; !ok {
		t.Fatalf("registry set %v missing tech-lead", set)
	}

	ev, err := s.Evidence("tech-lead")
	if err != nil {
		t.Fatalf("Evidence: %v", err)
	}
	if ev.Bundle == "" {
		t.Error("Evidence returned an empty bundle")
	}
	if ev.AgentPath != "/repo/.claude/agents/tech-lead.md" {
		t.Errorf("AgentPath = %q, want the seeded path", ev.AgentPath)
	}
	if ev.BaseSHA256 == "" {
		t.Error("Evidence returned an empty BaseSHA256")
	}
}

// A built-in agent (no live registry row) is absent from the set, and Evidence
// returns ErrAgentNotFound.
func TestEvidenceAndRegistrySetAbsent(t *testing.T) {
	db := openDB(t)
	seedAgent(t, db, 1, "tech-lead", "local", "/x/tech-lead.md", "body")
	s := &Service{DB: db}

	set, err := s.RegistryAgentSet()
	if err != nil {
		t.Fatalf("RegistryAgentSet: %v", err)
	}
	if _, ok := set["debugger"]; ok {
		t.Errorf("registry set %v unexpectedly contains built-in debugger", set)
	}

	if _, err := s.Evidence("debugger"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("Evidence(debugger) err = %v, want ErrAgentNotFound", err)
	}
}
