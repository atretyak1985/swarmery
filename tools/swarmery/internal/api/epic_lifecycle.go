// Plan lifecycle endpoint (plans-page-lifecycle phase 1):
//
//	POST /api/epics/{taskId}/lifecycle  {"action": pause|resume|archive|restore}
//
// The lifecycle is implemented as FILE operations on the workspace — the same
// operations a human (or agent-work.sh complete) would perform by hand — so
// the workspace repo stays the single source of truth and the wsingest
// scanner converges on the result:
//
//   - pause / resume  rewrite the task-card README.md status line in place
//     (backup under <taskDir>/.backups/<ts>/ first, like the plan-doc editor);
//   - archive         sets the README to done, fills the completion date, and
//     moves the task dir working/YYYY/MM/DD/ → archive/YYYY/MM/DD/ (date
//     segment preserved so external_id stays stable), pruning now-empty
//     working/ date parents — mirroring agent-work.sh cmd_complete. INDEX.md
//     is NOT touched (owned by agent-work.sh index);
//   - restore         is the exact reverse move + README active + date reset.
//
// After every action the tasks row (and the plan artifact/doc paths on a
// move) is updated directly so the API answer is immediately consistent, then
// plan_updated is published; the next watcher/periodic scan converges on the
// file state (idempotent upsert keyed on workspace_id+external_id). If a zone
// move or the row tx fails mid-action, the files can briefly lead the DB and
// a retry may 500 on resolveEpicDirs until the watcher-triggered rescan
// (~1 s) reconverges — transient by design, not a bug.
//
// Responses: 200 {"status": <new derived planStatus>}; 400 unknown action /
// bad id; 404 unknown or non-workspace task / no plan dir; 409 invalid
// transition (pause/resume/archive on an archived task, restore on a
// non-archived one).

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/phasegate"
)

var (
	// The task-card README status line — the canonical `- **Status**: active`
	// (agent-work.sh init, taskdir.cardReadme, the core SessionStart hooks) or
	// the legacy `- **Статус**: active`. The replace touches the value only:
	// whatever trails it (` · **Ціль**: …`) survives and the label is kept as found.
	cardStatusRe = regexp.MustCompile(`(?mi)^(\s*-\s+\*\*(?:Статус|Status)\*\*:\s*)(\S+)`)
	// The completion-date field, empty (`**Завершено**: —`) and filled forms.
	doneDateEmptyRe  = regexp.MustCompile(`(\*\*Завершено\*\*:\s*)—`)
	doneDateFilledRe = regexp.MustCompile(`(\*\*Завершено\*\*:\s*)\d{4}-\d{2}-\d{2}`)
	// First markdown H1 — the insertion anchor for cards without a status line.
	cardH1Re = regexp.MustCompile(`(?m)^#\s+.+$`)
)

