package taskcap_test

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskcap"
)

const ghLabel = "github:octo/widgets#12"

func labelInput(projectID int64, desc string) taskcap.LabelInput {
	return taskcap.LabelInput{
		ProjectID:   projectID,
		Label:       ghLabel,
		Title:       "octo/widgets#12: crash on empty config",
		Description: desc,
		Now:         time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC),
	}
}

type cardRow struct {
	column, status, origin string
	labels                 []string
	prompt                 string
	archivedAt             sql.NullString
	movedAt                sql.NullString
}

func readCard(t *testing.T, db *sql.DB, id int64) cardRow {
	t.Helper()
	var r cardRow
	var labels string
	if err := db.QueryRow(`
		SELECT board_column, status, origin, labels, prompt, archived_at, column_moved_at
		  FROM tasks WHERE id = ?`, id,
	).Scan(&r.column, &r.status, &r.origin, &labels, &r.prompt, &r.archivedAt, &r.movedAt); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(labels), &r.labels); err != nil {
		t.Fatal(err)
	}
	return r
}

func countCards(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestUpsertByLabelCreatesOnceThenUpdates is the dedupe the GitHub inbox rests
// on: the label is the key, so the same item synced twice is one card, and a
// change to it is an update of that card, never a second one.
func TestUpsertByLabelCreatesOnceThenUpdates(t *testing.T) {
	db, projectID, _ := testDB(t)

	first, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body v1"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != taskcap.UpsertCreated || first.ID == 0 || !strings.HasPrefix(first.ExternalID, "T-") {
		t.Fatalf("first upsert = %+v, want a created card", first)
	}
	c := readCard(t, db, first.ID)
	if c.column != "triage" || c.origin != "github" || c.status != "queued" {
		t.Errorf("new card column/origin/status = %s/%s/%s, want triage/github/queued", c.column, c.origin, c.status)
	}
	if len(c.labels) != 1 || c.labels[0] != ghLabel {
		t.Errorf("new card labels = %v, want exactly [%s] — a new item is not a new comment", c.labels, ghLabel)
	}

	replay, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body v1"))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Outcome != taskcap.UpsertUnchanged || replay.ID != first.ID {
		t.Errorf("replay = %+v, want unchanged on card %d", replay, first.ID)
	}

	upd, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body v1\n\nnew comment"))
	if err != nil {
		t.Fatal(err)
	}
	if upd.Outcome != taskcap.UpsertUpdated || upd.ID != first.ID {
		t.Fatalf("update = %+v, want updated on card %d", upd, first.ID)
	}
	c = readCard(t, db, first.ID)
	if c.prompt != "body v1\n\nnew comment" {
		t.Errorf("prompt = %q, want the new description", c.prompt)
	}
	if strings.Join(c.labels, ",") != ghLabel+","+taskcap.NewCommentLabel {
		t.Errorf("labels = %v, want the item label plus the new-comment marker", c.labels)
	}
	if n := countCards(t, db); n != 1 {
		t.Errorf("cards = %d, want 1", n)
	}
}

// TestUpsertByLabelReturnsCardToTriage: a card the operator already moved on —
// queued, reviewed, done, dismissed — comes back to the inbox on an update, with
// its retirement undone, so the new comment is seen. Never into todo.
func TestUpsertByLabelReturnsCardToTriage(t *testing.T) {
	for _, from := range []string{"todo", "in_review", "done", "archived"} {
		t.Run(from, func(t *testing.T) {
			db, projectID, _ := testDB(t)
			res, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`
				UPDATE tasks SET board_column = ?, status = 'cancelled',
				       archived_at = '2026-10-21T00:00:00.000Z', column_moved_at = '2026-10-21T00:00:00.000Z'
				 WHERE id = ?`, from, res.ID); err != nil {
				t.Fatal(err)
			}
			upd, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body\n\ncomment"))
			if err != nil {
				t.Fatal(err)
			}
			if upd.Outcome != taskcap.UpsertUpdated || upd.Column != "triage" {
				t.Fatalf("update = %+v, want updated into triage", upd)
			}
			c := readCard(t, db, res.ID)
			if c.column != "triage" || c.status != "queued" || c.archivedAt.Valid {
				t.Errorf("after update: column=%s status=%s archived_at=%v, want triage/queued/NULL",
					c.column, c.status, c.archivedAt)
			}
			if c.movedAt.String != "2026-10-22T12:00:00.000Z" {
				t.Errorf("column_moved_at = %v, want the update instant", c.movedAt)
			}
			if !containsLabel(c.labels, taskcap.NewCommentLabel) {
				t.Errorf("labels = %v, want the new-comment marker", c.labels)
			}
		})
	}
}

