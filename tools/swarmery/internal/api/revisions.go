package api

// Plan revisions over HTTP (plan-revision phase 3): start a revise wizard,
// list a task's revisions, render one revision's live diffs, and decide it —
// Apply (the one irreversible step, internal/planrev.Apply) or Reject.
//
// Attach discipline matches internal/api/planning.go: the endpoints serve 503
// until AttachPlanning wires the planning service (serve --no-ingest, bare
// test handlers). The apply rescan is a second, independent attachment
// (AttachRevisionRescan) — nil ⇒ no-op, tests stay hermetic.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planning"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/planrev"
)

// revisionRescan, when attached, pokes the SAME wsingest scan pass that
// converges plan-doc writes (cmd/swarmery/main.go wires it to the workspace
// scanner) so the Plans page never renders a half-applied plan.
var revisionRescan func(planDir string)

// AttachRevisionRescan wires the post-apply rescan callback. nil detaches
// (tests); planrev.Apply treats nil as a no-op.
func AttachRevisionRescan(fn func(planDir string)) { revisionRescan = fn }

// maxRevisionReasonLen bounds the operator's reason (prose, not a spec).
const maxRevisionReasonLen = 8000

// revisionFileDTO is the list-endpoint file shape: what changes, never the
// content — the detail endpoint renders diffs.
type revisionFileDTO struct {
	DocPath    string `json:"docPath"`
	Action     string `json:"action"`
	RenameFrom string `json:"renameFrom,omitempty"`
}

// revisionDTO is the wire shape of one revision (list + detail).
type revisionDTO struct {
	ID             int64             `json:"id"`
	Status         string            `json:"status"`
	Origin         string            `json:"origin"`
	Reason         string            `json:"reason"`
	TriggerPhaseID *int64            `json:"triggerPhaseId,omitempty"`
	SessionUUID    string            `json:"sessionUuid,omitempty"`
	Error          string            `json:"error,omitempty"`
	CreatedAt      string            `json:"createdAt"`
	DecidedAt      string            `json:"decidedAt,omitempty"`
	DecidedBy      string            `json:"decidedBy,omitempty"`
	Files          []revisionFileDTO `json:"files"`
}

func toRevisionDTO(r planrev.Revision) revisionDTO {
	files := make([]revisionFileDTO, 0, len(r.Files))
	for _, f := range r.Files {
		files = append(files, revisionFileDTO{DocPath: f.DocPath, Action: f.Action, RenameFrom: f.RenameFrom})
	}
	return revisionDTO{
		ID: r.ID, Status: r.Status, Origin: r.Origin, Reason: r.Reason,
		TriggerPhaseID: r.TriggerPhaseID, SessionUUID: r.SessionUUID, Error: r.Error,
		CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt, DecidedBy: r.DecidedBy,
		Files: files,
	}
}

// parseRevisionIDParam parses {revisionId}; writes a 400 on failure.
func parseRevisionIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("revisionId"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid revision id")
		return 0, false
	}
	return id, true
}

