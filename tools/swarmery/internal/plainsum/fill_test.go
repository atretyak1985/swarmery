package plainsum

// The Filler. The claims:
//
//  1. A quiet new plan gets `## In plain words` under the README's H1 and
//     before each phase doc's first `## `, in the plan's language, once.
//  2. A doc that already has the section, a plan dated before Since, and a
//     plan still being written are left alone.
//  3. A failed run is not retried for the same doc version.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type promptRunner struct {
	mu      sync.Mutex
	prompts []string
	err     error
}

func (p *promptRunner) Run(_ context.Context, prompt string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prompts = append(p.prompts, prompt)
	if p.err != nil {
		return "", p.err
	}
	return "What this is for, in plain words.", nil
}

const (
	readmeDoc = "# Ship the thing\n\n**Objective.** Do it.\n\n## Phase sequencing\n\n| # | Phase | Doc | Depends on |\n"
	phaseDoc  = "# Phase 1 — Do it\n\n**Repo:** /r\n**Branch:** b\n\n## Goal\n\nDo it.\n\n## Completion Report\n"
)

func writePlan(t *testing.T, root, date string, files map[string]string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(root, "proj", "workspace", "working", date, "slug", "plan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFiller(t *testing.T, r Runner, now time.Time) (*Filler, string) {
	t.Helper()
	root := t.TempDir()
	return &Filler{
		Svc:   newService(t, r),
		Root:  root,
		Since: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		Now:   func() time.Time { return now },
	}, root
}

func TestFillerWritesTheSectionOnce(t *testing.T) {
	now := time.Now()
	r := &promptRunner{}
	f, root := newFiller(t, r, now)
	dir := writePlan(t, root, "2026/11/02", map[string]string{
		"README.md": readmeDoc, "phase-1-do.md": phaseDoc, "manifest.json": "{}",
	}, now.Add(-time.Hour))

	f.Pass(context.Background())

	readme := read(t, filepath.Join(dir, "README.md"))
	if !strings.HasPrefix(readme, "# Ship the thing\n\n## In plain words\n\nWhat this is for, in plain words.\n\n**Objective.**") {
		t.Errorf("README section misplaced:\n%s", readme)
	}
	phase := read(t, filepath.Join(dir, "phase-1-do.md"))
	if !strings.Contains(phase, "**Branch:** b\n\n## In plain words\n\nWhat this is for, in plain words.\n\n## Goal") {
		t.Errorf("phase section misplaced:\n%s", phase)
	}
	if Authored(readme) == "" || Authored(phase) == "" {
		t.Error("written section is not read back as authored")
	}
	if len(r.prompts) != 2 {
		t.Fatalf("runs = %d, want 2", len(r.prompts))
	}

	// Quiet again: nothing more to do.
	for _, p := range []string{"README.md", "phase-1-do.md"} {
		_ = os.Chtimes(filepath.Join(dir, p), now.Add(-time.Hour), now.Add(-time.Hour))
	}
	f.Pass(context.Background())
	if len(r.prompts) != 2 {
		t.Errorf("runs after a second pass = %d, want 2", len(r.prompts))
	}
}

func TestFillerLeavesOldBusyAndAuthoredPlans(t *testing.T) {
	now := time.Now()
	r := &promptRunner{}
	f, root := newFiller(t, r, now)
	old := writePlan(t, root, "2026/11/01", map[string]string{"README.md": readmeDoc}, now.Add(-time.Hour))
	authored := "# T\n\n## In plain words\n\nAlready here.\n"
	writePlan(t, root, "2026/11/03", map[string]string{"README.md": authored}, now.Add(-time.Hour))
	busy := filepath.Join(root, "proj", "workspace", "working", "2026/11/04", "slug", "plan")
	writePlan(t, root, "2026/11/04", map[string]string{"README.md": readmeDoc}, now.Add(-time.Hour))
	writePlan(t, root, "2026/11/04", map[string]string{"phase-1-do.md": phaseDoc}, now.Add(-30*time.Second))

	f.Pass(context.Background())

	if len(r.prompts) != 0 {
		t.Fatalf("runs = %d, want 0", len(r.prompts))
	}
	if read(t, filepath.Join(old, "README.md")) != readmeDoc {
		t.Error("a plan dated before Since was filled")
	}
	if read(t, filepath.Join(busy, "README.md")) != readmeDoc {
		t.Error("a plan still being written was filled")
	}
}

func TestFillerDoesNotRetryAFailedVersion(t *testing.T) {
	now := time.Now()
	r := &promptRunner{err: errors.New("boom")}
	f, root := newFiller(t, r, now)
	writePlan(t, root, "2026/11/02", map[string]string{"README.md": readmeDoc}, now.Add(-time.Hour))
	f.Pass(context.Background())
	f.Pass(context.Background())
	if len(r.prompts) != 1 {
		t.Errorf("runs = %d, want 1", len(r.prompts))
	}
}

func TestDocLang(t *testing.T) {
	if got := DocLang("# План\n\nЗробити панель лімітів для `internal/usage` і `web/src`."); got != "uk" {
		t.Errorf("Ukrainian doc read as %s", got)
	}
	if got := DocLang("# Plan\n\nShip it.\n\n```go\n// коментар українською\n```\n"); got != "en" {
		t.Errorf("English doc read as %s", got)
	}
}

func TestInsertAuthoredFlattensModelHeadings(t *testing.T) {
	got := InsertAuthored("# T\n\nbody\n", "### Why\nBecause.", true)
	if got != "# T\n\n## In plain words\n\nWhy\nBecause.\n\nbody\n" {
		t.Errorf("got %q", got)
	}
}

func TestInsertAuthoredWithoutHeadings(t *testing.T) {
	if got := InsertAuthored("no headings\n", "x", false); got != "## In plain words\n\nx\n\nno headings\n" {
		t.Errorf("got %q", got)
	}
}
