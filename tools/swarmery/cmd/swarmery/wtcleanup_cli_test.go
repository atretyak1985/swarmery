package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wtjanitor"
)

type idleLiveness struct{}

func (idleLiveness) Busy(string) (bool, error) { return false, nil }

// wtcleanupFixture: a real repository with two harness-layout worktrees owned
// by DONE phases — one whose landing merged, one with real unmerged work — both
// journalled keep-unmerged, in a store on disk the CLI opens by path.
func wtcleanupFixture(t *testing.T) (dbPath, landed, unmerged string, git func(dir string, args ...string) string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	prev := wtcleanupLive
	wtcleanupLive = func(*sql.DB) wtjanitor.Liveness { return idleLiveness{} }
	t.Cleanup(func() { wtcleanupLive = prev })

	repo := t.TempDir()
	git = func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git(repo, "init", "-q", "-b", "main")
	git(repo, "config", "user.email", "t@example.com")
	git(repo, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(repo, "a.md"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(repo, "add", "a.md")
	git(repo, "commit", "-qm", "init")

	dbPath = filepath.Join(t.TempDir(), "cli.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	exec1 := func(q string, args ...any) int64 {
		t.Helper()
		res, err := db.Exec(q, args...)
		if err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
		id, _ := res.LastInsertId()
		return id
	}
	exec1(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, ?, 'repo', 'Repo', '2026-08-01T00:00:00Z')`, repo)
	plan := exec1(`INSERT INTO tasks (project_id, title, prompt, status, created_at, source, external_id)
		VALUES (1, 'Plan', 'p', 'running', '2026-10-01T00:00:00Z', 'workspace', '2026-10-01-plan')`)

	add := func(hex, landing, file string) string {
		ph := exec1(`INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, doc_status, run_state, landing_state)
			VALUES (?, 1, 'P', ?, 'done', 'done', ?)`, plan, "/ws/"+hex+".md", landing)
		branch := "swarm/phase-" + strconv.FormatInt(ph, 10)
		exec1(`UPDATE epic_phases SET run_branch = ? WHERE id = ?`, branch, ph)
		path := filepath.Join(repo, ".claude", "worktrees", "agent-"+hex)
		git(repo, "worktree", "add", "-q", "-b", branch, path)
		if err := os.WriteFile(filepath.Join(path, file), []byte(hex+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(path, "add", file)
		git(path, "commit", "-qm", "work")
		exec1(`INSERT INTO worktree_sweeps (ts, project_id, path, branch, verdict, reason) VALUES (?, 1, ?, ?, 'keep-unmerged', 'branch has unmerged commits')`,
			time.Now().UTC().Format(time.RFC3339), path, branch)
		return path
	}
	landed = add("aa11", "merged", "l.md")
	unmerged = add("bb22", "none", "u.md")
	return dbPath, landed, unmerged, git
}

// scan is read-only and reports both worktrees by proof kind; propose stores
// them; apply removes the selected one through a salvage branch and is
// idempotent on a second identical call.
func TestWtcleanupCLI_ScanProposeApply(t *testing.T) {
	dbPath, landed, unmerged, git := wtcleanupFixture(t)

	var out bytes.Buffer
	reportPath := filepath.Join(t.TempDir(), "nested", "scan.json")
	if err := runWtcleanup([]string{"scan", "--db", dbPath, "--json", "--out", reportPath}, &out); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var rep scanReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("scan JSON: %v\n%s", err, out.String())
	}
	if len(rep.Repos) != 1 || rep.Repos[0].ByProof["landing"] != 1 || rep.Repos[0].ByProof["none"] != 1 {
		t.Errorf("scan repos = %+v, want landing 1 + none 1", rep.Repos)
	}
	if raw, err := os.ReadFile(reportPath); err != nil || !bytes.Contains(raw, []byte(`"byProof"`)) {
		t.Errorf("--out file: %v %s", err, raw)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM worktree_cleanup_proposals`).Scan(&n)
	if n != 0 {
		t.Fatalf("scan wrote %d proposals; it must be read-only", n)
	}

	out.Reset()
	if err := runWtcleanup([]string{"propose", "--db", dbPath}, &out); err != nil {
		t.Fatalf("propose: %v", err)
	}
	if !strings.Contains(out.String(), "1 with proof (listed), 1 without (counted)") {
		t.Errorf("propose output = %q", out.String())
	}
	var id int64
	var path string
	if err := db.QueryRow(`SELECT id, path FROM worktree_cleanup_proposals WHERE proof_kind = 'landing' AND decided IS NULL`).Scan(&id, &path); err != nil {
		t.Fatalf("landing proposal: %v", err)
	}

	apply := func() string {
		t.Helper()
		var buf bytes.Buffer
		if err := runWtcleanup([]string{"apply", "--db", dbPath, "--id", strconv.FormatInt(id, 10), "--path", path}, &buf); err != nil {
			t.Fatalf("apply: %v\n%s", err, buf.String())
		}
		return buf.String()
	}
	first := apply()
	if !strings.Contains(first, "removed") || !strings.Contains(first, "→ salvage/swarm-phase-") {
		t.Errorf("first apply = %q, want removed with a salvage branch", first)
	}
	if _, err := os.Stat(landed); !os.IsNotExist(err) {
		t.Errorf("selected worktree still present: %v", err)
	}
	if _, err := os.Stat(unmerged); err != nil {
		t.Errorf("unselected worktree touched: %v", err)
	}
	if second := apply(); !strings.Contains(second, "already-decided") {
		t.Errorf("second apply = %q, want already-decided", second)
	}
	if salv := git(filepath.Dir(filepath.Dir(filepath.Dir(unmerged))), "branch", "--list", "salvage/*"); strings.Count(salv, "salvage/") != 1 {
		t.Errorf("salvage branches = %q, want exactly one (the second apply must not write another)", salv)
	}
}

func TestWtcleanupCLI_Usage(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"apply"}, {"apply", "--id", "1"}, {"scan", "extra"}} {
		if err := runWtcleanup(args, &bytes.Buffer{}); !isUsage(err) {
			t.Errorf("runWtcleanup(%v) = %v, want a usage error", args, err)
		}
	}
}
