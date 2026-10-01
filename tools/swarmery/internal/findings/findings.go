// Package findings owns the config_lint_findings lifecycle: for one rule, the
// single active row per target is refreshed in place while the rule keeps
// firing; a target that stops firing gets resolved_at; a target that fires
// again after a resolve INSERTs a new row, so history is preserved.
//
// Sync is per rule, so writers with disjoint rule sets never touch each
// other's rows — that is what lets sysscan (registry lint rules) and
// plugindrift (plugin_* rules) share one table.
package findings

import (
	"database/sql"
	"errors"
	"time"
)

// Item is one detected violation, ready for its row.
type Item struct {
	Target   string
	Severity string // info | warn | error
	Message  string
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Execer is what Upsert and Resolve write through: a *sql.DB, or the *sql.Tx a
// caller already holds. The store runs one connection, so a writer inside a
// transaction (ingest's tail) must surface its finding through that same
// transaction rather than reach for the pool.
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Upsert refreshes the active row for (target, rule) or INSERTs one.
func Upsert(db Execer, target, rule, severity, message string) error {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NULL`,
		target, rule).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec(
			`INSERT INTO config_lint_findings (target, rule, severity, message, detected_at)
			 VALUES (?, ?, ?, ?, ?)`, target, rule, severity, message, now())
		return err
	}
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE config_lint_findings SET severity = ?, message = ? WHERE id = ?`,
		severity, message, id)
	return err
}

// Resolve closes the active row for (target, rule), if any.
func Resolve(db Execer, target, rule string) error {
	_, err := db.Exec(
		`UPDATE config_lint_findings SET resolved_at = ? WHERE target = ? AND rule = ? AND resolved_at IS NULL`,
		now(), target, rule)
	return err
}

// Sync upserts every item of one rule and resolves that rule's active rows
// whose target was not re-detected. An empty items slice therefore resolves
// every active row of the rule. Returns (detected, resolved).
func Sync(db *sql.DB, rule string, items []Item) (int, int, error) {
	keep := make(map[string]bool, len(items))
	for _, it := range items {
		if err := Upsert(db, it.Target, rule, it.Severity, it.Message); err != nil {
			return 0, 0, err
		}
		keep[it.Target] = true
	}
	resolved, err := resolveStale(db, rule, keep)
	if err != nil {
		return len(items), 0, err
	}
	return len(items), resolved, nil
}

func resolveStale(db *sql.DB, rule string, keep map[string]bool) (int, error) {
	rows, err := db.Query(
		`SELECT id, target FROM config_lint_findings WHERE rule = ? AND resolved_at IS NULL`, rule)
	if err != nil {
		return 0, err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		var target string
		if err := rows.Scan(&id, &target); err != nil {
			rows.Close()
			return 0, err
		}
		if !keep[target] {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	ts := now()
	for _, id := range stale {
		if _, err := db.Exec(
			`UPDATE config_lint_findings SET resolved_at = ? WHERE id = ?`, ts, id); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}
