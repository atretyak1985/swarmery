// Task board API (fusion phase 1 — task queue): promotes the read-only tasks
// table into a dispatchable board queue. This file owns the WRITE surface
// (POST create, PATCH move/edit) and the board LIST/detail reads; the existing
// tasks.go keeps the workspace 14-day summary read API unchanged.
//
// Board rows created here carry source='queue' (the existing "created from the
// dashboard" enum value, 0006_workspaces.sql) and workspace_id=NULL, so they
// stay disjoint from workspace-ingested rows (source='workspace', unique on
// (workspace_id, external_id)): they never leak into the workspace summary
// query (WHERE source='workspace') and never hit the workspace upsert
// constraint (its WHERE workspace_id IS NOT NULL excludes NULL rows).
//
// Row storage is inline SQL through h.DB, matching approval_rules.go / retro.go
// (this codebase has no store.Store method layer for API-managed tables;
// single-writer discipline is the one daemon process, not a struct). The
// plan's TaskPatch/validation semantics live here as pure helper funcs
// (normalizePriority, board-column set, transition check, JSON round-trip) so
// they are unit-testable without a DB.
package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/staleness"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/taskcap"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/wsingest"
)

// boardTSFormat matches the millisecond-Z style of the other API timestamps.
const boardTSFormat = store.BoardTSFormat

// priorityLabels maps the accepted string tokens to the existing INTEGER
// priority scale (0001_init.sql, default 5). urgent < high < normal < low so
// idx_tasks_queue(status, priority, created_at) keeps meaningful ordering.
var priorityLabels = map[string]int{
	"urgent": 1,
	"high":   3,
	"normal": 5,
	"low":    7,
}

// priorityFromInt is the inverse used when serializing a row back to a DTO.
// Unknown/legacy values (e.g. the raw default 5 from workspace rows) map to the
// nearest label, defaulting to "normal".
func priorityFromInt(p int) string {
	switch {
	case p <= 1:
		return "urgent"
	case p <= 3:
		return "high"
	case p <= 5:
		return "normal"
	default:
		return "low"
	}
}

// normalizePriority validates a priority token and returns its integer form.
// Empty string → normal (the default). Pure; unit-tested.
func normalizePriority(token string) (int, error) {
	token = strings.TrimSpace(strings.ToLower(token))
	if token == "" {
		return priorityLabels["normal"], nil
	}
	v, ok := priorityLabels[token]
	if !ok {
		return 0, fmt.Errorf("invalid priority %q (want urgent|high|normal|low)", token)
	}
	return v, nil
}

// maxLabels and maxLabelLen bound a card's label set: a handful of short,
// slug-shaped marks (e.g. "jira-ticket") is the whole use case, and neither
// the board UI nor the ?label= filter needs more.
const (
	maxLabels   = 10
	maxLabelLen = 40
)