// POST /api/epics/{taskId}/revisions {reason, phaseId?} → 202 {sessionUuid}.
// requireLocalOrigin. 400 empty reason; 404 unknown task; 409 plan busy / a
// staged revision already open (body names it) / a planning session already
// active; 503 planning not attached. Origin (operator_revise vs
// phase_diagnosis) is selected by phaseId inside the planning service.
func (h *Handler) startRevision(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason  string `json:"reason"`
		PhaseID *int64 `json:"phaseId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Reason) > maxRevisionReasonLen {
		writeClientErr(w, http.StatusRequestEntityTooLarge, "reason too long")
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		writeClientErr(w, http.StatusBadRequest, "reason is required")
		return
	}

	uuid, err := planningSvc.StartRevise(taskID, body.Reason, body.PhaseID)
	switch {
	case errors.Is(err, planning.ErrTaskNotFound):
		writeClientErr(w, http.StatusNotFound, "workspace task not found")
		return
	case errors.Is(err, planning.ErrNoPlan):
		writeClientErr(w, http.StatusConflict, "task has no plan directory")
		return
	case errors.Is(err, planning.ErrNoPath):
		writeClientErr(w, http.StatusConflict, "project has no known path to plan in")
		return
	case errors.Is(err, planning.ErrPlanBusy):
		writeClientErr(w, http.StatusConflict, "plan has a running phase or an active plan run")
		return
	case errors.Is(err, planning.ErrRevisionOpen):
		// Name the open revision so the UI can offer "review it" instead of a
		// dead end.
		payload := map[string]any{"error": "a staged revision is already open for this plan"}
		if rev, rerr := planrev.LatestStaged(h.DB, taskID); rerr == nil && rev != nil {
			payload["revisionId"] = rev.ID
		}
		writeJSONStatus(w, http.StatusConflict, payload)
		return
	case errors.Is(err, planning.ErrActive):
		writeClientErr(w, http.StatusConflict, "a planning run is already active for this project")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]string{"sessionUuid": uuid})
}

// GET /api/epics/{taskId}/revisions → 200 {revisions:[…]} newest first, file
// actions without content. 404 unknown task; 503 planning not attached.
func (h *Handler) listRevisions(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	taskID, ok := parseTaskIDParam(w, r)
	if !ok {
		return
	}
	var one int
	err := h.DB.QueryRow(`SELECT 1 FROM tasks WHERE id = ? AND source = 'workspace'`, taskID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "workspace task not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	revs, err := planrev.ListByTask(h.DB, taskID)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos := make([]revisionDTO, 0, len(revs))
	for _, rev := range revs {
		dtos = append(dtos, toRevisionDTO(rev))
	}
	writeJSON(w, map[string]any{"revisions": dtos}, nil)
}

// GET /api/revisions/{revisionId} → 200 {revision, files:[{docPath, action,
// renameFrom, stale, diff}]}. The per-file live diff + stale computation lives
// in planrev.LiveDiffs — one shared implementation for the handler and the e2e
// test. 404 unknown; 503 planning not attached.
func (h *Handler) getRevision(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, ok := parseRevisionIDParam(w, r)
	if !ok {
		return
	}
	rev, err := planrev.Get(h.DB, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rev == nil {
		writeClientErr(w, http.StatusNotFound, "revision not found")
		return
	}
	writeJSON(w, map[string]any{"revision": toRevisionDTO(*rev), "files": planrev.LiveDiffs(rev)}, nil)
}

// POST /api/revisions/{revisionId}/apply → 200 {status:"applied", files:N}.
// requireLocalOrigin. 409 on stale files (body lists every conflict), on a
// running target phase, and on a revision that is not staged; 404 unknown;
// 503 planning not attached.
func (h *Handler) applyRevision(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, ok := parseRevisionIDParam(w, r)
	if !ok {
		return
	}
	n, err := planrev.Apply(h.DB, id, time.Now, revisionRescan)
	var cerr *planrev.ConflictError
	switch {
	case errors.As(err, &cerr):
		writeJSONStatus(w, http.StatusConflict, map[string]any{
			"error":     "content changed on disk since staging",
			"conflicts": cerr.Conflicts,
		})
		return
	case errors.Is(err, planrev.ErrRevisionNotFound):
		writeClientErr(w, http.StatusNotFound, "revision not found")
		return
	case errors.Is(err, planrev.ErrNotStaged):
		writeClientErr(w, http.StatusConflict, "revision is not staged")
		return
	case errors.Is(err, planrev.ErrPhaseRunning):
		writeClientErr(w, http.StatusConflict, "a target phase is running — apply refused")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"status": "applied", "files": n}, nil)
}

// POST /api/revisions/{revisionId}/reject {note?} → 200 {status:"rejected"}.
// requireLocalOrigin. The note is appended to reason as "Rejected: …" and the
// decision is stamped decided_by='operator'. 409 when not staged; 404 unknown;
// 503 planning not attached.
func (h *Handler) rejectRevision(w http.ResponseWriter, r *http.Request) {
	if planningSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "planning not attached")
		return
	}
	id, ok := parseRevisionIDParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	rev, err := planrev.Get(h.DB, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if rev == nil {
		writeClientErr(w, http.StatusNotFound, "revision not found")
		return
	}
	now := time.Now()
	ts := now.UTC().Format(time.RFC3339)
	won, err := planrev.Decide(h.DB, id, planrev.StatusRejected, "operator", ts)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !won {
		writeClientErr(w, http.StatusConflict, "revision is not staged")
		return
	}
	note := strings.TrimSpace(body.Note)
	if note != "" {
		if _, err := h.DB.Exec(
			`UPDATE plan_revisions SET reason = reason || ? WHERE id = ?`,
			"\n\nRejected: "+note, id); err != nil {
			writeErr(w, err)
			return
		}
	}
	// The reject is the operator overriding a staged revision: one ledger row
	// (internal/corrections), `before` = what the revision set out to do, the
	// note = the operator's reason. Never fails the reject.
	corrections.Record(h.DB, corrections.Correction{
		Source: corrections.SourceRevisionReject,
		Ref:    "revision:" + strconv.FormatInt(id, 10),
		Before: rev.Reason,
		Reason: note,
	}, now)
	writeJSON(w, map[string]any{"status": "rejected"}, nil)
}
