package routines

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/sysscan"
)

const skillAuditSeed = "skill-audit.json"

func loadShipped(t *testing.T, name string) *SeedFile {
	t.Helper()
	sf, err := LoadSeedFile(filepath.Join("..", "..", "config", "routines", name))
	if err != nil {
		t.Fatalf("shipped routine %s: %v", name, err)
	}
	return sf
}

// Every file in config/routines/ is in ShippedSeedFiles and loads.
func TestShippedSeedFilesComplete(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "config", "routines", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, f := range files {
		onDisk = append(onDisk, filepath.Base(f))
	}
	listed := append([]string(nil), ShippedSeedFiles...)
	sort.Strings(onDisk)
	sort.Strings(listed)
	if !reflect.DeepEqual(onDisk, listed) {
		t.Errorf("config/routines = %v, ShippedSeedFiles = %v", onDisk, listed)
	}
	for _, name := range ShippedSeedFiles {
		loadShipped(t, name)
	}
}

// The weekly skill audit: Monday 09:00, three steps — the audit JSON, a haiku
// prompt that turns it into the report, a write into the framework's own
// workspace retro/ dir. Report only: no step deletes or merges anything.
func TestSkillAuditSeedFile(t *testing.T) {
	sf := loadShipped(t, skillAuditSeed)
	if sf.Name != "skill-audit-weekly" {
		t.Errorf("name = %q, want skill-audit-weekly: seeding matches on it", sf.Name)
	}
	if sf.Cron != "0 9 * * 1" {
		t.Errorf("cron = %q, want \"0 9 * * 1\" (Monday 09:00)", sf.Cron)
	}
	if _, ok := NextRun(sf.Cron, time.Now()); !ok {
		t.Errorf("cron %q does not parse", sf.Cron)
	}
	if sf.CatchUp != "run_one" || !sf.Enabled {
		t.Errorf("catchUp = %q enabled = %v, want run_one and enabled", sf.CatchUp, sf.Enabled)
	}
	if len(sf.Steps) != 3 {
		t.Fatalf("steps = %d, want 3 (audit, report, save)", len(sf.Steps))
	}
	wantTypes := []string{StepCommand, StepAIPrompt, StepCommand}
	for i, s := range sf.Steps {
		if s.Type != wantTypes[i] {
			t.Errorf("step %d type = %q, want %q", i, s.Type, wantTypes[i])
		}
		if s.TimeoutSec <= 0 || s.TimeoutSec >= sf.TimeoutSec {
			t.Errorf("step %d timeout %ds must be set and below the routine's %ds", i, s.TimeoutSec, sf.TimeoutSec)
		}
		if s.ContinueOnFailure {
			t.Errorf("step %d continues on failure: a failed audit must not produce a report", i)
		}
		for _, verb := range []string{"rm ", "--yes", "--apply", "delete"} {
			if strings.Contains(s.Command, verb) {
				t.Errorf("step %d command %q contains %q: the audit only reports", i, s.Command, verb)
			}
		}
	}
	if sf.Steps[0].Command != "swarmery skill-audit --json" {
		t.Errorf("audit step = %q", sf.Steps[0].Command)
	}
	ai := sf.Steps[1]
	if ai.Model != "haiku" {
		t.Errorf("report step model = %q, want haiku", ai.Model)
	}
	if !strings.Contains(ai.Prompt, PrevOutputPlaceholder) {
		t.Errorf("report prompt never receives the audit JSON (%s)", PrevOutputPlaceholder)
	}
	save := sf.Steps[2].Command
	if !strings.Contains(save, "$SWARMERY_PREV_OUTPUT") || !strings.Contains(save, "/swarmery/workspace/retro") ||
		!strings.Contains(save, sysscan.SkillAuditReportPrefix+"$(date +%F).md") {
		t.Errorf("save step does not write <workspace>/swarmery/workspace/retro/skill-audit-<date>.md: %q", save)
	}
	// The prompt asks for, and the save step checks, the same five headings
	// the API and the CLI document.
	for _, sec := range sysscan.SkillAuditSections {
		if !strings.Contains(ai.Prompt, "## "+sec+"\n") {
			t.Errorf("prompt does not ask for %q", "## "+sec)
		}
		if !strings.Contains(save, "'"+sec+"'") {
			t.Errorf("save step does not check %q", sec)
		}
	}
}

