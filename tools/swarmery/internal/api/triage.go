package api

// Inbox triage agent (phase 2): start/read triage runs, list verdicts, undo an
// applied verdict. Every route answers 503 until the daemon attaches the
// service; the two mutating routes carry the D4 local-origin fence.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// triageSvc is attached once at daemon startup (nil ⇒ triage endpoints 503).
var triageSvc *triage.Service

// AttachTriage wires the triage service into the api layer.
func AttachTriage(s *triage.Service) { triageSvc = s }

func triageReady(w http.ResponseWriter) bool {
	if triageSvc == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "triage service not attached")
		return false
	}
	return true
}

// resolveTriageProject maps a ?project=/body project (slug|name|id) to its id;
// ok=false (with no error) means the project is unknown.
func (h *Handler) resolveTriageProject(p string) (int64, bool, error) {
	var id int64
	err := h.DB.QueryRow(`SELECT id FROM projects WHERE `+projectMatchExpr("")+` ORDER BY id LIMIT 1`,
		scopeArgs(p)...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

type triageStartBody struct {
	Project json.RawMessage `json:"project"`
	Kinds   []string        `json:"kinds"`
	Trigger string          `json:"trigger"`
	Cap     int             `json:"cap"`
}

// rawScalar turns a JSON string or number into its plain text ("" for null/absent).
func rawScalar(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	t := strings.TrimSpace(string(raw))
	if t == "null" {
		return ""
	}
	return t
}

// POST /api/triage/runs {"project"?, "kinds"?, "trigger"?, "cap"?} → 202 {"id"}.
func (h *Handler) startTriageRun(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	var body triageStartBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req := triage.StartReq{Kinds: body.Kinds, Trigger: body.Trigger, Cap: body.Cap}
	if req.Trigger != "" && req.Trigger != triage.TriggerOperator && req.Trigger != triage.TriggerSchedule {
		writeClientErr(w, http.StatusBadRequest, "trigger must be operator or schedule")
		return
	}
	if p := rawScalar(body.Project); p != "" {
		id, ok, err := h.resolveTriageProject(p)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			writeClientErr(w, http.StatusNotFound, "unknown project")
			return
		}
		req.Scope.ProjectID = id
	}
	id, err := triageSvc.Start(req)
	if errors.Is(err, triage.ErrBusy) {
		var active any
		if a, aerr := triageSvc.ActiveRun(); aerr == nil && a != nil {
			active = a.ID
		}
		writeJSONStatus(w, http.StatusConflict, map[string]any{"error": err.Error(), "activeRunId": active})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSONStatus(w, http.StatusAccepted, map[string]int64{"id": id})
}

// GET /api/triage/runs?limit= — newest first.
func (h *Handler) listTriageRuns(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := triageSvc.ListRuns(limit)
	writeJSON(w, map[string]any{"items": runs}, err)
}

// GET /api/triage/runs/active — the running run, or JSON null.
func (h *Handler) activeTriageRun(w http.ResponseWriter, _ *http.Request) {
	if !triageReady(w) {
		return
	}
	run, err := triageSvc.ActiveRun()
	writeJSON(w, run, err)
}

// GET /api/triage/runs/{id}.
func (h *Handler) getTriageRun(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid run id")
		return
	}
	run, err := triageSvc.GetRun(id)
	if errors.Is(err, triage.ErrNotFound) {
		writeClientErr(w, http.StatusNotFound, "run not found")
		return
	}
	writeJSON(w, run, err)
}

// GET /api/triage/verdicts?state=a,b&kind=&run=&project=&limit= — sweeps
// resolved suggestions first, then lists newest first. An unknown project
// yields an empty list, never the whole fleet's.
func (h *Handler) listTriageVerdicts(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	q := r.URL.Query()
	f := triage.VerdictFilter{Kind: strings.TrimSpace(q.Get("kind"))}
	for _, st := range strings.Split(q.Get("state"), ",") {
		if st = strings.TrimSpace(st); st != "" {
			f.States = append(f.States, st)
		}
	}
	f.RunID, _ = strconv.ParseInt(q.Get("run"), 10, 64)
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if p := strings.TrimSpace(q.Get("project")); p != "" {
		id, ok, err := h.resolveTriageProject(p)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			writeJSON(w, map[string]any{"items": []triage.Verdict{}}, nil)
			return
		}
		f.ProjectID = id
	}
	// Throttled to one sweep per 10 s: every list read used to walk every
	// open verdict through its Source.
	if err := triageSvc.MaybeSweepOpen(r.Context()); err != nil {
		writeErr(w, err)
		return
	}
	items, err := triageSvc.ListVerdicts(f)
	writeJSON(w, map[string]any{"items": items}, err)
}

// POST /api/triage/verdicts/{id}/undo → 200 verdict; not undoable → 409.
func (h *Handler) undoTriageVerdict(w http.ResponseWriter, r *http.Request) {
	if !triageReady(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid verdict id")
		return
	}
	// The undo must finish even if the browser goes away mid-request.
	v, err := triageSvc.Undo(context.WithoutCancel(r.Context()), id)
	switch {
	case errors.Is(err, triage.ErrNotFound):
		writeClientErr(w, http.StatusNotFound, "verdict not found")
	case errors.Is(err, triage.ErrNotUndoable):
		writeClientErr(w, http.StatusConflict, "verdict is not undoable (state "+v.State+")")
	case err == nil:
		// An undo is the operator contradicting the triage agent: one ledger
		// row (internal/corrections). The reason names the kind and value so
		// R14 folds "undo friction/noise" repeats together instead of every
		// undo of anything under one key. Never fails the undo.
		corrections.Record(h.DB, corrections.Correction{
			Source:    corrections.SourceTriageUndo,
			Ref:       "verdict:" + strconv.FormatInt(v.ID, 10),
			ProjectID: v.ProjectID,
			Before:    v.Value,
			After:     string(v.Prior),
			Reason:    "undo " + v.Kind + "/" + v.Value,
		}, time.Now())
		writeJSON(w, v, nil)
	default:
		writeJSON(w, v, err)
	}
}

// triageAudit is the GET /api/triage/audit body: totals plus one row per question.
type triageAudit struct {
	Answered   int               `json:"answered"`
	Agree      int               `json:"agree"`
	ByQuestion []triage.AuditRow `json:"byQuestion"`
}

// GET /api/triage/audit → how often the agent's audit-sample answers agreed
// with the labels the operator later gave. Read-only.
func (h *Handler) getTriageAudit(w http.ResponseWriter, _ *http.Request) {
	if !triageReady(w) {
		return
	}
	rows, err := triage.ClassifierAudit(triageSvc.DB)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	out := triageAudit{ByQuestion: rows}
	for _, r := range rows {
		out.Answered += r.Answered
		out.Agree += r.Agree
	}
	writeJSON(w, out, nil)
}
