package verify

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"testing"
)

// ── fixtures ──

// insertEpic seeds one workspace task (the epic a phase row belongs to). ext keeps two
// epics in one test distinct.
func insertEpic(t *testing.T, db *sql.DB, ext string) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO tasks(project_id, title, prompt, priority, status, created_at,
		                  source, origin, external_id, board_column, file_scope,
		                  dependencies)
		VALUES(1, 'epic', 'plan', 5, 'running', '2026-08-01T00:00:00.000Z',
		       'workspace', 'manual', ?, 'todo', '[]', '[]')`, ext)
	if err != nil {
		t.Fatalf("insert epic: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// insertBranchPhase seeds a phase row that ran on branch ("" ⇒ NULL, a phase that
// never ran).
func insertBranchPhase(t *testing.T, db *sql.DB, epicID int64, seq int, docPath, branch string) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO epic_phases(workspace_task_id, seq, name, doc_path, depends_on,
		                        checkboxes_done, checkboxes_total, run_state, run_branch,
		                        verify_mode)
		VALUES(?, ?, 'phase', ?, '[]', 0, 3, 'done', NULLIF(?, ''), 'normal')`,
		epicID, seq, docPath, branch)
	if err != nil {
		t.Fatalf("insert phase %s: %v", docPath, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// deletePhase drops a row the way the ingest prune does after a carry.
func deletePhase(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM epic_phases WHERE id=?`, id); err != nil {
		t.Fatalf("delete phase %d: %v", id, err)
	}
}

// rekeyPhase is the SQL equivalent of wsingest's rename handling in one transaction:
// insert the row for the new doc path, carry the daemon-owned columns the verdict
// depends on (run_state, run_branch, run_start_point, verify_verdict, verify_detail —
// the same 1:1 copy carryAcrossRenames makes), then prune the vanished row. Returns
// the successor's id.
func rekeyPhase(t *testing.T, db *sql.DB, oldID int64, newDocPath string) int64 {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	res, err := tx.Exec(`
		INSERT INTO epic_phases(workspace_task_id, seq, name, doc_path, depends_on,
		                        checkboxes_done, checkboxes_total, verify_mode)
		SELECT workspace_task_id, seq, name, ?, depends_on,
		       checkboxes_done, checkboxes_total, verify_mode
		  FROM epic_phases WHERE id=?`, newDocPath, oldID)
	if err != nil {
		t.Fatalf("re-key: insert successor: %v", err)
	}
	newID, _ := res.LastInsertId()
	if _, err := tx.Exec(`
		UPDATE epic_phases
		   SET (run_state, run_branch, run_start_point, verify_verdict, verify_detail) =
		       (SELECT run_state, run_branch, run_start_point, verify_verdict, verify_detail
		          FROM epic_phases WHERE id=?)
		 WHERE id=?`, oldID, newID); err != nil {
		t.Fatalf("re-key: carry: %v", err)
	}
	if _, err := tx.Exec(`DELETE FROM epic_phases WHERE id=?`, oldID); err != nil {
		t.Fatalf("re-key: prune: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return newID
}

// captureLog redirects the standard logger for one test. The verify tests never run
// in parallel, so swapping the global writer is safe.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	})
	return &buf
}

// logLines returns the non-empty lines the logger wrote.
func logLines(buf *bytes.Buffer) []string {
	var out []string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// ── the five cases ──

func TestStampPhaseVerdict(t *testing.T) {
	const branch = "swarm/phase-11"
	type world struct {
		phaseID, taskID int64 // what the stamp is called with
		branch          string
		want            int64   // the id the stamp must report (and the one row stamped)
		untouched       []int64 // rows that must keep no verdict
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB) world
		// wantLog: substrings the single log line must carry; nil ⇒ no line at all.
		wantLog []string
	}{
		{
			name: "id present ⇒ stamped on it; the successor lookup never runs",
			setup: func(t *testing.T, db *sql.DB) world {
				epic := insertEpic(t, db, "W-a")
				id := insertBranchPhase(t, db, epic, 1, "/plan/phase-1.md", branch)
				// A second row sharing the branch would make a lookup ambiguous; it
				// stays unstamped only if the id path returned first.
				twin := insertBranchPhase(t, db, epic, 2, "/plan/phase-2.md", branch)
				return world{phaseID: id, taskID: epic, branch: branch, want: id, untouched: []int64{twin}}
			},
		},
		{
			name: "id gone, one same-task row carries the branch ⇒ that row is stamped",
			setup: func(t *testing.T, db *sql.DB) world {
				epic := insertEpic(t, db, "W-a")
				gone := insertBranchPhase(t, db, epic, 1, "/plan/phase-1.md", branch)
				successor := insertBranchPhase(t, db, epic, 1, "/archive/phase-1.md", branch)
				other := insertBranchPhase(t, db, epic, 2, "/archive/phase-2.md", "swarm/phase-12")
				deletePhase(t, db, gone)
				return world{phaseID: gone, taskID: epic, branch: branch, want: successor, untouched: []int64{other}}
			},
			wantLog: []string{"verdict=pass", "re-routed to successor", branch},
		},
		{
			name: "id gone, the branch only on a row of another workspace task ⇒ nothing stamped",
			setup: func(t *testing.T, db *sql.DB) world {
				epic := insertEpic(t, db, "W-a")
				stranger := insertEpic(t, db, "W-b")
				gone := insertBranchPhase(t, db, epic, 1, "/plan/phase-1.md", branch)
				foreign := insertBranchPhase(t, db, stranger, 1, "/plan/phase-1.md", branch)
				deletePhase(t, db, gone)
				return world{phaseID: gone, taskID: epic, branch: branch, untouched: []int64{foreign}}
			},
			wantLog: []string{"warn:", "verdict=pass", "not stamped", "0 rows carry branch " + branch},
		},
		{
			name: "id gone, two rows claim the branch ⇒ nothing stamped",
			setup: func(t *testing.T, db *sql.DB) world {
				epic := insertEpic(t, db, "W-a")
				gone := insertBranchPhase(t, db, epic, 1, "/plan/phase-1.md", branch)
				a := insertBranchPhase(t, db, epic, 1, "/archive/phase-1.md", branch)
				b := insertBranchPhase(t, db, epic, 2, "/archive/phase-2.md", branch)
				deletePhase(t, db, gone)
				return world{phaseID: gone, taskID: epic, branch: branch, untouched: []int64{a, b}}
			},
			wantLog: []string{"warn:", "verdict=pass", "not stamped", "2 rows carry branch " + branch},
		},
		{
			name: "id gone, empty branch ⇒ nothing stamped",
			setup: func(t *testing.T, db *sql.DB) world {
				epic := insertEpic(t, db, "W-a")
				gone := insertBranchPhase(t, db, epic, 1, "/plan/phase-1.md", "")
				// A branchless sibling must not be mistaken for the lineage either.
				sibling := insertBranchPhase(t, db, epic, 1, "/archive/phase-1.md", "")
				deletePhase(t, db, gone)
				return world{phaseID: gone, taskID: epic, branch: "", untouched: []int64{sibling}}
			},
			wantLog: []string{"warn:", "verdict=pass", "not stamped", "no run branch to follow"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := testDB(t)
			w := c.setup(t, db)
			buf := captureLog(t)

			got, err := stampPhaseVerdict(db, w.phaseID, w.taskID, w.branch, VerdictPass, "criteria met")
			if err != nil {
				t.Fatalf("stampPhaseVerdict: %v", err)
			}
			if got != w.want {
				t.Errorf("stamped id = %d, want %d", got, w.want)
			}
			if w.want != 0 {
				v, d := phaseVerdict(t, db, w.want)
				if v != string(VerdictPass) || d != "criteria met" {
					t.Errorf("row %d = (%q, %q), want (pass, criteria met)", w.want, v, d)
				}
			}
			for _, id := range w.untouched {
				if v, _ := phaseVerdict(t, db, id); v != "" {
					t.Errorf("row %d outside the lineage got verdict %q", id, v)
				}
			}

			lines := logLines(buf)
			if c.wantLog == nil {
				if len(lines) != 0 {
					t.Errorf("a direct stamp logged %q, want silence", lines)
				}
				return
			}
			if len(lines) != 1 {
				t.Fatalf("log lines = %q, want exactly one", lines)
			}
			want := append([]string{fmt.Sprintf("phase=%d", w.phaseID)}, c.wantLog...)
			if w.want != 0 {
				want = append(want, fmt.Sprintf("phase=%d", w.want))
			}
			for _, s := range want {
				if !strings.Contains(lines[0], s) {
					t.Errorf("log line %q does not name %q", lines[0], s)
				}
			}
		})
	}
}

// An empty detail lands as NULL, not ” — the shape the old inline stamp wrote and the
// API reads back as "no reasons".
func TestStampPhaseVerdict_EmptyDetailIsNull(t *testing.T) {
	db := testDB(t)
	epic := insertEpic(t, db, "W-a")
	id := insertBranchPhase(t, db, epic, 1, "/plan/phase-1.md", "swarm/phase-1")
	if _, err := stampPhaseVerdict(db, id, epic, "swarm/phase-1", VerdictFail, ""); err != nil {
		t.Fatal(err)
	}
	var d sql.NullString
	if err := db.QueryRow(`SELECT verify_detail FROM epic_phases WHERE id=?`, id).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if d.Valid {
		t.Errorf("verify_detail = %q, want NULL", d.String)
	}
}

// ── ordering against the re-key ──

// The ingest transaction and the stamp are serialized by SQLite, so a verdict can land
// on either side of a re-key. Both orders must end with the verdict on the successor.
func TestStampPhaseVerdict_ReKeyOrdering(t *testing.T) {
	const branch = "swarm/phase-21"
	t.Run("re-key after the stamp ⇒ the carry copies the verdict", func(t *testing.T) {
		db := testDB(t)
		epic := insertEpic(t, db, "W-a")
		old := insertBranchPhase(t, db, epic, 1, "/working/phase-1.md", branch)

		got, err := stampPhaseVerdict(db, old, epic, branch, VerdictPass, "graded")
		if err != nil {
			t.Fatal(err)
		}
		if got != old {
			t.Errorf("stamped id = %d, want the live row %d", got, old)
		}
		successor := rekeyPhase(t, db, old, "/archive/phase-1.md")
		if v, d := phaseVerdict(t, db, successor); v != string(VerdictPass) || d != "graded" {
			t.Errorf("successor = (%q, %q), want (pass, graded)", v, d)
		}
	})
	t.Run("re-key before the stamp ⇒ the stamp follows the branch", func(t *testing.T) {
		db := testDB(t)
		epic := insertEpic(t, db, "W-a")
		old := insertBranchPhase(t, db, epic, 1, "/working/phase-1.md", branch)
		successor := rekeyPhase(t, db, old, "/archive/phase-1.md")
		buf := captureLog(t)

		got, err := stampPhaseVerdict(db, old, epic, branch, VerdictPass, "graded")
		if err != nil {
			t.Fatal(err)
		}
		if got != successor {
			t.Errorf("stamped id = %d, want the successor %d", got, successor)
		}
		if v, d := phaseVerdict(t, db, successor); v != string(VerdictPass) || d != "graded" {
			t.Errorf("successor = (%q, %q), want (pass, graded)", v, d)
		}
		want := fmt.Sprintf("verify: phase=%d verdict=pass re-routed to successor phase=%d (branch %s)",
			old, successor, branch)
		if lines := logLines(buf); len(lines) != 1 || lines[0] != want {
			t.Errorf("log = %q, want exactly [%q]", lines, want)
		}
	})
}

// VerifyPhase's Stamp goes through the helper: a row archived while the verifier ran
// still receives the verdict, on its successor, and the plan is still nudged.
func TestVerifyPhase_VerdictFollowsAReKeyDuringGrading(t *testing.T) {
	db := testDB(t)
	epic := insertEpic(t, db, "W-a")
	req := phaseReq(0, epic, "normal")
	old := insertBranchPhase(t, db, epic, 1, "/working/phase-1.md", req.Branch)
	req.PhaseID = old

	var successor int64
	runner := &stubRunner{outFn: func(RunSpec) *Run {
		// The plan is archived while the verifier is still grading.
		successor = rekeyPhase(t, db, old, "/archive/phase-1.md")
		return &Run{Output: "all criteria met\nVERDICT: PASS"}
	}}
	s := newTestService(t, db, runner, stubTrees{hash: "phasetree"})
	var nudged []int64
	s.NotifyPlan = func(id int64) { nudged = append(nudged, id) }
	buf := captureLog(t)

	if err := s.VerifyPhase(context.Background(), req); err != nil {
		t.Fatalf("VerifyPhase: %v", err)
	}
	if v, d := phaseVerdict(t, db, successor); v != string(VerdictPass) || !strings.Contains(d, "all criteria met") {
		t.Errorf("successor = (%q, %q), want pass with the verifier's reasons", v, d)
	}
	if !strings.Contains(buf.String(), fmt.Sprintf("phase=%d verdict=pass re-routed to successor phase=%d", old, successor)) {
		t.Errorf("no re-route line in the log:\n%s", buf.String())
	}
	if len(nudged) != 1 || nudged[0] != epic {
		t.Errorf("plan_updated nudges = %v, want [%d]", nudged, epic)
	}
}
