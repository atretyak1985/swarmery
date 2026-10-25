package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// staleRepo is a real repo on main with one commit, a live branch, and a branch
// that existed and was deleted — the shape a merged plan-run branch leaves.
func staleRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	gitFixture(t, root, "init", "-q", "-b", "main")
	gitFixture(t, root, "commit", "-q", "--allow-empty", "-m", "base")
	gitFixture(t, root, "branch", "gone-branch")
	gitFixture(t, root, "branch", "-q", "-D", "gone-branch")
	gitFixture(t, root, "branch", "live-branch")
	return root
}

// stalenessFixture is frictionFixture with project 1 pointed at a real repo.
func stalenessFixture(t *testing.T, j *advisorJudge) (*sql.DB, *triage.Service, *frictionSource, string) {
	t.Helper()
	db, svc, src := frictionFixture(t, j)
	root := staleRepo(t)
	if _, err := db.Exec(`UPDATE projects SET path = ? WHERE id = 1`, root); err != nil {
		t.Fatal(err)
	}
	return db, svc, src, root
}

func addBranchSession(t *testing.T, db *sql.DB, id int64, branch, cwd string) errGroupSample {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO sessions(id, project_id, session_uuid, status, started_at, git_branch, cwd)
		VALUES(?, 1, ?, 'completed', '2026-01-02T00:00:00Z', ?, ?)`, id, "s"+branch+cwd, branch, cwd); err != nil {
		t.Fatal(err)
	}
	return errGroupSample{SessionID: id}
}

// The path signal: a missing path in a worktree container or outside the root
// is stale; an existing path, or a missing one inside the root, is live.
func TestStalePathSignal(t *testing.T) {
	_, _, src, root := stalenessFixture(t, &advisorJudge{})
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gone := root + "/.claude/worktrees/agent-gone"
	cases := []struct {
		name, example, wantPath string
		stale                   bool
	}{
		{"worktree gone", "Error: Exit code 128\nfatal: cannot change to '" + gone + "': No such file or directory", gone, true},
		{"sibling wt gone", "ugrep: warning: " + root + "-wt/x/tools/swarmery/web/tsconfig.app.json: No such file or directory",
			root + "-wt/x/tools/swarmery/web/tsconfig.app.json", true},
		{"path exists", "open " + root + "/README: permission denied", "", false},
		{"one exists, one gone", "cp '" + root + "/README' '" + gone + "/README': failed", "", false},
		{"missing inside root", "open " + root + "/src/missing.go: no such file or directory", "", false},
		{"relative path only", "internal/api/triage_agents.go:21:2: agentKind redeclared", "", false},
		{"single segment", "cannot open /nowhere: missing", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := src.h.probeFrictionStaleness(errGroup{Example: c.example}, 1)
			if st.Stale != c.stale {
				t.Fatalf("stale = %v (%v), want %v", st.Stale, st.Reasons, c.stale)
			}
			if c.stale && (len(st.Reasons) != 1 || !strings.Contains(st.Reasons[0], "path "+c.wantPath+" no longer exists")) {
				t.Fatalf("reasons = %v, want one naming %s", st.Reasons, c.wantPath)
			}
		})
	}
}

// A path running into the end of a cut (160-rune) example may be truncated and
// is not probed.
func TestStalePathCutExampleSkipsTrailingToken(t *testing.T) {
	const tail = " /tmp/gone-dir/some/long/path/that/was/cut"
	ex := strings.Repeat("x", errExampleRunes-len(tail)) + tail
	if got := extractAbsPaths(ex); len(got) != 0 {
		t.Fatalf("extracted %v from a cut example's trailing token", got)
	}
	if got := extractAbsPaths("cd '/tmp/gone-dir/a' failed"); len(got) != 1 || got[0] != "/tmp/gone-dir/a" {
		t.Fatalf("extracted %v", got)
	}
}

// The branch signal: every sample on a deleted (or cwd-gone) non-default
// branch is stale; one sample on main, an empty branch, or a live ref with a
// live cwd keeps the group live.
func TestStaleBranchSignal(t *testing.T) {
	db, _, src, root := stalenessFixture(t, &advisorJudge{})
	const ex = "internal/api/triage_agents.go:21:2: agentKind redeclared"
	goneCwd := root + "/.claude/worktrees/swarm-plan-566"
	deleted := addBranchSession(t, db, 10, "gone-branch", root)
	neverCwdGone := addBranchSession(t, db, 11, "fix/planner-turn-budget", goneCwd)
	onMain := addBranchSession(t, db, 12, "main", root)
	liveLive := addBranchSession(t, db, 13, "live-branch", root)
	liveCwdGone := addBranchSession(t, db, 14, "live-branch", goneCwd)
	empty := addBranchSession(t, db, 15, "", root)

	probe := func(s ...errGroupSample) frictionStaleness {
		return src.h.probeFrictionStaleness(errGroup{Example: ex, Samples: s}, 1)
	}
	st := probe(deleted, neverCwdGone)
	if !st.Stale || len(st.Reasons) != 1 ||
		!strings.Contains(st.Reasons[0], "non-default branch: gone-branch, fix/planner-turn-budget") {
		t.Fatalf("deleted branches = %+v, want stale naming both", st)
	}
	if st := probe(deleted, neverCwdGone, onMain); st.Stale {
		t.Fatalf("with a sample on main = %+v, want live", st)
	}
	if st := probe(liveLive); st.Stale {
		t.Fatalf("live ref + live cwd = %+v, want live", st)
	}
	if st := probe(liveCwdGone); !st.Stale {
		t.Fatalf("live ref + gone cwd = %+v, want stale", st)
	}
	if st := probe(deleted, empty); st.Stale {
		t.Fatalf("with an empty-branch sample = %+v, want live", st)
	}
	if st := probe(); st.Stale {
		t.Fatalf("no samples and no paths = %+v, want live", st)
	}
	if st := probe(errGroupSample{SessionID: 999}); st.Stale {
		t.Fatalf("unknown session = %+v, want live", st)
	}
}

// No project, an unknown project, or a project whose path is gone: fail open.
func TestStaleProbeFailsOpenWithoutRepo(t *testing.T) {
	db, _, src, root := stalenessFixture(t, &advisorJudge{})
	g := errGroup{
		Example: "fatal: cannot change to '" + root + "/.claude/worktrees/agent-gone': No such file or directory",
		Samples: []errGroupSample{addBranchSession(t, db, 10, "gone-branch", root)},
	}
	if st := src.h.probeFrictionStaleness(g, 1); !st.Stale || len(st.Reasons) != 2 {
		t.Fatalf("control: project 1 = %+v, want stale on both signals", st)
	}
	for _, project := range []int64{0, 999, 2} { // 2 is /repo/q, which does not exist
		if st := src.h.probeFrictionStaleness(g, project); st.Stale {
			t.Fatalf("project %d = %+v, want live", project, st)
		}
	}
}

// Prepare shows the judge the probe's answer, and Apply re-probes: a stale
// fixable group files its T1 recommendation down-ranked and gets a plain track
// follow-up; a live one still gets the fix card.
func TestStaleApplyDownRanksFixable(t *testing.T) {
	payload := json.RawMessage(`{"title":"t","prompt":"p"}`)
	apply := func(t *testing.T, msg string) (triage.Item, triage.Applied, string) {
		t.Helper()
		db, _, src, root := stalenessFixture(t, &advisorJudge{})
		seedErrors(t, db, 1, strings.ReplaceAll(msg, "ROOT", root), 2)
		items, err := src.Collect(context.Background(), triage.Scope{}, 0)
		if err != nil || len(items) != 1 || items[0].ProjectID != 1 {
			t.Fatalf("collect = %+v (%v)", items, err)
		}
		it := items[0]
		if err := src.Prepare(context.Background(), &it); err != nil {
			t.Fatalf("prepare: %v", err)
		}
		a, err := src.Apply(context.Background(), it, it.Parts[0], frictionFixable, "why", payload)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		var detail string
		if err := db.QueryRow(`SELECT detail FROM recommendations WHERE rule = 'T1'`).Scan(&detail); err != nil {
			t.Fatal(err)
		}
		return it, a, detail
	}

	t.Run("stale", func(t *testing.T) {
		it, a, detail := apply(t, "cd 'ROOT/.claude/worktrees/agent-gone' failed: No such file or directory")
		if !strings.Contains(it.Evidence, "staleness: stale — path ") {
			t.Fatalf("evidence = %q, want a stale line", it.Evidence)
		}
		if len(a.Follow) != 1 || a.Follow[0].Class != triage.ClassPlain || a.Follow[0].Value != "track" ||
			a.Follow[0].Payload != nil || a.Follow[0].ProjectID != 0 {
			t.Fatalf("follow = %+v, want a plain track", a.Follow)
		}
		if !strings.HasPrefix(detail, "why\n\nDown-ranked — no fix card: path ") {
			t.Fatalf("detail = %q", detail)
		}
	})
	t.Run("live", func(t *testing.T) {
		it, a, detail := apply(t, "ENOENT reading config")
		if !strings.Contains(it.Evidence, "staleness: live") {
			t.Fatalf("evidence = %q, want a live line", it.Evidence)
		}
		if len(a.Follow) != 1 || a.Follow[0].Class != triage.ClassCard || a.Follow[0].Value != "fix-card" ||
			a.Follow[0].ProjectID != 1 {
			t.Fatalf("follow = %+v, want a fix card", a.Follow)
		}
		if detail != "why" {
			t.Fatalf("detail = %q, want the judge's reason unchanged", detail)
		}
	})
}

// R1-1/R1-2: the path signal fails open. A missing path outside the root is not
// ephemeral on that alone, a `:line:col` suffix is not part of the path, a live
// worktree keeps the group live, and a URL is not a path.
func TestStalePathSignalFailsOpen(t *testing.T) {
	_, _, src, root := stalenessFixture(t, &advisorJudge{})
	wt := root + "/.claude/worktrees/agent-a"
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wt+"/x.go", []byte("package x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir() // no repo-wt/name under it
	cases := []struct {
		name, example string
		stale         bool
	}{
		{"missing plugin-cache hook outside the root",
			"open /Users/me/.claude/plugins/cache/x/hooks/foo.sh: no such file or directory", false},
		{"live worktree file with line:col", wt + "/x.go:21:2: undefined: foo", false},
		{"live worktree, missing file", wt + "/gone.go:3: undefined: foo", false},
		{"sibling -wt worktree gone", "stat " + tmp + "/repo-wt/name/tools/a.go: no such file or directory", true},
		{"http URL", "GET http://localhost:7777/api/sessions returned 500", false},
		{"https URL", "fatal: unable to access 'https://github.com/x/y.git/'", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if st := src.h.probeFrictionStaleness(errGroup{Example: c.example}, 1); st.Stale != c.stale {
				t.Fatalf("stale = %v (%v), want %v", st.Stale, st.Reasons, c.stale)
			}
		})
	}

	// The same line:col example once its worktree is removed: stale, and the
	// reason names the path without the suffix.
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	st := src.h.probeFrictionStaleness(errGroup{Example: wt + "/x.go:21:2: undefined: foo"}, 1)
	if !st.Stale || len(st.Reasons) != 1 || !strings.Contains(st.Reasons[0], "path "+wt+"/x.go no longer exists") {
		t.Fatalf("removed worktree = %+v, want stale naming %s/x.go", st, wt)
	}
	if got := extractAbsPaths("GET http://localhost:7777/api/sessions and file:/etc/hosts/x"); len(got) != 0 {
		t.Fatalf("extracted %v from URLs", got)
	}
}
