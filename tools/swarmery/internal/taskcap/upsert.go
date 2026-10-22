package taskcap

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// NewCommentLabel is the «new comment» marker: UpsertByLabel adds it to a card
// whenever an update carries something new, so the board can say why a card it
// had already seen is back in the inbox. A plain label rather than a column of
// its own — labels already ride on every board read, the ?label= filter and the
// chip row; nothing needs a migration to show it.
const NewCommentLabel = "new-comment"

// upsertOrigins is the provenance set a label-keyed card may carry. Only the
// GitHub inbox upserts today; manual and verify-fix cards are never re-written
// by a sync, and session/llm capture is insert-once (InsertCapturedTask).
var upsertOrigins = map[string]bool{
	"github": true,
}

// LabelInput is one label-keyed card as its writer wants it to read now.
type LabelInput struct {
	ProjectID int64
	// Label is the dedupe key: at most one queue card per project carries it.
	Label       string
	Title       string
	Description string
	// Column is where a NEW card is born: "" ⇒ triage. "todo" is refused — it
	// is the dispatch boundary, and a card a sync writes there would start a
	// run nobody asked for — and so is "in_progress", which only the
	// dispatcher may write. An update ignores Column (see UpsertByLabel).
	Column string
	// Origin is the provenance; "" ⇒ github.
	Origin string
	// Now is the write instant (test seam); zero ⇒ wall clock.
	Now time.Time
}

// UpsertOutcome says what UpsertByLabel did.
type UpsertOutcome string

const (
	UpsertCreated   UpsertOutcome = "created"
	UpsertUpdated   UpsertOutcome = "updated"
	UpsertUnchanged UpsertOutcome = "unchanged"
)

// UpsertResult is the card UpsertByLabel wrote (or left alone).
type UpsertResult struct {
	ID         int64
	ExternalID string
	Outcome    UpsertOutcome
	// Column is the card's column after the write.
	Column string
}

// LabeledCard is the part of a board card a label-keyed writer reads back.
type LabeledCard struct {
	ID           int64
	ExternalID   string
	Title        string
	Prompt       string
	Column       string
	Labels       []string
	WorktreePath string
}

// FindByLabel returns the project's newest queue card carrying label, or
// found=false. Workspace rows (source='workspace') are owned by the disk and
// never match.
func FindByLabel(db DB, projectID int64, label string) (LabeledCard, bool, error) {
	return scanLabeled(db.QueryRow(`
		SELECT id, external_id, title, prompt, board_column, labels, COALESCE(worktree_path, '')
		  FROM tasks
		 WHERE project_id = ? AND source = 'queue'
		   AND EXISTS (SELECT 1 FROM json_each(tasks.labels) WHERE json_each.value = ?)
		 ORDER BY id DESC LIMIT 1`, projectID, label))
}

func cardByID(db DB, id int64) (LabeledCard, bool, error) {
	return scanLabeled(db.QueryRow(`
		SELECT id, external_id, title, prompt, board_column, labels, COALESCE(worktree_path, '')
		  FROM tasks WHERE id = ? AND source = 'queue'`, id))
}

func scanLabeled(row *sql.Row) (LabeledCard, bool, error) {
	var c LabeledCard
	var ext sql.NullString
	var labels string
	if err := row.Scan(&c.ID, &ext, &c.Title, &c.Prompt, &c.Column, &labels, &c.WorktreePath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LabeledCard{}, false, nil
		}
		return LabeledCard{}, false, err
	}
	c.ExternalID = ext.String
	if strings.TrimSpace(labels) != "" {
		if err := json.Unmarshal([]byte(labels), &c.Labels); err != nil {
			return LabeledCard{}, false, fmt.Errorf("card %d: labels: %w", c.ID, err)
		}
	}
	return c, true, nil
}

// labelCaptureKey is the insert-race guard behind the label dedupe: two syncs
// creating the same card at once hit the capture_key partial unique index
// (0048) and only one row survives. Scoped by project, because two projects
// may well watch the same repository and each owns its own card.
func labelCaptureKey(projectID int64, label string) string {
	return "label:" + strconv.FormatInt(projectID, 10) + ":" + label
}