// validLabelChars reports whether s contains only lowercase letters, digits,
// '-' and '_' — the slug-safe alphabet a label may use once normalized.
func validLabelChars(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// normalizeLabels lowercases, trims, drops empties and de-duplicates while
// preserving first-seen order. Rejects labels longer than 40 chars or holding
// characters outside [a-z0-9-_], and more than 10 labels total. nil → empty
// slice (stored as "[]"). Pure; unit-tested.
func normalizeLabels(xs []string) ([]string, error) {
	seen := make(map[string]bool, len(xs))
	out := make([]string, 0, len(xs))
	for _, raw := range xs {
		l := strings.ToLower(strings.TrimSpace(raw))
		if l == "" {
			continue
		}
		if len(l) > maxLabelLen {
			return nil, fmt.Errorf("label %q exceeds %d characters", l, maxLabelLen)
		}
		if !validLabelChars(l) {
			return nil, fmt.Errorf("label %q: only lowercase letters, digits, - and _ are allowed", l)
		}
		if seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	if len(out) > maxLabels {
		return nil, fmt.Errorf("at most %d labels allowed, got %d", maxLabels, len(out))
	}
	return out, nil
}

// containsLabel reports whether label is present in xs. Both sides are
// expected already-normalized (lowercase, trimmed), so this is a plain
// equality scan. Pure; unit-tested.
func containsLabel(xs []string, label string) bool {
	for _, x := range xs {
		if x == label {
			return true
		}
	}
	return false
}

// validColumn reports whether c is in the closed board set (Fusion
// builtin:coding semantics). The set lives next to the board-card constructor
// in internal/store, which validates it on every insert. Pure; unit-tested.
func validColumn(c string) bool { return store.ValidColumn(c) }

// validOrigin reports whether o is in the closed provenance set of task
// provenances (0048): 'manual' for every hand-written card, 'session'/'llm' for
// minted ones, 'verify-fix' for the verifier's fix chain. The set itself lives
// in internal/store next to the constructor — the capture write path is shared
// with internal/ingest, and a second copy of the enum here is exactly how the
// HTTP validation and the writer would drift apart. Pure; unit-tested.
func validOrigin(o string) bool { return taskcap.ValidOrigin(o) }

// legalTransition enforces the two board rules from the phase doc: any→archived
// is always allowed; done→in_progress is rejected (recovery rehome is
// dispatcher-owned, not user-facing); everything else is permissive. Pure;
// unit-tested.
func legalTransition(from, to string) error {
	if to == "archived" {
		return nil
	}
	if from == "done" && to == "in_progress" {
		return errors.New("illegal transition done→in_progress (recovery is dispatcher-owned)")
	}
	return nil
}

// marshalStringList renders a []string as a compact JSON array for storage.
// nil → "[]". Pure; unit-tested via round-trip.
func marshalStringList(xs []string) (string, error) {
	if xs == nil {
		return "[]", nil
	}
	b, err := json.Marshal(xs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalStringList parses a stored JSON array back to []string. Empty/NULL
// storage → empty slice. Pure; unit-tested via round-trip.
func unmarshalStringList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return []string{}, nil
	}
	var xs []string
	if err := json.Unmarshal([]byte(s), &xs); err != nil {
		return nil, err
	}
	if xs == nil {
		xs = []string{}
	}
	return xs, nil
}

// newBoardExternalID mints a "T-" + 6-char base36 card id. The tasks.id INTEGER
// PK is autoincremented by SQLite; this string is the external_id (the shape the
// dispatcher and commit trailers reference). Minting lives in internal/taskcap
// so captured cards minted from the ingest side carry identically-shaped ids.
func newBoardExternalID() (string, error) { return taskcap.NewExternalID() }

// boardTaskDTO is the full board task shape (POST/PATCH response, board list
// item, and the task_updated WS payload). camelCase JSON, mirrored in
// web/src/api/types.ts.
type boardTaskDTO struct {
	ID            int64    `json:"id"`
	ExternalID    string   `json:"externalId"`
	ProjectID     int64    `json:"projectId"`
	ProjectSlug   *string  `json:"projectSlug"`
	Title         string   `json:"title"`
	Prompt        string   `json:"prompt"`
	Priority      string   `json:"priority"` // urgent|high|normal|low
	Status        string   `json:"status"`   // existing lifecycle column (queued|running|…)
	BoardColumn   string   `json:"boardColumn"`
	Paused        bool     `json:"paused"`
	UserPaused    bool     `json:"userPaused"`
	Dependencies  []string `json:"dependencies"`
	Model         *string  `json:"model"`
	Playbook      *string  `json:"playbook"` // selected recipe name (null = default 'standard')
	FileScope     []string `json:"fileScope"`
	Labels        []string `json:"labels"` // free-form marks (0049), e.g. "jira-ticket"; never null
	Branch        *string  `json:"branch"`
	WorktreePath  *string  `json:"worktreePath"`
	DispatchError *string  `json:"dispatchError"`
	// StartPoint is the SHA admit() pinned the worktree to (0051); null on a row
	// dispatched before it, and on any row that was never admitted. It is what
	// verification diffs against, so exposing it is what makes a verdict auditable
	// — "graded against what?" has an answer on the card.
	StartPoint *string `json:"startPoint"`
	// The two retry budgets are separate and must be labelled as such: RetryCount
	// is the dispatcher's no-progress heal budget, VerifyRetryCount the
	// verification fix-chain budget. One number for both was unreadable.
	RetryCount       int     `json:"retryCount"`
	VerifyRetryCount int     `json:"verifyRetryCount"`
	VerifyVerdict    *string `json:"verifyVerdict"`
	VerifyDetail     *string `json:"verifyDetail"`
	// ResultNote is the one-line outcome of the card: the sentinel line the
	// dispatcher recorded on a no-op exit, or — since the review loop (§3.2) — the
	// URL of the PR `land` opened. It was already on the row (0001_init) and
	// already written by dispatch; the board simply never showed it, so a landed
	// card had its PR link nowhere but the HTTP response that created it.
	ResultNote *string `json:"resultNote"`
	// Agent is the registry agent name this card dispatches as (0048); null = a
	// plain run. Origin/OriginSessionID are capture provenance: 'manual' for a
	// hand-written card, 'session'/'llm' for one minted from a session. The
	// idempotency key (capture_key) is deliberately NOT exposed — it is an
	// internal write-path detail with no reader on the board.
	Agent           *string `json:"agent"`
	Origin          string  `json:"origin"` // manual|session|llm|verify-fix
	OriginSessionID *int64  `json:"originSessionId"`
	// Source is the capture provenance as one nullable object (0066): null for
	// a manual card and for a verifier fix task, so "was this captured?" is one
	// null check rather than a string compare on origin. Quote is the session's
	// opening prompt (clipped at capture); Files the files that session had
	// touched — never null inside a non-null Source.
	Source *boardTaskSourceDTO `json:"source"`
	// StaleAfter is when the inbox sweeper will retire this card (RFC3339), or
	// null when the sweep can never touch it — taskcap.StaleAfter, evaluated
	// with the daemon's SWARMERY_INBOX_TTL. Derived, never stored.
	StaleAfter *string `json:"staleAfter"`
	// DispatchedPrompt is the exact first-stage prompt the dispatcher handed the
	// runner; null until the card has been dispatched.
	DispatchedPrompt *string `json:"dispatchedPrompt"`
	// PendingApprovalCount is how many permission requests are sitting unanswered
	// on this card's run — the fourth disjunct of the board's "needs me" filter,
	// and the only one the DTO could not already answer. Derived, never stored;
	// 0 (never null) so the client reads "no run, no approvals" and "nothing
	// pending" the same way, which is what the filter means by them.
	//
	// It is a COUNT rather than a bool because the number is the useful part of
	// "this run is parked": one request is a question, five is a wall.
	PendingApprovalCount int `json:"pendingApprovalCount"`
	// Derived, never stored: whether a task that claims to be running actually is,
	// and on what evidence. Computed per request by internal/staleness — a cached
	// verdict would be stale exactly when it matters, since the state it reads
	// changes outside the daemon. Empty when the task does not claim to be running.
	// PlanExternalID is the external id of the WORKSPACE task this card's micro-plan
	// became — the Plans-page entry for the same unit of work (phase 4). Null when
	// the card has no micro-plan, or when wsingest has not indexed the dir yet: the
	// chip appears once there is somewhere for it to link to, rather than dangling.
	PlanExternalID  *string `json:"planExternalId"`
	Staleness       string  `json:"staleness,omitempty"`
	StalenessReason string  `json:"stalenessReason,omitempty"`
	ColumnMovedAt   *string `json:"columnMovedAt"`
	CreatedAt       string  `json:"createdAt"`
}

// boardTaskSourceDTO is where a captured card came from (boardTaskDTO.Source).
type boardTaskSourceDTO struct {
	SessionID *int64   `json:"sessionId"`
	TurnUUID  *string  `json:"turnUuid"`
	Quote     *string  `json:"quote"`
	Files     []string `json:"files"`
}

const boardTaskSelect = `
	SELECT t.id, t.external_id, t.project_id, p.slug, t.title, t.prompt,
	       t.priority, t.status, t.board_column, t.paused, t.user_paused,
	       t.dependencies, t.model, t.playbook, t.file_scope, t.labels, t.branch, t.worktree_path,
	       t.dispatch_error, t.start_point, t.retry_count, t.verify_retry_count,
	       t.verify_verdict, t.verify_detail, t.result_note,
	       t.agent, t.origin, t.origin_session_id,
	       t.origin_turn_uuid, t.origin_quote, t.origin_files, t.dispatched_prompt,
	       t.source, t.column_moved_at, t.created_at, w.external_id
	FROM tasks t JOIN projects p ON p.id = t.project_id
	-- The card's micro-plan, if it has one and wsingest has indexed it: the dir is
	-- the join (tasks.workspace_dir), the 'plan' artifact is what points at it, and
	-- LEFT so a card without one is unaffected.
	LEFT JOIN task_artifacts ta ON ta.kind = 'plan' AND ta.path = t.workspace_dir || '/plan'
	LEFT JOIN tasks w ON w.id = ta.task_id AND w.source = 'workspace'`

// scanBoardTask hydrates d from one boardTaskSelect row. inboxTTL is the
// sweeper's TTL for the derived StaleAfter (<= 0 ⇒ the sweep is off ⇒ null).
func scanBoardTask(scan func(...any) error, d *boardTaskDTO, inboxTTL time.Duration) error {
	var (
		priority               int
		paused, userPaused     int64
		deps, scope, labs, src string
		externalID             sql.NullString
		turnUUID, quote, files sql.NullString
	)
	if err := scan(&d.ID, &externalID, &d.ProjectID, &d.ProjectSlug, &d.Title, &d.Prompt,
		&priority, &d.Status, &d.BoardColumn, &paused, &userPaused,
		&deps, &d.Model, &d.Playbook, &scope, &labs, &d.Branch, &d.WorktreePath,
		&d.DispatchError, &d.StartPoint, &d.RetryCount, &d.VerifyRetryCount,
		&d.VerifyVerdict, &d.VerifyDetail, &d.ResultNote,
		&d.Agent, &d.Origin, &d.OriginSessionID,
		&turnUUID, &quote, &files, &d.DispatchedPrompt,
		&src, &d.ColumnMovedAt, &d.CreatedAt, &d.PlanExternalID); err != nil {
		return err
	}
	d.ExternalID = externalID.String
	d.Priority = priorityFromInt(priority)
	d.Paused = paused != 0
	d.UserPaused = userPaused != 0
	var err error
	if d.Dependencies, err = unmarshalStringList(deps); err != nil {
		return err
	}
	if d.FileScope, err = unmarshalStringList(scope); err != nil {
		return err
	}
	if d.Labels, err = unmarshalStringList(labs); err != nil {
		return err
	}
	// A card is "captured" when it points at a session or carries any capture
	// column; origin alone is not the test because a verifier fix task has a
	// non-manual origin and no source at all.
	if d.OriginSessionID != nil || turnUUID.Valid || quote.Valid || files.Valid {
		srcDTO := &boardTaskSourceDTO{SessionID: d.OriginSessionID, Files: []string{}}
		if turnUUID.Valid {
			srcDTO.TurnUUID = &turnUUID.String
		}
		if quote.Valid {
			srcDTO.Quote = &quote.String
		}
		if files.Valid {
			if srcDTO.Files, err = unmarshalStringList(files.String); err != nil {
				return err
			}
		}
		d.Source = srcDTO
	}
	if at := taskcap.StaleAfter(taskcap.BoardTaskRow{
		Source: src, BoardColumn: d.BoardColumn, Origin: d.Origin,
		WorktreePath: d.WorktreePath, CreatedAt: d.CreatedAt, ColumnMovedAt: d.ColumnMovedAt,
	}, inboxTTL); at != nil {
		s := at.Format(time.RFC3339)
		d.StaleAfter = &s
	}
	return nil
}

// boardTaskByID hydrates one board task (used by POST/PATCH responses and the
// task_updated WS payload). Returns (nil, nil) when the row is gone.
func (h *Handler) boardTaskByID(id int64) (*boardTaskDTO, error) {
	var d boardTaskDTO
	err := scanBoardTask(h.DB.QueryRow(boardTaskSelect+` WHERE t.id = ?`, id).Scan, &d, h.InboxTTL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.PendingApprovalCount = pendingApprovalCount(h.DB, id)
	return &d, nil
}

// pendingApprovalPairs is every (card, unanswered permission request) pair in the
// database, and the ONE definition of "this card's run is waiting on a human".
// Both readers below wrap it, so the list and the single-card reads can never
// answer the same question differently.
//
// permission_requests has no task_id — the link runs through the SESSION. Which
// session is the whole design question, and there are three candidate columns:
//
//	tasks.session_id            REJECTED. Stamped COALESCE-first-wins
//	                            (runcore.StampPrimarySession), so a multi-stage
//	                            playbook pins it to stage 1 forever and an approval
//	                            raised in stage 3 would be invisible. It is also
//	                            redundant: it is only ever written after an explicit
//	                            task_sessions upsert, so the arm below is a strict
//	                            superset of it.
//	tasks.origin_session_id     REJECTED. That is the session a card was CAPTURED
//	                            from — someone's live interactive session, whose
//	                            approvals have nothing to do with this card.
//	task_sessions('explicit')   ARM 1. Every stage of a playbook lands here as it
//	                            exits (dispatch.linkSession). 'explicit' only:
//	                            heuristic links are cwd/time guesses and would
//	                            charge a stranger's approval to this card.
//
// Arm 1 alone would still read 0 exactly when it matters. linkSession runs AFTER
// a stage's process exits, and an agent blocked on a permission request has not
// exited — so during the block the only thing tying the run to the card is the
// uuid the dispatcher parked BEFORE spawning. Hence:
//
//	tasks.dispatch_session_uuid ARM 2. Authoritative from the instant the run
//	                            starts, which is the same reason ingest.isEngineRun
//	                            keeps it alongside its own task_sessions arm: a
//	                            predicate that guards the board must not depend on
//	                            a race with ingest.
//
// UNION, not UNION ALL: stage 1 satisfies both arms once its transcript lands, and
// its request must count once. Deduping on (task_id, request_id) is what makes that
// true for the request rather than for the row.
const pendingApprovalPairs = `
	SELECT ts.task_id AS task_id, pr.id AS request_id
	  FROM permission_requests pr
	  JOIN task_sessions ts ON ts.session_id = pr.session_id AND ts.link_source = 'explicit'
	 WHERE pr.status = 'pending'
	UNION
	SELECT t.id AS task_id, pr.id AS request_id
	  FROM permission_requests pr
	  JOIN sessions s ON s.id = pr.session_id
	  JOIN tasks t ON t.dispatch_session_uuid = s.session_uuid
	 WHERE pr.status = 'pending'`

// pendingApprovalCount answers for ONE card (the POST/PATCH response and the
// task_updated WS payload, which replace the client's row wholesale — leaving it
// at 0 here would blank the field on every edit). 0 on error: a derived hint must
// never fail the write it rides along with.
func pendingApprovalCount(db *sql.DB, id int64) int {
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM (`+pendingApprovalPairs+`) WHERE task_id = ?`, id).Scan(&n); err != nil {
		log.Printf("warning: api: pending approvals (task %d): %v", id, err)
		return 0
	}
	return n
}

// annotatePendingApprovals fills the count for a whole board in ONE query, the
// same shape and the same best-effort contract as annotateStaleness: a card the
// map does not mention keeps its zero value.
//
// Unbounded by the board's size on purpose — the outer filter is
// `status = 'pending'` against idx_pr_pending, and unanswered approvals are a
// handful at any instant, so the scan is over that handful and not over the
// cards. Per-card queries would have inverted exactly that.
func annotatePendingApprovals(db *sql.DB, out []boardTaskDTO) {
	if len(out) == 0 {
		return
	}
	rows, err := db.Query(`SELECT task_id, COUNT(*) FROM (` + pendingApprovalPairs + `) GROUP BY task_id`)
	if err != nil {
		log.Printf("warning: api: pending approvals annotate: %v", err)
		return
	}
	defer rows.Close()
	byID := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			log.Printf("warning: api: pending approvals scan: %v", err)
			return
		}
		byID[id] = n
	}
	if err := rows.Err(); err != nil {
		log.Printf("warning: api: pending approvals rows: %v", err)
		return
	}
	for i := range out {
		out[i].PendingApprovalCount = byID[out[i].ID]
	}
}

// publishTaskUpdated notifies WS subscribers a board task row changed. No-op
// when the bus is not attached (serve --no-ingest).
func publishTaskUpdated(id int64) {
	if wsBus != nil {
		wsBus.Publish(ingest.Notification{Type: ingest.NoteTaskUpdated, TaskID: id})
	}
}

// publishTaskDeleted notifies WS subscribers a board task row is gone. The
// project id rides along because the row can no longer be read back.
func publishTaskDeleted(id, projectID int64) {
	if wsBus != nil {
		wsBus.Publish(ingest.Notification{
			Type: ingest.NoteTaskDeleted, TaskID: id, ProjectID: projectID,
		})
	}
}

// resolveAgentName validates an optional agent selection against the agents
// registry and returns the value to store (nil = no agent). Mirrors
// resolvePlaybookName's shape: it writes its own 4xx and returns ok=false, so
// callers just `if !ok { return }`.
//
// nil pointer → leave unset; empty string → clear the selection; a non-blank
// name must resolve for THIS project, i.e. be a global agent or one scoped to
// it. The `(scope = 'global' OR project_id = ?)` predicate is specific to this
// validation — the Agent Hub roster query does no scope filtering at all, so
// there is no existing query to mirror here. Soft-deleted rows
// (deleted = 1) do not count — the registry re-scan tombstones an agent whose
// file is gone, and dispatching to it would fail at spawn time.
func (h *Handler) resolveAgentName(w http.ResponseWriter, projectID int64, name *string) (any, bool) {
	if name == nil {
		return nil, true
	}
	trimmed := strings.TrimSpace(*name)
	if trimmed == "" {
		return nil, true // clear → plain run, no agent
	}
	var one int
	err := h.DB.QueryRow(`
		SELECT 1 FROM agents
		 WHERE deleted = 0 AND name = ? AND (scope = 'global' OR project_id = ?)
		 LIMIT 1`, trimmed, projectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusBadRequest, "unknown agent: "+trimmed)
		return nil, false
	}
	if err != nil {
		writeErr(w, err)
		return nil, false
	}
	return trimmed, true
}

// optString converts the (any, ok) result of resolvePlaybookName /
// resolveAgentName — nil or a string — into the *string the constructor takes.
func optString(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

// capturedTaskInput is the payload of a capture insert: the parts a captured
// card actually varies. Everything else is fixed (see taskcap.InsertCapturedTask).
type capturedTaskInput = taskcap.Input

// insertCapturedTask is the API layer's capture entry point: it delegates the
// row write to internal/taskcap and adds the one thing taskcap deliberately
// does not do — publishing task_updated.
//
// The row logic moved out because internal/ingest needs the identical write and
// cannot reach this package: api imports ingest (AttachBus takes an
// *ingest.Bus), so an ingest→api edge would be an import cycle. taskcap is the
// shared, dependency-free bottom of that pair.
//
// Publishing stays here rather than in taskcap because delivery rules differ per
// caller: this path writes against the pool and is free to publish
// synchronously, while ingest captures inside its tail transaction and must
// hold the frame until that transaction commits. Either way, only a REAL insert
// reaches the wire — a replay changed nothing, and a frame for a no-op would
// make every re-tail look like live board activity.
func insertCapturedTask(db *sql.DB, in capturedTaskInput) (int64, bool, error) {
	id, inserted, err := taskcap.InsertCapturedTask(db, in)
	if err != nil {
		return 0, false, err
	}
	if inserted {
		publishTaskUpdated(id)
	}
	return id, inserted, nil
}

// GET /api/board/tasks?projectId=&boardColumn= — board rows (source='queue'),
// newest first. Both filters optional. Distinct from GET /api/tasks (the
// workspace 14-day summary).
func (h *Handler) listBoardTasks(w http.ResponseWriter, r *http.Request) {
	q := boardTaskSelect + ` WHERE t.source = 'queue'`
	var args []any
	if pid := r.URL.Query().Get("projectId"); pid != "" {
		q += ` AND t.project_id = ?`
		args = append(args, pid)
	}
	if col := r.URL.Query().Get("boardColumn"); col != "" {
		if !validColumn(col) {
			http.Error(w, `{"error":"unknown boardColumn"}`, http.StatusBadRequest)
			return
		}
		q += ` AND t.board_column = ?`
		args = append(args, col)
	}
	// label is normalized then filtered in Go below rather than in SQL: the
	// board already reads every card for the project/column in one query, and
	// SQLite has no JSON-array-contains operator worth reaching for at this
	// scale.
	var labelFilter string
	if lbl := r.URL.Query().Get("label"); lbl != "" {
		normalized, err := normalizeLabels([]string{lbl})
		if err != nil || len(normalized) == 0 {
			http.Error(w, `{"error":"invalid label filter"}`, http.StatusBadRequest)
			return
		}
		labelFilter = normalized[0]
	}
	q += ` ORDER BY t.id DESC`
	rows, err := h.DB.Query(q, args...)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []boardTaskDTO{}
	for rows.Next() {
		var d boardTaskDTO
		if err := scanBoardTask(rows.Scan, &d, h.InboxTTL); err != nil {
			writeErr(w, err)
			return
		}
		if labelFilter != "" && !containsLabel(d.Labels, labelFilter) {
			continue
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		writeJSON(w, out, err)
		return
	}
	// Staleness is joined in Go rather than folded into boardTaskSelect: that select
	// is shared with the single-task handler, and widening it would make every
	// caller pay for four extra aggregates. A failure here degrades the field to
	// empty and never fails the list — a missing derived hint must not cost the
	// board its data.
	annotateStaleness(h.DB, out)
	// Same reasoning, same shape: one extra query for the whole board rather than
	// four more aggregates on the select every single-task reader would then pay for.
	annotatePendingApprovals(h.DB, out)
	writeJSON(w, out, nil)
}

// annotateStaleness fills the derived staleness fields in place. Best-effort by
// design: the verdict is a hint for a human, and the board must render without it.
//
// NOTE for whoever extends this: the board lists only source='queue' rows
// (see listBoardTasks), so workspace tasks — which is where every stuck task in the
// live database actually sits — never reach this function. `swarmery stale` is the
// surface that covers them.
func annotateStaleness(db *sql.DB, out []boardTaskDTO) {
	if len(out) == 0 {
		return
	}
	verdicts, err := staleness.Load(db, 0)
	if err != nil {
		log.Printf("warning: api: staleness annotate: %v", err)
		return
	}
	byID := make(map[int64]staleness.Verdict, len(verdicts))
	for _, v := range verdicts {
		byID[v.TaskID] = v.Verdict
	}
	for i := range out {
		v, ok := byID[out[i].ID]
		if !ok || v.Kind == staleness.KindLive {
			continue // a live task carries no hint; absence is the signal
		}
		out[i].Staleness = string(v.Kind)
		out[i].StalenessReason = v.Reason
	}
}

// POST /api/board/tasks {projectId, title, prompt, priority?, model?, agent?,
// fileScope?, dependencies?, boardColumn?} → 201 DTO. requireLocalOrigin.
//
// This endpoint only ever mints origin='manual' cards. Captured cards
// (origin session|llm, carrying a capture_key) are inserted by
// insertCapturedTask, never over HTTP: the idempotency key is what makes
// capture replay-safe, and an external caller with a free hand over it could
// mint a card that permanently blocks the real capture of that same key.
func (h *Handler) createBoardTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID    int64    `json:"projectId"`
		Title        string   `json:"title"`
		Prompt       string   `json:"prompt"`
		Priority     string   `json:"priority"`
		Model        *string  `json:"model"`
		Playbook     *string  `json:"playbook"`
		Agent        *string  `json:"agent"`
		Origin       string   `json:"origin"`
		FileScope    []string `json:"fileScope"`
		Labels       []string `json:"labels"`
		Dependencies []string `json:"dependencies"`
		BoardColumn  string   `json:"boardColumn"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	title := strings.TrimSpace(body.Title)
	prompt := strings.TrimSpace(body.Prompt)
	if title == "" || prompt == "" {
		http.Error(w, `{"error":"title and prompt are required"}`, http.StatusBadRequest)
		return
	}
	column := body.BoardColumn
	if column == "" {
		column = "triage"
	}
	if !validColumn(column) {
		http.Error(w, `{"error":"unknown boardColumn"}`, http.StatusBadRequest)
		return
	}
	// Origin is capture-owned: the only value this endpoint accepts is the one
	// it would have used anyway. Rejecting loudly beats silently downgrading a
	// caller that believes it is creating a captured card.
	if body.Origin != "" && body.Origin != "manual" {
		writeClientErr(w, http.StatusBadRequest,
			"origin is capture-owned; only 'manual' may be created over HTTP")
		return
	}
	priority, err := normalizePriority(body.Priority)
	if err != nil {
		badRequest(w, err)
		return
	}
	labels, err := normalizeLabels(body.Labels)
	if err != nil {
		badRequest(w, err)
		return
	}
	// Project must exist.
	var one int
	err = h.DB.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, body.ProjectID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"unknown project id"}`, http.StatusBadRequest)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	// Playbook (fusion phase 13): optional recipe name; when set it must resolve
	// in the registry for this project (built-in or project-local). NULL ⇒ the
	// default 'standard' recipe. An unresolvable name is a client bug (400).
	playbook, ok := h.resolvePlaybookName(w, body.ProjectID, body.Playbook)
	if !ok {
		return
	}
	// Agent (0048): optional registry agent name to dispatch as. Checked after
	// the project lookup because the registry query is project-scoped.
	agent, ok := h.resolveAgentName(w, body.ProjectID, body.Agent)
	if !ok {
		return
	}
	id, _, err := store.InsertBoardTask(h.DB, store.BoardTaskInput{
		ProjectID:    body.ProjectID,
		Title:        title,
		Prompt:       prompt,
		Priority:     priority,
		Column:       column,
		Model:        body.Model,
		Playbook:     optString(playbook),
		Agent:        optString(agent),
		FileScope:    body.FileScope,
		Labels:       labels,
		Dependencies: body.Dependencies,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := h.boardTaskByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	publishTaskUpdated(id)
	// A task created directly into todo is immediately dispatchable — trigger the
	// event fast path (Poke is a no-op when created into triage or when the
	// dispatcher is not attached).
	pokeDispatch()
	writeJSONStatus(w, http.StatusCreated, d)
}

// PATCH /api/board/tasks/{id} — accepts the user-editable TaskPatch fields
// (boardColumn, title, prompt, priority, model, fileScope, dependencies,
// paused, userPaused). Dispatcher-owned fields (branch, verdict, …) are NOT
// settable here. Moving column emits task_updated. requireLocalOrigin.
func (h *Handler) patchBoardTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid task id"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		BoardColumn  *string   `json:"boardColumn"`
		Title        *string   `json:"title"`
		Prompt       *string   `json:"prompt"`
		Priority     *string   `json:"priority"`
		Model        *string   `json:"model"`
		Playbook     *string   `json:"playbook"`
		Agent        *string   `json:"agent"`
		FileScope    *[]string `json:"fileScope"`
		Labels       *[]string `json:"labels"`
		Dependencies *[]string `json:"dependencies"`
		Paused       *bool     `json:"paused"`
		UserPaused   *bool     `json:"userPaused"`
		// Provenance is immutable through this endpoint — present only so an
		// attempt to change it is a loud 400 rather than a silently dropped
		// field. A card's origin is a statement about where it came from; it
		// cannot be edited after the fact, and rewriting capture_key would
		// re-open the duplicate a captured card exists to prevent.
		Origin          *string `json:"origin"`
		CaptureKey      *string `json:"captureKey"`
		OriginSessionID *int64  `json:"originSessionId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}
	if body.Origin != nil || body.CaptureKey != nil || body.OriginSessionID != nil {
		writeClientErr(w, http.StatusBadRequest,
			"origin, captureKey and originSessionId are capture-owned and cannot be patched")
		return
	}

	// Load current row (board-scoped) for transition validation + 404.
	cur, err := h.boardTaskByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if cur == nil {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}

	var sets []string
	var args []any
	columnChanged := false

	if body.BoardColumn != nil {
		to := *body.BoardColumn
		if !validColumn(to) {
			http.Error(w, `{"error":"unknown boardColumn"}`, http.StatusBadRequest)
			return
		}
		if err := legalTransition(cur.BoardColumn, to); err != nil {
			badRequest(w, err)
			return
		}
		if to != cur.BoardColumn {
			sets = append(sets, "board_column = ?")
			args = append(args, to)
			columnChanged = true
		}
	}
	if body.Title != nil {
		t := strings.TrimSpace(*body.Title)
		if t == "" {
			http.Error(w, `{"error":"title cannot be empty"}`, http.StatusBadRequest)
			return
		}
		sets = append(sets, "title = ?")
		args = append(args, t)
	}
	if body.Prompt != nil {
		p := strings.TrimSpace(*body.Prompt)
		if p == "" {
			http.Error(w, `{"error":"prompt cannot be empty"}`, http.StatusBadRequest)
			return
		}
		sets = append(sets, "prompt = ?")
		args = append(args, p)
	}
	if body.Priority != nil {
		v, err := normalizePriority(*body.Priority)
		if err != nil {
			badRequest(w, err)
			return
		}
		sets = append(sets, "priority = ?")
		args = append(args, v)
	}
	if body.Model != nil {
		sets = append(sets, "model = ?")
		args = append(args, nullableStr(*body.Model))
	}
	if body.Playbook != nil {
		// A blank string clears the selection (→ default 'standard'); a non-blank
		// name must resolve in the registry for this project.
		playbook, ok := h.resolvePlaybookName(w, cur.ProjectID, body.Playbook)
		if !ok {
			return
		}
		sets = append(sets, "playbook = ?")
		args = append(args, playbook)
	}
	if body.Agent != nil {
		// A blank string clears the selection (→ plain run); a non-blank name
		// must resolve in the registry for this card's project.
		agent, ok := h.resolveAgentName(w, cur.ProjectID, body.Agent)
		if !ok {
			return
		}
		sets = append(sets, "agent = ?")
		args = append(args, agent)
	}
	if body.FileScope != nil {
		scopeJSON, err := marshalStringList(*body.FileScope)
		if err != nil {
			badRequest(w, err)
			return
		}
		sets = append(sets, "file_scope = ?")
		args = append(args, scopeJSON)
	}
	if body.Labels != nil {
		labels, err := normalizeLabels(*body.Labels)
		if err != nil {
			badRequest(w, err)
			return
		}
		labelsJSON, err := marshalStringList(labels)
		if err != nil {
			badRequest(w, err)
			return
		}
		sets = append(sets, "labels = ?")
		args = append(args, labelsJSON)
	}
	if body.Dependencies != nil {
		depsJSON, err := marshalStringList(*body.Dependencies)
		if err != nil {
			badRequest(w, err)
			return
		}
		sets = append(sets, "dependencies = ?")
		args = append(args, depsJSON)
	}
	if body.Paused != nil {
		sets = append(sets, "paused = ?")
		args = append(args, boolToInt(*body.Paused))
	}
	if body.UserPaused != nil {
		sets = append(sets, "user_paused = ?")
		args = append(args, boolToInt(*body.UserPaused))
	}

	if columnChanged {
		sets = append(sets, "column_moved_at = ?")
		args = append(args, time.Now().UTC().Format(boardTSFormat))
	}

	if len(sets) == 0 {
		// Nothing to change — return current state (idempotent no-op).
		writeJSON(w, cur, nil)
		return
	}

	args = append(args, id)
	if _, err := h.DB.Exec(
		`UPDATE tasks SET `+strings.Join(sets, ", ")+` WHERE id = ? AND source = 'queue'`, args...); err != nil {
		writeErr(w, err)
		return
	}
	d, err := h.boardTaskByID(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	publishTaskUpdated(id)
	if columnChanged {
		h.applyTerminalColumnEffects(id, d.BoardColumn)
	}
	// Poke so the scheduler reacts to the fast path — a move to todo, an unpause,
	// or a dependency reaching done/archived (FN-3895: unblocking dependents must
	// be an event, not the 15s sweep). No-op when the dispatcher is not attached.
	pokeDispatch()
	writeJSON(w, d, nil)
}

// applyTerminalColumnEffects performs the side effects a board card's arrival in
// a terminal column carries: reclaiming its worktree (done and archived both,
// keeping the branch) and resolving the plan phase it was minted from (done
// only — archiving can mean "abandoned", and abandoned work must not tick a
// plan's acceptance boxes). A no-op for every non-terminal column, so callers
// need no guard of their own.
//
// Extracted from patchBoardTask because the review exits (land, discard in
// tasks_review.go) must fire the IDENTICAL effects: a card landed by the button
// and a card dragged to done are the same state, and a second inline copy is
// exactly how the two would drift — one of them silently stopping to reclaim
// worktrees or tick phase docs. Both hooks are no-ops when the dispatcher is not
// attached / the task was minted from no phase.
func (h *Handler) applyTerminalColumnEffects(id int64, column string) {
	if dispatchSvc != nil && (column == "done" || column == "archived") {
		dispatchSvc.RemoveWorktreeFor(id)
	}
	if column == "done" {
		if n, err := wsingest.TickPhaseChecklist(h.DB, id); err != nil {
			log.Printf("warn: api: tick phase checklist (task %d): %v", id, err)
		} else if n > 0 {
			log.Printf("api: task %d done — ticked %d phase checkbox(es)", id, n)
		}
	}
}

// DELETE /api/board/tasks/{id} → 204. Permanently removes a board row the user
// no longer wants — the escape hatch Archive is not: an archived task still
// shows up in the column and in every dependency picker. Board-scoped: only
// source='queue' rows are deletable (a workspace-ingested task is owned by the
// disk, deleting the row would just resurrect it on the next scan) — anything
// else is a 404.
//
// A RUNNING task is refused with 409 rather than force-killed: the row is the
// only handle the daemon has on a live headless agent (its process group, its
// worktree, its session link), so dropping it would orphan all three. Stop it
// first by moving it to done/archived, which reclaims the worktree.
func (h *Handler) deleteBoardTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, `{"error":"invalid task id"}`, http.StatusBadRequest)
		return
	}
	var source, status string
	var projectID int64
	var worktreePath sql.NullString
	err = h.DB.QueryRow(
		`SELECT source, status, project_id, worktree_path FROM tasks WHERE id = ?`, id).
		Scan(&source, &status, &projectID, &worktreePath)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if source != "queue" {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}
	if status == "running" {
		http.Error(w,
			`{"error":"task is running — move it to done or archived first (that stops it and reclaims the worktree)"}`,
			http.StatusConflict)
		return
	}
	// Reclaim a worktree the row still holds. A task can carry one without being
	// running (a failed admission, a crash before the terminal move) and the row
	// is about to stop being reachable, so this is the last chance to free it.
	// Best-effort: a failed removal is logged inside the dispatcher and must not
	// block the delete, otherwise a stale worktree makes the card undeletable.
	if dispatchSvc != nil && strings.TrimSpace(worktreePath.String) != "" {
		dispatchSvc.RemoveWorktreeFor(id)
	}

	tx, err := h.DB.Begin()
	if err != nil {
		writeErr(w, err)
		return
	}
	defer tx.Rollback() //nolint:errcheck // no-op after a successful Commit
	// task_sessions predates ON DELETE CASCADE (0006_workspaces.sql) and the
	// store opens SQLite with foreign_keys(1), so its link rows must go first or
	// the row delete fails on a task that ever ran. Every other child table
	// (retro, verification, epic_phases, plan_runs) declares CASCADE.
	if _, err := tx.Exec(`DELETE FROM task_sessions WHERE task_id = ?`, id); err != nil {
		writeErr(w, err)
		return
	}
	// epic_phases.activated_board_task_id is a soft reference (no FK), so it
	// would dangle. Clearing it releases the phase to be activated again.
	if _, err := tx.Exec(
		`UPDATE epic_phases SET activated_board_task_id = NULL WHERE activated_board_task_id = ?`, id); err != nil {
		writeErr(w, err)
		return
	}
	res, err := tx.Exec(`DELETE FROM tasks WHERE id = ? AND source = 'queue'`, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, err)
		return
	}
	publishTaskDeleted(id, projectID)
	w.WriteHeader(http.StatusNoContent)
}