// TestUpsertByLabelLeavesDispatcherOwnedColumn: a card the dispatcher is running
// (in_progress, or holding a worktree) gets the new text and the marker but
// keeps its column — pulling it to triage would strand the run.
func TestUpsertByLabelLeavesDispatcherOwnedColumn(t *testing.T) {
	cases := []struct {
		name, column string
		worktree     any
	}{
		{"in_progress", "in_progress", "/tmp/wt"},
		{"in_review with worktree", "in_review", "/tmp/wt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, projectID, _ := testDB(t)
			res, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE tasks SET board_column = ?, status = 'running', worktree_path = ? WHERE id = ?`,
				tc.column, tc.worktree, res.ID); err != nil {
				t.Fatal(err)
			}
			upd, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body\n\ncomment"))
			if err != nil {
				t.Fatal(err)
			}
			c := readCard(t, db, res.ID)
			if upd.Outcome != taskcap.UpsertUpdated || c.column != tc.column || c.status != "running" {
				t.Errorf("update = %+v, row column=%s status=%s; want updated, column %s and status running kept",
					upd, c.column, c.status, tc.column)
			}
			if c.prompt != "body\n\ncomment" || !containsLabel(c.labels, taskcap.NewCommentLabel) {
				t.Errorf("text/marker not applied: prompt=%q labels=%v", c.prompt, c.labels)
			}
		})
	}
}

// TestUpsertByLabelNeverTodo pins the idea boundary: dispatch takes todo only,
// so a synced card may never be born there (nor in the dispatcher's own
// in_progress).
func TestUpsertByLabelNeverTodo(t *testing.T) {
	db, projectID, _ := testDB(t)
	for _, col := range []string{"todo", "in_progress"} {
		in := labelInput(projectID, "body")
		in.Column = col
		if _, err := taskcap.UpsertByLabel(db, in); err == nil {
			t.Errorf("Column %q accepted; a synced card must never be born there", col)
		}
	}
	if n := countCards(t, db); n != 0 {
		t.Errorf("cards = %d after refused upserts, want 0", n)
	}
}

func TestUpsertByLabelValidation(t *testing.T) {
	db, projectID, _ := testDB(t)
	bad := []taskcap.LabelInput{
		{ProjectID: projectID, Title: "t", Description: "d"},                                  // no label
		{ProjectID: projectID, Label: ghLabel, Description: "d"},                              // no title
		{ProjectID: projectID, Label: ghLabel, Title: "t"},                                    // no description
		{ProjectID: projectID, Label: ghLabel, Title: "t", Description: "d", Origin: "manual"}, // not an upsert origin
	}
	for i, in := range bad {
		if _, err := taskcap.UpsertByLabel(db, in); err == nil {
			t.Errorf("case %d accepted: %+v", i, in)
		}
	}
}

// TestUpsertByLabelProjectScoped: two projects watching the same repository each
// own their own card for the same item.
func TestUpsertByLabelProjectScoped(t *testing.T) {
	db, projectA, _ := testDB(t)
	res, err := db.Exec(`INSERT INTO projects (path, slug, first_seen) VALUES ('/tmp/cap-b', '-tmp-cap-b', '2026-07-10T00:00:00.000Z')`)
	if err != nil {
		t.Fatal(err)
	}
	projectB, _ := res.LastInsertId()
	a, err := taskcap.UpsertByLabel(db, labelInput(projectA, "body"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := taskcap.UpsertByLabel(db, labelInput(projectB, "body"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Outcome != taskcap.UpsertCreated || b.Outcome != taskcap.UpsertCreated || a.ID == b.ID {
		t.Errorf("a=%+v b=%+v, want two created cards", a, b)
	}
}

// TestUpsertByLabelRelabelsAStrippedCard: a card whose label was edited away is
// still this item's card (the capture key remembers it), so the next sync
// restores the label instead of minting a duplicate.
func TestUpsertByLabelRelabelsAStrippedCard(t *testing.T) {
	db, projectID, _ := testDB(t)
	res, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET labels = '["mine"]' WHERE id = ?`, res.ID); err != nil {
		t.Fatal(err)
	}
	again, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body"))
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != res.ID || again.Outcome != taskcap.UpsertUpdated {
		t.Errorf("again = %+v, want an update of card %d", again, res.ID)
	}
	c := readCard(t, db, res.ID)
	if !containsLabel(c.labels, ghLabel) || !containsLabel(c.labels, "mine") {
		t.Errorf("labels = %v, want the operator's label kept and the item label restored", c.labels)
	}
	if n := countCards(t, db); n != 1 {
		t.Errorf("cards = %d, want 1", n)
	}
}

func TestFindByLabel(t *testing.T) {
	db, projectID, _ := testDB(t)
	if _, found, err := taskcap.FindByLabel(db, projectID, ghLabel); err != nil || found {
		t.Fatalf("empty board: found=%v err=%v", found, err)
	}
	res, err := taskcap.UpsertByLabel(db, labelInput(projectID, "body"))
	if err != nil {
		t.Fatal(err)
	}
	c, found, err := taskcap.FindByLabel(db, projectID, ghLabel)
	if err != nil || !found || c.ID != res.ID || c.Prompt != "body" || c.Column != "triage" {
		t.Errorf("FindByLabel = %+v found=%v err=%v", c, found, err)
	}
	if _, found, _ := taskcap.FindByLabel(db, projectID, "github:octo/widgets#1"); found {
		t.Error("a label prefix matched; the label must match whole")
	}
}

func containsLabel(xs []string, l string) bool {
	for _, x := range xs {
		if x == l {
			return true
		}
	}
	return false
}
