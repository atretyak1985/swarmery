package api

// Routines endpoints (fusion phase 7 — scheduled automation): CRUD over
// cron/webhook/manual routines, a manual run trigger, a token-gated webhook
// trigger, and the run history. The scheduler + executor live in
// internal/routines; these handlers are the thin write/read surface. The service
// is attached once at daemon startup (AttachRoutines) — the same package-var
// idiom as dispatchSvc/approvalsSvc — so httptest handlers built with
// &Handler{DB: db} stay hermetic (routinesSvc nil ⇒ endpoints 503).
//
// Writes carry the D4 requireLocalOrigin hardening (see routes.go), EXCEPT the
// webhook trigger POST /api/hooks/routine/{id}/{token}: it is designed for
// external callers (CI, cron on another box) so it cannot require a local
// origin; instead it authenticates with a constant-time token compare and
// returns 404 (never 403) on any miss so it never confirms a routine's
// existence to an unauthenticated probe.

import (
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/routines"
)

// routinesSvc is attached once at daemon startup (nil ⇒ routines endpoints 503).
var routinesSvc *routines.Service

// AttachRoutines wires the routines service into the api layer.
func AttachRoutines(s *routines.Service) { routinesSvc = s }

// ── DTOs (camelCase; mirrored in web/src/api/types.ts) ──