// Seeding registers the routine once: a second seed updates it in place.
func TestSkillAuditSeedRegisters(t *testing.T) {
	s, _ := newTestSvc(t, &stubRunner{}, &stubTasks{})
	sf := loadShipped(t, skillAuditSeed)
	r, created, err := s.Seed(sf, sql.NullInt64{})
	if err != nil || !created {
		t.Fatalf("first seed: created=%v err=%v", created, err)
	}
	if _, created, err := s.Seed(sf, sql.NullInt64{}); err != nil || created {
		t.Fatalf("second seed: created=%v err=%v, want an in-place update", created, err)
	}
	all, err := s.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != r.ID || all[0].CronExpr != "0 9 * * 1" || len(all[0].Steps) != 3 {
		t.Fatalf("routines after two seeds = %+v", all)
	}
	if all[0].Steps[1].Model != "haiku" {
		t.Errorf("stored report step model = %q, want haiku", all[0].Steps[1].Model)
	}
}

// fakeSwarmery puts a `swarmery` on PATH that prints a canned audit for
// `skill-audit --json` — the executor test runs the SHIPPED commands verbatim.
func fakeSwarmery(t *testing.T, auditJSON string) {
	t.Helper()
	bin := t.TempDir()
	payload := filepath.Join(bin, "audit.json")
	if err := os.WriteFile(payload, []byte(auditJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1 $2\" = \"skill-audit --json\" ] || { echo \"unexpected: $*\" >&2; exit 2; }\ncat '" + payload + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "swarmery"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// reportMarkdown is a model reply carrying the five sections, minus the ones
// named in drop.
func reportMarkdown(drop ...string) string {
	var b strings.Builder
	b.WriteString("# Skill audit 2026-10-26\n\nWindow 30 days; similarity 0.80; p95 limit 12.\n")
	for _, sec := range sysscan.SkillAuditSections {
		if len(drop) > 0 && drop[0] == sec {
			continue
		}
		b.WriteString("\n## " + sec + "\n\n- item\n")
	}
	return b.String()
}

func runSkillAuditRoutine(t *testing.T, reply string) (string, Run, []StepResult, *stubRunner) {
	t.Helper()
	ws := t.TempDir()
	t.Setenv("AGENT_WORKSPACE_ROOT", "")
	t.Setenv("SWARMERY_WORKSPACE_ROOT", ws)
	const auditJSON = `{"windowDays":30,"unusedSkills":[{"name":"never-used"}]}`
	fakeSwarmery(t, auditJSON)

	sr := &stubRunner{out: reply}
	s, _ := newTestSvc(t, sr, &stubTasks{})
	sf := loadShipped(t, skillAuditSeed)
	r, _, err := s.Seed(sf, sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	run, results := runInline(t, s, r, "manual")
	if sr.callCount() != 1 {
		t.Fatalf("report step ran %d times, want 1", sr.callCount())
	}
	if !strings.Contains(sr.calls[0].prompt, auditJSON) || strings.Contains(sr.calls[0].prompt, PrevOutputPlaceholder) {
		t.Errorf("report prompt did not receive the audit JSON in place of %s", PrevOutputPlaceholder)
	}
	if sr.calls[0].model != "haiku" {
		t.Errorf("report step model = %q, want haiku", sr.calls[0].model)
	}
	return filepath.Join(sysscan.SkillAuditReportDir(ws), sysscan.SkillAuditReportPrefix+time.Now().Format("2006-01-02")+".md"),
		run, results, sr
}

// The routine end to end: the report lands at
// <workspace>/swarmery/workspace/retro/skill-audit-<date>.md with the five sections.
func TestSkillAuditRoutineWritesReport(t *testing.T) {
	reply := reportMarkdown()
	path, run, results, _ := runSkillAuditRoutine(t, reply)
	if run.Status != "ok" {
		t.Fatalf("run status = %q, results = %+v", run.Status, results)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("report not written: %v", err)
	}
	if string(got) != reply {
		t.Errorf("report = %q, want the model's reply verbatim", got)
	}
	for _, sec := range sysscan.SkillAuditSections {
		if !strings.Contains(string(got), "\n## "+sec+"\n") {
			t.Errorf("report lacks section %q", sec)
		}
	}
	if latest, _, ok, err := sysscan.LatestSkillAuditReport(filepath.Dir(path)); err != nil || !ok || latest != path {
		t.Errorf("LatestSkillAuditReport = %q ok=%v err=%v, want %q", latest, ok, err, path)
	}
}

// A reply that dropped a section still lands (the operator can read it), but
// the run fails and names what is missing.
func TestSkillAuditRoutineRejectsMissingSection(t *testing.T) {
	path, run, results, _ := runSkillAuditRoutine(t, reportMarkdown("Skills per session"))
	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	last := results[len(results)-1]
	if last.Status != "failed" || !strings.Contains(last.Output, "missing section: Skills per session") {
		t.Errorf("save step = %+v, want a failure naming the missing section", last)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("incomplete report not kept for reading: %v", err)
	}
}