// bulkArchiveResult is the amnesty response: how many rows the predicate
// selected, and how many were actually written. A dry run reports
// matched=N/archived=0; an execute reports them equal.
type bulkArchiveResult struct {
	Matched  int64 `json:"matched"`
	Archived int64 `json:"archived"`
}

// POST /api/board/tasks/bulk-archive {projectId?, column, before, dryRun?} →
// 200 {matched, archived}. requireLocalOrigin.
//
// The one-shot counterpart to the TTL sweeper: the sweeper stops the inbox from
// becoming a graveyard from here on, this clears the graveyard that already
// accumulated before the sweeper existed. Both stand on the SAME predicate —
// taskcap.StaleInboxWhere — so an amnesty can never reach a row the automatic
// sweep is forbidden to touch (a hand-written card, an accepted card, a card
// the dispatcher owns). The only thing this adds is narrowing: an explicit
// cutoff and an optional project fence.
//
// Two guards make the bulk write safe to expose:
//
//   - `column` is required and only "triage" is accepted. It carries no
//     information the handler could not have assumed — that is the point: a
//     caller must name the column it means, so a future column can never be
//     swept by a client that predates it.
//   - `before` is required with no default. A bulk archive has no undo, so the
//     blast radius is always the caller's explicit decision, never an implied
//     one; dryRun then lets the UI show that radius before committing to it.
//
// Deliberately publishes NO task_updated frames. A 231-row amnesty would put
// 231 frames on the bus to say one thing, and useBoard already reconciles by
// refetching the whole list (60s tick + reconnect) — the initiating client
// reloads off this response, other tabs converge on the next tick. The trade is
// up-to-60s staleness in a second tab against a bus flood, and the flood is the
// worse outcome.
func (h *Handler) bulkArchiveBoardTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID int64  `json:"projectId"` // 0/omitted = every project
		Column    string `json:"column"`
		Before    string `json:"before"`
		DryRun    bool   `json:"dryRun"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.Column) != "triage" {
		writeClientErr(w, http.StatusBadRequest,
			`column is required and must be "triage" — amnesty applies to the inbox only`)
		return
	}
	before := strings.TrimSpace(body.Before)
	if before == "" {
		writeClientErr(w, http.StatusBadRequest,
			"before is required — a bulk archive must name its own cutoff")
		return
	}
	cutoffAt, err := time.Parse(time.RFC3339, before)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest,
			"before must be an RFC3339 instant, e.g. 2026-08-04T00:00:00Z")
		return
	}
	// Re-render the cutoff in the stored millisecond-Z format rather than
	// comparing the caller's string directly: the comparison is lexical, and
	// "…:00Z" sorts AFTER "…:00.000Z" ('Z' > '.'), so an un-normalized cutoff
	// would quietly include a second's worth of rows the caller excluded.
	cutoff := cutoffAt.UTC().Format(boardTSFormat)

	where := taskcap.StaleInboxWhere + ` AND ` + taskcap.InboxIdleSince + ` < ?`
	args := []any{cutoff}
	if body.ProjectID != 0 {
		where += ` AND project_id = ?`
		args = append(args, body.ProjectID)
	}

	if body.DryRun {
		var matched int64
		if err := h.DB.QueryRow(`SELECT COUNT(*) FROM tasks WHERE `+where, args...).Scan(&matched); err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, bulkArchiveResult{Matched: matched}, nil)
		return
	}

	// One UPDATE rather than count-then-update: a separate count could only
	// disagree with the write it precedes, and RowsAffected already reports the
	// truth. matched == archived on an execute is not a shortcut — it IS the
	// number of rows the predicate selected at write time.
	now := time.Now().UTC().Format(boardTSFormat)
	res, err := h.DB.Exec(`
		UPDATE tasks
		   SET board_column = 'archived',
		       status = 'cancelled',
		       column_moved_at = ?,
		       archived_at = ?
		 WHERE `+where, append([]any{now, now}, args...)...)
	if err != nil {
		writeErr(w, err)
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		writeErr(w, err)
		return
	}
	if n > 0 {
		log.Printf("api: inbox amnesty archived %d captured card(s) older than %s", n, cutoff)
	}
	writeJSON(w, bulkArchiveResult{Matched: n, Archived: n}, nil)
}

// badRequest writes a 400 with the error's message as JSON.
func badRequest(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