type routineDTO struct {
	ID           string          `json:"id"`
	ProjectID    *int64          `json:"projectId"`
	Name         string          `json:"name"`
	CronExpr     string          `json:"cronExpr"`
	Enabled      bool            `json:"enabled"`
	CatchUp      string          `json:"catchUp"`
	Steps        []routines.Step `json:"steps"`
	HasWebhook   bool            `json:"hasWebhook"`
	WebhookToken string          `json:"webhookToken,omitempty"` // returned ONLY on create/rotate
	TimeoutSec   int             `json:"timeoutSec"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
	LastRunAt    *string         `json:"lastRunAt"`
	NextRunAt    *string         `json:"nextRunAt"`
}

type routineRunDTO struct {
	ID         int64   `json:"id"`
	Trigger    string  `json:"trigger"`
	Status     string  `json:"status"`
	Detail     *string `json:"detail"`
	StartedAt  string  `json:"startedAt"`
	FinishedAt *string `json:"finishedAt"`
}

// toRoutineDTO shapes a routines.Routine for the API. showToken reveals the
// webhook token (create/rotate responses only); list/get never leak it.
func toRoutineDTO(r routines.Routine, showToken bool) routineDTO {
	d := routineDTO{
		ID:         r.ID,
		Name:       r.Name,
		CronExpr:   r.CronExpr,
		Enabled:    r.Enabled,
		CatchUp:    r.CatchUp,
		Steps:      r.Steps,
		HasWebhook: r.WebhookToken != "",
		TimeoutSec: r.TimeoutSec,
		CreatedAt:  r.CreatedAt,
		UpdatedAt:  r.UpdatedAt,
	}
	if r.Steps == nil {
		d.Steps = []routines.Step{}
	}
	if r.ProjectID.Valid {
		v := r.ProjectID.Int64
		d.ProjectID = &v
	}
	if r.LastRunAt.Valid {
		d.LastRunAt = &r.LastRunAt.String
	}
	if r.NextRunAt.Valid {
		d.NextRunAt = &r.NextRunAt.String
	}
	if showToken {
		d.WebhookToken = r.WebhookToken
	}
	return d
}

// GET /api/routines?projectId= — all routines (optionally project-scoped),
// newest first. Tokens are never included.
func (h *Handler) listRoutines(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	var projectID int64
	if pid := r.URL.Query().Get("projectId"); pid != "" {
		id, err := strconv.ParseInt(pid, 10, 64)
		if err != nil {
			writeClientErr(w, http.StatusBadRequest, "invalid projectId")
			return
		}
		projectID = id
	}
	list, err := routinesSvc.List(projectID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]routineDTO, 0, len(list))
	for _, rt := range list {
		out = append(out, toRoutineDTO(rt, false))
	}
	writeJSON(w, out, nil)
}

// routineBody is the create/update request shape.
type routineBody struct {
	ProjectID  *int64          `json:"projectId"`
	Name       string          `json:"name"`
	CronExpr   string          `json:"cronExpr"`
	Enabled    *bool           `json:"enabled"`
	CatchUp    string          `json:"catchUp"`
	Steps      []routines.Step `json:"steps"`
	TimeoutSec *int            `json:"timeoutSec"`
	// Webhook, when set, requests token behavior: true → mint/keep a token,
	// false → clear it. On create, true mints a fresh token.
	Webhook *bool `json:"webhook"`
}

// validateRoutineCommon validates the fields shared by create + update:
// non-empty name, a valid cron (when provided), a known catch-up policy (when
// provided), a positive timeout (when provided), and a valid steps slice.
func validateRoutineCommon(name, cronExpr, catchUp string, timeout *int, steps []routines.Step, stepsRequired bool) (validSteps []routines.Step, errMsg string) {
	if name == "" {
		return nil, "name is required"
	}
	if cronExpr != "" {
		if _, err := routines.ParseCron(cronExpr); err != nil {
			return nil, err.Error()
		}
	}
	if catchUp != "" && catchUp != "skip" && catchUp != "run_one" {
		return nil, "catchUp must be skip or run_one"
	}
	if timeout != nil && *timeout <= 0 {
		return nil, "timeoutSec must be positive"
	}
	if stepsRequired || steps != nil {
		vs, err := routines.ValidateStepSlice(steps)
		if err != nil {
			return nil, err.Error()
		}
		return vs, ""
	}
	return nil, ""
}

// POST /api/routines — create a routine. requireLocalOrigin. Returns 201 with
// the webhook token IF one was minted (the only time it is exposed).
func (h *Handler) createRoutine(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	var body routineBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	steps, msg := validateRoutineCommon(body.Name, body.CronExpr, body.CatchUp, body.TimeoutSec, body.Steps, true)
	if msg != "" {
		writeClientErr(w, http.StatusBadRequest, msg)
		return
	}

	// Project scope: when set, the project must exist.
	var projectID sql.NullInt64
	if body.ProjectID != nil {
		if err := h.DB.QueryRow(`SELECT 1 FROM projects WHERE id=?`, *body.ProjectID).Scan(new(int)); err != nil {
			writeClientErr(w, http.StatusBadRequest, "unknown project id")
			return
		}
		projectID = sql.NullInt64{Int64: *body.ProjectID, Valid: true}
	}

	catchUp := body.CatchUp
	if catchUp == "" {
		catchUp = "skip"
	}
	timeout := 900
	if body.TimeoutSec != nil {
		timeout = *body.TimeoutSec
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	var token string
	if body.Webhook != nil && *body.Webhook {
		t, err := routines.NewToken()
		if err != nil {
			writeErr(w, err)
			return
		}
		token = t
	}

	rt, err := routinesSvc.Create(routines.CreateParams{
		ProjectID: projectID, Name: body.Name, CronExpr: body.CronExpr,
		Enabled: enabled, CatchUp: catchUp, Steps: steps,
		WebhookToken: token, TimeoutSec: timeout,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, toRoutineDTO(rt, true))
}

// PATCH /api/routines/{id} — partial update. requireLocalOrigin. A webhook=true
// with no existing token mints one (returned); webhook=false clears it.
func (h *Handler) patchRoutine(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	id := r.PathValue("id")
	cur, err := routinesSvc.Get(id)
	if errors.Is(err, routines.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	var body routineBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Validate only the provided fields. Name defaults to the current when blank
	// so a metadata-only PATCH (e.g. {enabled:false}) is not forced to resend it.
	name := body.Name
	if name == "" {
		name = cur.Name
	}
	if _, msg := validateRoutineCommon(name, body.CronExpr, body.CatchUp, body.TimeoutSec, body.Steps, false); msg != "" {
		writeClientErr(w, http.StatusBadRequest, msg)
		return
	}

	var up routines.UpdateParams
	if body.Name != "" {
		up.Name = &body.Name
	}
	// cronExpr is set-through even to "" (clearing the schedule), so we cannot use
	// the zero value as "absent". A dedicated marker keeps the contract simple:
	// the field is applied whenever the key is present in the body — detected via
	// a second raw decode below is overkill, so instead we treat cronExpr as
	// always-settable (its validation already ran) and let the client send the
	// current value when it does not intend to change it. This matches the web
	// editor, which always submits the full form.
	up.CronExpr = &body.CronExpr
	if body.Enabled != nil {
		up.Enabled = body.Enabled
	}
	if body.CatchUp != "" {
		up.CatchUp = &body.CatchUp
	}
	if body.Steps != nil {
		up.Steps = &body.Steps
	}
	if body.TimeoutSec != nil {
		up.TimeoutSec = body.TimeoutSec
	}
	showToken := false
	if body.Webhook != nil {
		if *body.Webhook {
			// Mint a token only if there is not already one (idempotent enable).
			if cur.WebhookToken == "" {
				t, err := routines.NewToken()
				if err != nil {
					writeErr(w, err)
					return
				}
				up.WebhookToken = &t
				showToken = true
			}
		} else {
			empty := ""
			up.WebhookToken = &empty
		}
	}

	rt, err := routinesSvc.Update(id, up)
	if errors.Is(err, routines.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, toRoutineDTO(rt, showToken), nil)
}

// DELETE /api/routines/{id} — remove a routine (runs cascade). requireLocalOrigin.
func (h *Handler) deleteRoutine(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	id := r.PathValue("id")
	err := routinesSvc.Delete(id)
	if errors.Is(err, routines.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// POST /api/routines/{id}/run — manual trigger. requireLocalOrigin. 202 when
// started; 202 with "busy" when the routine is already running or the global cap
// is full (the run is not queued — the caller can retry).
func (h *Handler) runRoutine(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	id := r.PathValue("id")
	started, err := routinesSvc.Trigger(id, "manual")
	if errors.Is(err, routines.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	status := "started"
	if !started {
		status = "busy"
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": status, "id": id})
}

// POST /api/hooks/routine/{id}/{token} — webhook trigger. NO requireLocalOrigin
// (external callers), token-gated with a constant-time compare. Any miss —
// unknown id OR wrong token — returns 404 so an unauthenticated probe cannot
// distinguish "no such routine" from "wrong token".
func (h *Handler) hookRoutine(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	id := r.PathValue("id")
	token := r.PathValue("token")

	rt, err := routinesSvc.Get(id)
	if errors.Is(err, routines.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	// A routine with no webhook token cannot be triggered this way; treat as 404
	// (same opaque response as a wrong token). Constant-time compare on the token.
	if rt.WebhookToken == "" || subtle.ConstantTimeCompare([]byte(rt.WebhookToken), []byte(token)) != 1 {
		writeClientErr(w, http.StatusNotFound, "not found")
		return
	}

	started, err := routinesSvc.Trigger(id, "webhook")
	if err != nil {
		writeErr(w, err)
		return
	}
	status := "started"
	if !started {
		status = "busy"
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]any{"status": status, "id": id})
}

// GET /api/routines/{id}/runs — run history (newest first, capped). 404 unknown.
func (h *Handler) listRoutineRuns(w http.ResponseWriter, r *http.Request) {
	if routinesSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "routines not attached")
		return
	}
	id := r.PathValue("id")
	// Confirm the routine exists so an unknown id is a clean 404 (Runs of an
	// unknown id would just return []).
	if _, err := routinesSvc.Get(id); errors.Is(err, routines.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "routine not found")
		return
	} else if err != nil {
		writeErr(w, err)
		return
	}
	runs, err := routinesSvc.Runs(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]routineRunDTO, 0, len(runs))
	for _, rn := range runs {
		d := routineRunDTO{ID: rn.ID, Trigger: rn.Trigger, Status: rn.Status, StartedAt: rn.StartedAt}
		if rn.Detail.Valid {
			d.Detail = &rn.Detail.String
		}
		if rn.FinishedAt.Valid {
			d.FinishedAt = &rn.FinishedAt.String
		}
		out = append(out, d)
	}
	writeJSON(w, out, nil)
}
