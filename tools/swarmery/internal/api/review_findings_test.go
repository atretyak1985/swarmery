package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// findingsFixture scans a real temp workspace holding one plan with three
// phases: a two-round reviewed doc, a legacy Done doc (no block, dated before
// migration 0122's applied_at) and a control phase left with only a [MANUAL]
// criterion — the one needs-you item the plan should produce.
func findingsFixture(t *testing.T) (srv *httptest.Server, db *sql.DB, taskID int64, phases map[int]int64) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "findings.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	root := t.TempDir()
	taskDir := filepath.Join(root, "demo", "workspace", "working", "2026", "10", "09", "demo")
	planDir := filepath.Join(taskDir, "plan")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := func(name string) string {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "review-blocks", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	files := map[string]string{
		filepath.Join(taskDir, "README.md"): "# Task: Demo epic\n\n" +
			"- **Статус**: active\n- **Старт**: 2026-10-09 · **Завершено**: —\n- **Ціль**: demo goal\n",
		filepath.Join(planDir, "README.md"): "# Demo plan\n\n" +
			"| # | Phase | Doc | Depends on |\n|---|---|---|---|\n" +
			"| 1 | Reviewed | `phase-1-reviewed.md` | — |\n" +
			"| 2 | Legacy | `phase-2-legacy.md` | — |\n" +
			"| 3 | Manual | `phase-3-manual.md` | — |\n",
		filepath.Join(planDir, "phase-1-reviewed.md"): fixture("round2-resolves.md"),
		filepath.Join(planDir, "phase-2-legacy.md"):   fixture("legacy-done.md"),
		filepath.Join(planDir, "phase-3-manual.md"): "# Phase 3 — Manual\nStatus: Done\n\n## Acceptance criteria\n" +
			"- [x] a\n- [ ] [MANUAL] check production by hand\n\n## Completion Report\n\nShipped.\n",
	}
	old := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := wsingest.New(db, wsingest.Config{WorkspaceRoot: root}).Scan(); err != nil {
		t.Fatalf("workspace scan: %v", err)
	}
	if err := db.QueryRow(`SELECT id FROM tasks WHERE external_id='2026-10-09-demo'`).Scan(&taskID); err != nil {
		t.Fatalf("demo task row: %v", err)
	}
	phases = map[int]int64{}
	rows, err := db.Query(`SELECT seq, id FROM epic_phases WHERE workspace_task_id = ?`, taskID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var seq int
		var id int64
		if err := rows.Scan(&seq, &id); err != nil {
			t.Fatal(err)
		}
		phases[seq] = id
	}
	rows.Close()
	if len(phases) != 3 {
		t.Fatalf("phases = %v, want 3", phases)
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, db, taskID, phases
}

func TestPhaseFindingsEndpoint(t *testing.T) {
	srv, _, taskID, phases := findingsFixture(t)

	var got phaseFindingsDTO
	getJSON(t, srv.URL+"/api/epics/"+itoa(taskID)+"/phases/"+itoa(phases[1])+"/findings", &got)
	if got.LegacyReview || len(got.Blocks) != 2 {
		t.Fatalf("findings = %+v, want two rounds, not legacy", got)
	}
	r1, r2 := got.Blocks[0], got.Blocks[1]
	if r1.Round != "1" || r1.Verdict != "FAIL" || r2.Round != "2" || r2.Verdict != "PASS" || len(r2.Resolves) != 2 {
		t.Errorf("rounds = %+v / %+v", r1, r2)
	}
	if len(r1.Findings) != 2 || r1.Findings[0].ID != "R1-1" || r1.Findings[0].Type != "silent-failure" ||
		r1.Findings[0].Line == nil || *r1.Findings[0].Line != 84 ||
		r1.Findings[0].ResolvedBy == nil || *r1.Findings[0].ResolvedBy != "2" {
		t.Errorf("round 1 findings = %+v", r1.Findings)
	}
	if len(r2.Findings) != 0 || r2.Findings == nil {
		t.Errorf("round 2 findings = %#v, want [] (not null)", r2.Findings)
	}

	var legacy phaseFindingsDTO
	getJSON(t, srv.URL+"/api/epics/"+itoa(taskID)+"/phases/"+itoa(phases[2])+"/findings", &legacy)
	if !legacy.LegacyReview || legacy.Blocks == nil || len(legacy.Blocks) != 0 {
		t.Errorf("legacy findings = %#v, want legacyReview and blocks []", legacy)
	}

	resp, err := http.Get(srv.URL + "/api/epics/" + itoa(taskID+1) + "/phases/" + itoa(phases[1]) + "/findings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a phase outside the epic: status %d, want 404", resp.StatusCode)
	}
}

