package api

// routinesTaskCreator is the api-layer adapter that satisfies
// routines.TaskCreator: a create-task step inserts a board task through the SAME
// path as POST /api/board/tasks (source='queue', minted external id, default
// column validation, task_updated WS publish, dispatcher poke), so the board
// stays the single source of truth for task semantics and the routines package
// never imports the api package (no cycle). Constructed in cmd/swarmery and
// handed to routines.NewService.

import (
	"database/sql"
	"fmt"
	"time"
)

// RoutinesTaskCreator inserts board tasks on behalf of routine create-task steps.
// It holds only *sql.DB; the WS publish + dispatcher poke go through the same
// package-level hooks the board handlers use (publishTaskUpdated / pokeDispatch),
// which are no-ops when the bus/dispatcher are not attached.
type RoutinesTaskCreator struct {
	DB *sql.DB
}

// NewRoutinesTaskCreator builds the adapter.
func NewRoutinesTaskCreator(db *sql.DB) *RoutinesTaskCreator {
	return &RoutinesTaskCreator{DB: db}
}

// CreateTask inserts a board task (source='queue') in the given column for
// projectID and returns its external card id. Mirrors createBoardTask's INSERT
// exactly (priority 'normal', empty file_scope/dependencies), minus the HTTP
// plumbing. An unknown/blank column falls back to 'triage'.
func (c *RoutinesTaskCreator) CreateTask(projectID int64, title, prompt, column string) (string, error) {
	if column == "" || !validColumn(column) {
		column = "triage"
	}
	extID, err := newBoardExternalID()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(boardTSFormat)
	var movedAt any
	if column != "triage" {
		movedAt = now
	}
	res, err := c.DB.Exec(`
		INSERT INTO tasks (project_id, title, prompt, priority, status, created_at,
		                   source, external_id, board_column, file_scope,
		                   dependencies, column_moved_at)
		VALUES (?, ?, ?, ?, 'queued', ?, 'queue', ?, ?, '[]', '[]', ?)`,
		projectID, title, prompt, priorityLabels["normal"], now,
		extID, column, movedAt)
	if err != nil {
		return "", fmt.Errorf("insert board task: %w", err)
	}
	id, _ := res.LastInsertId()
	// Fan out the same signals a manual board POST would: notify WS subscribers
	// and poke the dispatcher (both no-ops when not attached).
	publishTaskUpdated(id)
	pokeDispatch()
	return extID, nil
}
