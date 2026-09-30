package decide

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var bashSeq atomic.Int64

// seedBash records one Bash tool call for a session with the given exit status.
func seedBash(t *testing.T, db *sql.DB, uuid, cmd, status string) int64 {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"input": map[string]any{"command": cmd},
		"result": map[string]any{"stdout": "", "stderr": ""}})
	res, err := db.Exec(`INSERT INTO events (session_id, ts, type, tool_name, status, payload, dedup_key)
		VALUES ((SELECT id FROM sessions WHERE session_uuid = ?), '2026-09-20T10:30:00.000Z', 'tool_call', 'Bash', ?, ?, ?)`,
		uuid, status, string(payload), fmt.Sprintf("%s-%d", uuid, bashSeq.Add(1)))
	if err != nil {
		t.Fatalf("seed bash: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedFileChange(t *testing.T, db *sql.DB, uuid, file string, adds, dels int) {
	t.Helper()
	ev := seedBash(t, db, uuid, "true", "ok") // any event row; file_changes needs one
	mustExec(t, db, `INSERT INTO file_changes (event_id, session_id, file_path, change_type, additions, deletions)
		VALUES (?, (SELECT id FROM sessions WHERE session_uuid = ?), ?, 'edit', ?, ?)`, ev, uuid, file, adds, dels)
}

func seedPhaseRun(t *testing.T, db *sql.DB, uuid, state string, done, total int, before, after any) {
	t.Helper()
	mustExec(t, db, `INSERT INTO epic_phases (workspace_task_id, seq, name, doc_path, checkboxes_done, checkboxes_total,
		run_session_uuid, run_state, run_checkboxes_before, run_checkboxes_after)
		VALUES (1, 1, 'phase', '/ws/plan/phase-1-x.md', ?, ?, ?, ?, ?, ?)`, done, total, uuid, state, before, after)
}

func TestShipEvidence(t *testing.T) {
	cases := []struct {
		name     string
		seed     func(t *testing.T, db *sql.DB)
		operator string
		want     []string // lines that must appear
		absent   []string // substrings that must not
	}{
		{
			name:   "no evidence",
			seed:   func(*testing.T, *sql.DB) {},
			want:   []string{"commits: 0", "pushed: no", "pr opened: 0, pr merged: 0", "files edited: 0 (+0/-0)"},
			absent: []string{"phase run:", "operator verdict:"},
		},
		{
			name: "commits count only ok calls",
			seed: func(t *testing.T, db *sql.DB) {
				seedBash(t, db, "s", `git add a.go && git commit -m "fix: a"`, "ok")
				seedBash(t, db, "s", `git -C /repo commit -m "fix: b; with a semicolon"`, "ok")
				seedBash(t, db, "s", `GIT_AUTHOR_NAME=x git commit --amend --no-edit`, "ok")
				seedBash(t, db, "s", `git commit -m "nothing to commit"`, "error")
				seedBash(t, db, "s", `git status && git log --oneline -3`, "ok")
				seedBash(t, db, "s", `echo "git commit later"`, "ok")
			},
			want:   []string{"commits: 3", "pushed: no"},
			absent: []string{"commits: 4"},
		},
		{
			name: "push and PR create/merge",
			seed: func(t *testing.T, db *sql.DB) {
				seedBash(t, db, "s", `git push -u origin fix/x 2>&1 | tail -3`, "ok")
				seedBash(t, db, "s", `gh pr create --title t --body-file b.md`, "ok")
				seedBash(t, db, "s", `gh pr create --title t --body b`, "error")
				seedBash(t, db, "s", `gh -R owner/repo pr merge 12 --squash`, "ok")
				seedBash(t, db, "s", `gh pr view 12 --json state`, "ok")
			},
			want: []string{"commits: 0", "pushed: yes", "pr opened: 1, pr merged: 1"},
		},
		{
			name: "rejected push is not a push",
			seed: func(t *testing.T, db *sql.DB) {
				seedBash(t, db, "s", `git push origin main`, "error")
			},
			want: []string{"pushed: no"},
		},
		{
			name: "file changes",
			seed: func(t *testing.T, db *sql.DB) {
				seedFileChange(t, db, "s", "a.go", 10, 2)
				seedFileChange(t, db, "s", "a.go", 5, 0)
				seedFileChange(t, db, "s", "b.go", 1, 3)
			},
			want: []string{"files edited: 2 (+16/-5)"},
		},
		{
			name: "phase run prefers the run's own after-count",
			seed: func(t *testing.T, db *sql.DB) {
				seedPhaseRun(t, db, "s", "done", 3, 3, 0, 3)
			},
			want: []string{"phase run: done, criteria 3/3 ticked (0 before the run)"},
		},
		{
			name: "phase run without before/after counts",
			seed: func(t *testing.T, db *sql.DB) {
				seedPhaseRun(t, db, "s", "blocked", 1, 4, nil, nil)
			},
			want:   []string{"phase run: blocked, criteria 1/4 ticked\n"},
			absent: []string{"before the run"},
		},
		{
			name:     "operator verdict",
			seed:     func(*testing.T, *sql.DB) {},
			operator: "success",
			want:     []string{"operator verdict: success"},
		},
		{
			name: "another session's evidence is not counted",
			seed: func(t *testing.T, db *sql.DB) {
				seedSession(t, db, "other", "2026-09-20T11:00:00.000Z", "")
				seedBash(t, db, "other", `git commit -m x`, "ok")
				seedFileChange(t, db, "other", "c.go", 7, 7)
				seedPhaseRun(t, db, "other", "done", 1, 1, 0, 1)
			},
			want:   []string{"commits: 0", "files edited: 0 (+0/-0)"},
			absent: []string{"phase run:"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			seedSession(t, db, "s", "2026-09-20T11:00:00.000Z", "")
			tc.seed(t, db)
			got := shipEvidence(db, "s", tc.operator)
			if !strings.HasPrefix(got, "evidence:\n") {
				t.Fatalf("no evidence header:\n%s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("unexpected %q in:\n%s", a, got)
				}
			}
		})
	}
}

// A broken DB drops the evidence lines, never the block or the verdict.
func TestShipEvidence_DBErrorKeepsWhatItHas(t *testing.T) {
	db := openDB(t)
	db.Close()
	got := shipEvidence(db, "s", "fail")
	if got != "evidence:\noperator verdict: fail\n" {
		t.Fatalf("got %q", got)
	}
}

func TestLandingAction(t *testing.T) {
	for seg, want := range map[string]string{
		"git commit -m x":              "commit",
		"  (git commit -m x":           "commit",
		"git -c user.name=x commit":    "commit",
		"git --no-pager push":          "push",
		"/usr/bin/git push origin":     "push",
		"gh pr create --fill":          "pr-create",
		"gh pr merge --auto 3":         "pr-merge",
		"gh --repo o/r pr create":      "pr-create",
		"gh pr list":                   "",
		"git log --grep commit":        "",
		"git":                          "",
		"echo git commit":              "",
		"gh issue create":              "",
		"FOO=1 BAR=2 git commit -am x": "commit",
		// An option value with a space in it stays one token.
		`git -c user.name="First Last" -c user.email="a@b.c" commit -q -m "feat: x"`: "commit",
		`git -c user.name='First Last' push origin main`:                             "push",
		`git -c user.name="First Last" log --grep commit`:                            "",
		`git commit -m "unclosed message`:                                            "commit",
		`echo "git commit later"`:                                                    "",
	} {
		if got := landingAction(seg); got != want {
			t.Errorf("landingAction(%q) = %q, want %q", seg, got, want)
		}
	}
}

// inputCapture is a backend that records the evidence each question was given.
type inputCapture struct {
	inputs  map[string]string
	prompts map[string]string
}

func (c *inputCapture) Name() string { return BackendLocal }
func (c *inputCapture) Ask(_ context.Context, q Question) (Answer, error) {
	c.inputs[q.ID], c.prompts[q.ID] = q.Input, q.Prompt
	v := map[string]string{QD2TaskType: "bugfix", QD2Outcome: "shipped", QD2Failure: "none"}[q.ID]
	return Answer{Value: v, Confidence: 0.9, Calibrated: true}, nil
}

// The digest carries the ship-evidence block ahead of the message tail, and no
// bare `ended:` key a model could echo back as an off-list outcome.
func TestLabeler_DigestCarriesShipEvidence(t *testing.T) {
	db := openDB(t)
	seedSession(t, db, "s-ship", "2026-09-20T11:00:00.000Z", "")
	seedBash(t, db, "s-ship", `git commit -m "fix: parser" && git push`, "ok")
	seedFileChange(t, db, "s-ship", "parser.go", 12, 4)
	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	c := &inputCapture{inputs: map[string]string{}, prompts: map[string]string{}}
	e := &Engine{DB: db, Local: c, Now: now, Thresholds: map[string]float64{"d2": 0.6}}

	if n, err := (&Labeler{E: e}).Run(context.Background()); err != nil || n != 1 {
		t.Fatalf("run: %d %v", n, err)
	}
	for _, q := range []string{QD2TaskType, QD2Outcome, QD2Failure} {
		in, ok := c.inputs[q]
		if !ok {
			t.Fatalf("%s was not asked", q)
		}
		ev, msg := strings.Index(in, "evidence:\n"), strings.Index(in, "last assistant message (tail):")
		if ev < 0 || msg < 0 || ev > msg {
			t.Errorf("%s: evidence block missing or after the tail:\n%s", q, in)
		}
		for _, w := range []string{"commits: 1", "pushed: yes", "files edited: 1 (+12/-4)",
			"session window: 2026-09-20T10:00:00.000Z → 2026-09-20T11:00:00.000Z"} {
			if !strings.Contains(in, w) {
				t.Errorf("%s: digest lacks %q:\n%s", q, w, in)
			}
		}
		for _, line := range strings.Split(in, "\n") {
			if strings.HasPrefix(line, "ended:") || strings.HasPrefix(line, "started:") {
				t.Errorf("%s: digest still has a bare timestamp key %q", q, line)
			}
		}
	}
	if p := c.prompts[QD2Outcome]; !strings.Contains(p, "shipped = work was committed") {
		t.Errorf("outcome prompt does not define shipped: %q", p)
	}
	if p := c.prompts[QD2Failure]; !strings.Contains(p, "none if it shipped") {
		t.Errorf("failure prompt lost its none-if-shipped rule: %q", p)
	}
	// D2 stays shadow by default: decisions only, no session_labels.
	if l, _ := LabelFor(db, "s-ship"); l != nil {
		t.Errorf("shadow mode wrote labels: %+v", l)
	}
}