// UpsertByLabel creates the project's card for in.Label, or updates the one
// that already carries it.
//
//   - No card yet ⇒ one is minted through store.InsertBoardTask in in.Column
//     (default triage) carrying exactly [in.Label]. Outcome created.
//   - A card whose title and prompt already read as in.Title/in.Description ⇒
//     nothing is written. Outcome unchanged — a replay is not activity.
//   - Otherwise title and prompt are rewritten, NewCommentLabel is added, and
//     the card returns to triage: status queued, archived_at cleared, its move
//     stamped. Outcome updated. A dispatcher-owned card (in_progress, or one
//     with a worktree) keeps its column and gets only the text and the marker.
//
// It never writes todo — not on create (refused), not on update (the only
// column an update writes is triage). Dispatch takes todo only, so a card this
// writes can never start on its own.
func UpsertByLabel(db DB, in LabelInput) (UpsertResult, error) {
	label := strings.TrimSpace(in.Label)
	title := strings.TrimSpace(in.Title)
	desc := strings.TrimSpace(in.Description)
	if label == "" {
		return UpsertResult{}, errors.New("upsert by label: label is required")
	}
	if title == "" || desc == "" {
		return UpsertResult{}, errors.New("upsert by label: title and description are required")
	}
	origin := in.Origin
	if origin == "" {
		origin = "github"
	}
	if !upsertOrigins[origin] {
		return UpsertResult{}, fmt.Errorf("upsert by label: invalid origin %q (want github)", origin)
	}
	column := in.Column
	if column == "" {
		column = "triage"
	}
	if column == "todo" || column == "in_progress" {
		return UpsertResult{}, fmt.Errorf("upsert by label: a synced card is never born in %s", column)
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	cur, found, err := FindByLabel(db, in.ProjectID, label)
	if err != nil {
		return UpsertResult{}, err
	}
	if !found {
		id, inserted, err := store.InsertBoardTask(db, store.BoardTaskInput{
			ProjectID:  in.ProjectID,
			Title:      title,
			Prompt:     desc,
			Priority:   NormalPriority,
			Column:     column,
			Origin:     origin,
			Labels:     []string{label},
			CaptureKey: labelCaptureKey(in.ProjectID, label),
			Now:        now,
		})
		if err != nil {
			return UpsertResult{}, err
		}
		if inserted {
			c, _, err := cardByID(db, id)
			if err != nil {
				return UpsertResult{}, err
			}
			return UpsertResult{ID: id, ExternalID: c.ExternalID, Outcome: UpsertCreated, Column: column}, nil
		}
		// The capture key already exists: a concurrent sync won the insert, or
		// the label was edited off the card since. Either way that row IS this
		// label's card — update it rather than mint a second one.
		c, ok, err := cardByID(db, id)
		if err != nil {
			return UpsertResult{}, err
		}
		if !ok {
			return UpsertResult{}, fmt.Errorf("upsert by label: card %d for %q vanished", id, label)
		}
		cur = c
	}

	if cur.Title == title && cur.Prompt == desc && containsString(cur.Labels, label) {
		return UpsertResult{ID: cur.ID, ExternalID: cur.ExternalID, Outcome: UpsertUnchanged, Column: cur.Column}, nil
	}
	labels := appendMissing(cur.Labels, label, NewCommentLabel)
	labelsJSON, err := json.Marshal(labels)
	if err != nil {
		return UpsertResult{}, err
	}
	stamp := now.UTC().Format(tsFormat)
	// The column decision is made inside the UPDATE, against the row as it is
	// at write time: the dispatcher may have claimed the card (todo →
	// in_progress, worktree set) between the read above and this write, and a
	// card it owns must not be pulled back to triage under its run.
	if _, err := db.Exec(`
		UPDATE tasks
		   SET title = ?, prompt = ?, labels = ?,
		       board_column    = CASE WHEN board_column = 'in_progress' OR worktree_path IS NOT NULL
		                              THEN board_column ELSE 'triage' END,
		       status          = CASE WHEN board_column = 'in_progress' OR worktree_path IS NOT NULL
		                              THEN status ELSE 'queued' END,
		       archived_at     = CASE WHEN board_column = 'in_progress' OR worktree_path IS NOT NULL
		                              THEN archived_at ELSE NULL END,
		       column_moved_at = CASE WHEN board_column IN ('triage', 'in_progress') OR worktree_path IS NOT NULL
		                              THEN column_moved_at ELSE ? END
		 WHERE id = ? AND source = 'queue'`,
		title, desc, string(labelsJSON), stamp, cur.ID); err != nil {
		return UpsertResult{}, fmt.Errorf("upsert by label: update card %d: %w", cur.ID, err)
	}
	after, ok, err := cardByID(db, cur.ID)
	if err != nil {
		return UpsertResult{}, err
	}
	if !ok {
		return UpsertResult{}, fmt.Errorf("upsert by label: card %d vanished", cur.ID)
	}
	return UpsertResult{ID: cur.ID, ExternalID: after.ExternalID, Outcome: UpsertUpdated, Column: after.Column}, nil
}

func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// appendMissing returns xs plus every one of add it does not already hold, in
// order; xs itself is not modified.
func appendMissing(xs []string, add ...string) []string {
	out := append([]string(nil), xs...)
	for _, a := range add {
		if !containsString(out, a) {
			out = append(out, a)
		}
	}
	return out
}
