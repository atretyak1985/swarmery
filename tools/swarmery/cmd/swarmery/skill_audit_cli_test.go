package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

// skillAuditDB writes a migrated database loaded with the sysscan skill-audit
// fixtures and returns its path (the CLI opens it itself, read-only).
func skillAuditDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, f := range []string{"skills.sql", "events.sql"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "skillaudit", f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("load %s: %v", f, err)
		}
	}
	return path
}

func TestSkillAuditJSON(t *testing.T) {
	dbPath := skillAuditDB(t)
	var out bytes.Buffer
	if err := skillAudit([]string{"--db", dbPath, "--json", "--similarity", "0.8", "--p95", "2"}, &out); err != nil {
		t.Fatalf("skill-audit: %v", err)
	}
	var rep sysscan.SkillAudit
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if rep.SimilarityThreshold != 0.8 || rep.SessionSkillLoadP95 != 2 || rep.WindowDays != 30 {
		t.Errorf("thresholds = %.2f / %d / %dd, want the flags and the 30-day window",
			rep.SimilarityThreshold, rep.SessionSkillLoadP95, rep.WindowDays)
	}
	if len(rep.UnusedSkills) != 2 || len(rep.UnusedAgents) != 1 || len(rep.SimilarPairs) != 1 {
		t.Errorf("unused skills %d, agents %d, pairs %d — want 2, 1, 1",
			len(rep.UnusedSkills), len(rep.UnusedAgents), len(rep.SimilarPairs))
	}
	if len(rep.SkillLoad) != 2 || rep.SkillLoad[0].Slug != "alpha" || !rep.SkillLoad[0].OverLimit {
		t.Errorf("p95 table = %+v, want alpha first and over the limit", rep.SkillLoad)
	}
	// The JSON is the routine's contract with the model: every list key is
	// present (an empty list, never null) so the prompt can rely on it.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"unusedAgents", "unusedSkills", "usedSkills", "similarPairs", "skillLoad"} {
		if v, ok := raw[k]; !ok || string(v) == "null" {
			t.Errorf("key %s missing or null", k)
		}
	}
}

func TestSkillAuditText(t *testing.T) {
	dbPath := skillAuditDB(t)
	var out bytes.Buffer
	if err := skillAudit([]string{"--db", dbPath, "--similarity", "0.8", "--p95", "2"}, &out); err != nil {
		t.Fatalf("skill-audit: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"unused agents (1)", "legacy-bot",
		"unused skills (2)", "never-used", "old-helper",
		"similar descriptions (1)", "pdf-export  ~  pdf-exporter",
		"skills per session (2)", "alpha", "over limit",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text report lacks %q:\n%s", want, text)
		}
	}
}

func TestSkillAuditUsage(t *testing.T) {
	for _, args := range [][]string{{"extra"}, {"--similarity", "1.5"}, {"--window", "0"}, {"--nope"}} {
		if err := skillAudit(args, &bytes.Buffer{}); !isUsage(err) {
			t.Errorf("args %v: err = %v, want a usage error", args, err)
		}
	}
}