// The epic payload carries the rounds and the legacy flag on each phase.
func TestEpicPayloadCarriesReviewBlocks(t *testing.T) {
	srv, _, taskID, phases := findingsFixture(t)
	type epicShape struct {
		TaskID int64 `json:"taskId"`
		Phases []struct {
			ID           int64            `json:"id"`
			DocStatus    *string          `json:"docStatus"`
			ReviewBlocks []reviewBlockDTO `json:"reviewBlocks"`
			LegacyReview bool             `json:"legacyReview"`
		} `json:"phases"`
	}
	var epics []epicShape
	getJSON(t, srv.URL+"/api/epics", &epics)
	var epic epicShape
	for _, e := range epics {
		if e.TaskID == taskID {
			epic = e
		}
	}
	seen := 0
	for _, p := range epic.Phases {
		switch p.ID {
		case phases[1]:
			seen++
			if p.LegacyReview || len(p.ReviewBlocks) != 2 || len(p.ReviewBlocks[0].Findings) != 2 {
				t.Errorf("reviewed phase = %+v", p)
			}
		case phases[2]:
			seen++
			if !p.LegacyReview || p.ReviewBlocks == nil || len(p.ReviewBlocks) != 0 {
				t.Errorf("legacy phase = %+v, want legacyReview and reviewBlocks []", p)
			}
			if p.DocStatus == nil || *p.DocStatus != "done" {
				t.Errorf("legacy phase docStatus = %v, want done (D21: no status change)", p.DocStatus)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("payload phases = %+v", epic.Phases)
	}
}

// D21: a legacy phase is a badge, never a needs-you item. The control phase
// proves the source is live for this plan.
func TestLegacyReviewIsNotANeedsYouItem(t *testing.T) {
	srv, db, _, phases := findingsFixture(t)
	var legacy int
	if err := db.QueryRow(`SELECT legacy_review FROM epic_phases WHERE id = ?`, phases[2]).Scan(&legacy); err != nil || legacy != 1 {
		t.Fatalf("legacy_review = %d, %v; want 1", legacy, err)
	}
	items := getNeedsYou(t, srv.URL+"/api/needs-you").Items
	sawControl := false
	for _, it := range items {
		if it.Phase == nil {
			continue
		}
		switch it.Phase.PhaseID {
		case phases[2]:
			t.Errorf("the legacy phase is a needs-you item: %+v", it)
		case phases[3]:
			sawControl = true
		}
	}
	if !sawControl {
		t.Fatalf("the [MANUAL] control phase is missing from needs-you (%d items) — the check proves nothing", len(items))
	}
}

// D20 on the wire: a phase whose latest review round is incomplete carries
// reviewHold, and the completion gate reads it as unverified with the
// review-incomplete reason.
func TestEpicPayloadCarriesReviewHold(t *testing.T) {
	srv, db, taskID, phases := findingsFixture(t)
	const why = "review-incomplete: reviewer-timed-out — killed"
	if _, err := db.Exec(`UPDATE review_blocks SET complete = 0, scope_left_json = ?
		 WHERE round = '2' AND doc_path = (SELECT doc_path FROM epic_phases WHERE id = ?)`,
		`["`+why+`"]`, phases[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE epic_phases SET checkboxes_total = 1, checkboxes_done = 1 WHERE id = ?`, phases[1]); err != nil {
		t.Fatal(err)
	}
	type epicShape struct {
		TaskID int64 `json:"taskId"`
		Phases []struct {
			ID                 int64    `json:"id"`
			ReviewHold         string   `json:"reviewHold"`
			CompletionState    string   `json:"completionState"`
			CompletionBlockers []string `json:"completionBlockers"`
		} `json:"phases"`
	}
	var epics []epicShape
	getJSON(t, srv.URL+"/api/epics", &epics)
	for _, e := range epics {
		if e.TaskID != taskID {
			continue
		}
		for _, p := range e.Phases {
			switch p.ID {
			case phases[1]:
				if p.ReviewHold != "round 2: "+why || p.CompletionState != "unverified" ||
					len(p.CompletionBlockers) != 1 || !strings.HasPrefix(p.CompletionBlockers[0], "review-incomplete: ") {
					t.Errorf("held phase = %+v", p)
				}
			case phases[2]:
				if p.ReviewHold != "" {
					t.Errorf("legacy phase reviewHold = %q, want empty", p.ReviewHold)
				}
			}
		}
		return
	}
	t.Fatalf("task %d not in the payload", taskID)
}