// epicLifecycle — POST /api/epics/{taskId}/lifecycle. requireLocalOrigin.
func (h *Handler) epicLifecycle(w http.ResponseWriter, r *http.Request) {
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	var req struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	switch req.Action {
	case "pause", "resume", "archive", "restore":
	default:
		writeClientErr(w, http.StatusBadRequest,
			"action must be one of pause, resume, archive, restore")
		return
	}

	// The task must be a workspace task (an epic lives in the workspace repo).
	var archivedAt sql.NullString
	err := h.DB.QueryRow(
		`SELECT archived_at FROM tasks WHERE id = ? AND source = 'workspace'`,
		taskID).Scan(&archivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "workspace task not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	archived := archivedAt.Valid

	// Transition guards: an archived plan accepts only restore; restore
	// requires an archived plan.
	if req.Action == "restore" && !archived {
		writeClientErr(w, http.StatusConflict, "task is not archived")
		return
	}
	if req.Action != "restore" && archived {
		writeClientErr(w, http.StatusConflict, "task is archived — restore it first")
		return
	}

	// Resolve the task dir from the plan artifact (symlink-resolved, like
	// resolvePlanDoc): plan dir per task_artifacts, task dir = its parent.
	planDir, taskDir, err := h.resolveEpicDirs(taskID)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "no plan directory for this task")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	switch req.Action {
	case "pause":
		err = h.lifecycleSetStatus(taskID, taskDir, "paused", "paused")
	case "resume":
		err = h.lifecycleSetStatus(taskID, taskDir, "active", "running")
	case "archive":
		err = h.lifecycleArchive(taskID, taskDir, planDir)
	case "restore":
		err = h.lifecycleRestore(taskID, taskDir, planDir)
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	status, err := h.derivedPlanStatus(taskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	publishPlanUpdated(taskID)
	writeJSON(w, map[string]string{"status": status}, nil)
}

// resolveEpicDirs returns the plan dir and its parent task dir for a
// workspace task. sql.ErrNoRows when the task has no plan artifact. The dir
// is validated via EvalSymlinks (it must exist and resolve — no dangling or
// looping links), but the RAW stored form is what the file operations and DB
// row rewrites use: task_artifacts/epic_phases hold the unresolved path the
// scanner wrote, and lifecycleUpdateRows' prefix REPLACE must match it
// byte-for-byte (e.g. macOS /var/… vs the resolved /private/var/…). There is
// no user-supplied path here to confine — the path comes from our own DB.
func (h *Handler) resolveEpicDirs(taskID int64) (planDir, taskDir string, err error) {
	var raw string
	err = h.DB.QueryRow(
		`SELECT path FROM task_artifacts WHERE task_id = ? AND kind = 'plan'`,
		taskID).Scan(&raw)
	if err != nil {
		return "", "", err
	}
	if _, err := filepath.EvalSymlinks(raw); err != nil {
		return "", "", fmt.Errorf("plan dir unresolvable: %w", err)
	}
	return raw, filepath.Dir(raw), nil
}

// lifecycleSetStatus rewrites the card README status (backup first) and
// updates the tasks row: pause → paused/paused, resume → active/running.
func (h *Handler) lifecycleSetStatus(taskID int64, taskDir, cardStatus, rowStatus string) error {
	if err := rewriteCardReadme(taskDir, func(text string) string {
		return upsertCardStatus(text, cardStatus)
	}); err != nil {
		return err
	}
	_, err := h.DB.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, rowStatus, taskID)
	return err
}

// lifecycleArchive mirrors agent-work.sh cmd_complete: README → done + today's
// completion date, then the zone move working/ → archive/ with parent pruning,
// then the DB rows (tasks + the moved plan/doc paths).
func (h *Handler) lifecycleArchive(taskID int64, taskDir, planDir string) error {
	today := time.Now().UTC().Format("2006-01-02")
	if err := rewriteCardReadme(taskDir, func(text string) string {
		text = upsertCardStatus(text, "done")
		return doneDateEmptyRe.ReplaceAllString(text, "${1}"+today)
	}); err != nil {
		return err
	}
	newTaskDir, err := moveTaskZone(taskDir, "working", "archive")
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(boardTSFormat)
	return h.lifecycleUpdateRows(taskID, taskDir, newTaskDir, planDir, `
		UPDATE tasks SET status = 'done', archived_at = ?, finished_at = ? WHERE id = ?`,
		now, now, taskID)
}

// lifecycleRestore is the exact reverse: zone move archive/ → working/, README
// back to active with the completion date cleared, tasks row un-archived.
func (h *Handler) lifecycleRestore(taskID int64, taskDir, planDir string) error {
	newTaskDir, err := moveTaskZone(taskDir, "archive", "working")
	if err != nil {
		return err
	}
	if err := rewriteCardReadme(newTaskDir, func(text string) string {
		text = upsertCardStatus(text, "active")
		return doneDateFilledRe.ReplaceAllString(text, "${1}—")
	}); err != nil {
		return err
	}
	return h.lifecycleUpdateRows(taskID, taskDir, newTaskDir, planDir, `
		UPDATE tasks SET status = 'running', archived_at = NULL, finished_at = NULL WHERE id = ?`,
		taskID)
}

// lifecycleUpdateRows applies the tasks-row update plus the path rewrites a
// zone move requires (task_artifacts.path + epic_phases.doc_path) in one tx,
// so the doc/activate endpoints keep resolving without waiting for a rescan.
// A concurrently running scan pass may briefly overwrite this row; the
// watcher rescan converges it.
func (h *Handler) lifecycleUpdateRows(taskID int64, oldTaskDir, newTaskDir, oldPlanDir string,
	taskUpdate string, args ...any) error {
	newPlanDir := filepath.Join(newTaskDir, filepath.Base(oldPlanDir))
	tx, err := h.DB.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(taskUpdate, args...); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`UPDATE task_artifacts SET path = ? WHERE task_id = ? AND kind = 'plan'`,
		newPlanDir, taskID); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec(
		`UPDATE epic_phases SET doc_path = REPLACE(doc_path, ?, ?) WHERE workspace_task_id = ?`,
		oldTaskDir+string(os.PathSeparator), newTaskDir+string(os.PathSeparator),
		taskID); err != nil {
		tx.Rollback()
		return err
	}
	// plan_revisions stores the ABSOLUTE plan dir resolved at staging time
	// (diff + apply read live bytes through it) — a zone move that skipped it
	// would leave every revision of this task pointing into the pruned tree.
	if _, err := tx.Exec(
		`UPDATE plan_revisions SET plan_dir = REPLACE(plan_dir, ?, ?) WHERE workspace_task_id = ?`,
		oldTaskDir+string(os.PathSeparator), newTaskDir+string(os.PathSeparator),
		taskID); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// derivedPlanStatus recomputes the epic's planStatus from the fresh DB state
// (tasks row + checkbox rollup) — the lifecycle response body.
func (h *Handler) derivedPlanStatus(taskID int64) (string, error) {
	var status string
	var archivedAt sql.NullString
	if err := h.DB.QueryRow(
		`SELECT status, archived_at FROM tasks WHERE id = ?`, taskID).
		Scan(&status, &archivedAt); err != nil {
		return "", err
	}
	var done, total int
	if err := h.DB.QueryRow(`
		SELECT COALESCE(SUM(checkboxes_done),0), COALESCE(SUM(checkboxes_total),0)
		FROM epic_phases WHERE workspace_task_id = ?`, taskID).
		Scan(&done, &total); err != nil {
		return "", err
	}
	// The lifecycle response and the list must never disagree about the same plan,
	// so this walks the SAME completion gate the list does rather than deriving
	// "done" from the checkbox rollup alone.
	allComplete, err := h.phasesAllComplete(taskID)
	if err != nil {
		return "", err
	}
	return planStatus(archivedAt.Valid, status, done, total, allComplete), nil
}

// phasesAllComplete reports whether every phase of a plan passes the completion
// gate (phasegate.Check). One row per phase rather than a clever SQL predicate:
// the gate is Go, and a SQL copy of it is exactly the second implementation this
// package is trying not to have.
func (h *Handler) phasesAllComplete(taskID int64) (bool, error) {
	closureRequired := phasegate.ClosureGateEnabled()
	rows, err := h.DB.Query(`
		SELECT e.checkboxes_done, e.checkboxes_total,
		       COALESCE(e.verify_mode,''), COALESCE(e.verify_verdict,''),
		       COALESCE(e.completion_report,''), e.run_ended_at IS NOT NULL,
		       COALESCE(bt.board_column,''), bt.archived_at IS NOT NULL
		  FROM epic_phases e
		  LEFT JOIN tasks bt ON bt.id = e.activated_board_task_id
		 WHERE e.workspace_task_id = ?`, taskID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	all := true
	for rows.Next() {
		var (
			done, total         int
			verifyMode, verdict string
			report              string
			ran                 bool
			boardCol            string
			archived            bool
		)
		if err := rows.Scan(&done, &total, &verifyMode, &verdict, &report, &ran, &boardCol, &archived); err != nil {
			return false, err
		}
		in := phasegate.Input{
			CriteriaDone:     done,
			CriteriaTotal:    total,
			VerifyMode:       verifyMode,
			VerifyVerdict:    verdict,
			LegacyDone:       boardCol == "done" || archived,
			CompletionReport: report,
			Ran:              ran,
			ClosureRequired:  closureRequired,
		}
		if phasegate.Check(in).HoldsPlanBack(in) {
			all = false
		}
	}
	return all, rows.Err()
}

// rewriteCardReadme applies edit to <taskDir>/README.md, backing the current
// file up under <taskDir>/.backups/<ts>/ first (writePlanDocFile — the same
// backup contract as the plan-doc editor). A missing README is created from
// the edit of an empty string (tolerant cards exist; parseCard must keep
// parsing ours afterwards).
func rewriteCardReadme(taskDir string, edit func(string) string) error {
	readme := filepath.Join(taskDir, "README.md")
	raw, err := os.ReadFile(readme)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err = writePlanDocFile(readme, edit(string(raw)))
	return err
}

// upsertCardStatus sets the README status-line value, inserting a fresh
// `- **Status**: <v>` after the H1 (or at the top) when no status line exists.
// The inserted label is the canonical one every writer uses (agent-work.sh init,
// taskdir.cardReadme) and every reader accepts (the core SessionStart hooks,
// agent-work.sh, wsingest). An existing line keeps whichever label it has: a
// legacy **Статус** card is edited, never re-spelled.
func upsertCardStatus(text, status string) string {
	if cardStatusRe.MatchString(text) {
		return cardStatusRe.ReplaceAllString(text, "${1}"+status)
	}
	line := "- **Status**: " + status
	if loc := cardH1Re.FindStringIndex(text); loc != nil {
		return text[:loc[1]] + "\n\n" + line + text[loc[1]:]
	}
	return line + "\n" + text
}

// moveTaskZone renames <ws>/workspace/<from>/YYYY/MM/DD/<slug> to the
// mirrored path under <to>/ (date segment preserved — external_id stays
// stable) and prunes the now-empty DD → MM → YYYY parents left behind.
func moveTaskZone(taskDir, from, to string) (string, error) {
	// slug → DD → MM → YYYY → zone root.
	zoneRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(taskDir))))
	if filepath.Base(zoneRoot) != from {
		return "", fmt.Errorf("task dir %s is not under a %s/ zone", taskDir, from)
	}
	rel, err := filepath.Rel(zoneRoot, taskDir) // YYYY/MM/DD/slug
	if err != nil {
		return "", err
	}
	dest := filepath.Join(filepath.Dir(zoneRoot), to, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(taskDir, dest); err != nil {
		return "", err
	}
	// Prune empty date parents (os.Remove refuses non-empty dirs — that stop
	// condition IS the guard, mirroring agent-work.sh's rmdir chain).
	for p := filepath.Dir(taskDir); strings.HasPrefix(p, zoneRoot+string(os.PathSeparator)); p = filepath.Dir(p) {
		if err := os.Remove(p); err != nil {
			break
		}
	}
	return dest, nil
}
