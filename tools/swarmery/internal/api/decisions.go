package api

// Decision-classifier endpoints (learning-loop phase 9, internal/decide):
//
//	GET  /api/decisions                     → {configured, local, claude, questions:[QuestionStats]}
//	PUT  /api/decisions/{question}/mode     {mode: off|shadow|active} → 200 QuestionStats[]
//	POST /api/decisions/{id}/ground-truth   {value} → 204 (the operator says what actually happened)
//	GET  /api/decisions/labels/{uuid}       → {labels: SessionLabel | null}
//
// The engine is attached once at daemon startup (AttachDecide); unit tests
// leave it nil and the endpoints still answer from the DB, with every question
// reported in its DB/default mode.

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
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/decide"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
)

var decideEngine *decide.Engine

// AttachDecide wires the daemon's classifier engine into the api layer.
func AttachDecide(e *decide.Engine) { decideEngine = e }

type decisionsResponse struct {
	Configured bool                   `json:"configured"`
	Local      bool                   `json:"local"`
	Claude     bool                   `json:"claude"`
	Questions  []decide.QuestionStats `json:"questions"`
}

func (h *Handler) decisionsResponse() (decisionsResponse, error) {
	qs, err := decide.Summary(h.DB, decideEngine)
	if err != nil {
		return decisionsResponse{}, err
	}
	out := decisionsResponse{Questions: qs, Configured: decideEngine.Configured()}
	if decideEngine != nil {
		out.Local, out.Claude = decideEngine.Local != nil, decideEngine.Claude != nil
	}
	return out, nil
}

func (h *Handler) decisionsSummary(w http.ResponseWriter, _ *http.Request) {
	out, err := h.decisionsResponse()
	writeJSON(w, out, err)
}

func (h *Handler) putDecisionMode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	mode, ok := decide.ParseMode(body.Mode)
	if !ok {
		writeClientErr(w, http.StatusBadRequest, "mode must be off, shadow or active")
		return
	}
	if err := decide.SetMode(h.DB, r.PathValue("question"), mode, time.Now()); err != nil {
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := h.decisionsResponse()
	writeJSON(w, out, err)
}

func (h *Handler) postDecisionTruth(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeClientErr(w, http.StatusBadRequest, "invalid decision id")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil || body.Value == "" {
		writeClientErr(w, http.StatusBadRequest, "body must be {\"value\": \"<what actually happened>\"}")
		return
	}
	// Ground truth must be one of the question's own options: a free-text value
	// could never equal an answer, and would count as a disagreement for ever.
	switch err := decide.ValidateTruth(h.DB, id, body.Value); {
	case errors.Is(err, sql.ErrNoRows):
		writeClientErr(w, http.StatusNotFound, "no such decision")
		return
	case err != nil:
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Read the agent's answer BEFORE the truth lands: a truth that contradicts
	// it is an operator correction (internal/corrections), a truth that agrees
	// is a label and writes no ledger row. Same lookup key ValidateTruth used.
	var question, answer string
	switch err := h.DB.QueryRow(`SELECT question_id, COALESCE(answer, '') FROM decisions WHERE id = ?`, id).
		Scan(&question, &answer); {
	case errors.Is(err, sql.ErrNoRows):
		writeClientErr(w, http.StatusNotFound, "no such decision")
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	now := time.Now()
	switch err := decide.RecordGroundTruth(h.DB, id, body.Value, decide.TruthOperator, now); {
	case errors.Is(err, sql.ErrNoRows):
		writeClientErr(w, http.StatusNotFound, "no such decision")
	case err != nil:
		writeErr(w, err)
	default:
		if truth := strings.TrimSpace(body.Value); truth != answer {
			corrections.Record(h.DB, corrections.Correction{
				Source: corrections.SourceTruthDisagree,
				Ref:    "decision:" + strconv.FormatInt(id, 10),
				Before: answer,
				After:  truth,
				// The question is the correction's identity: "task_type: refactor
				// -> bugfix" repeated is one classifier mistake, not a coincidence
				// of two decisions sharing a label.
				Reason: question + ": " + answer + " -> " + truth,
			}, now) // never fails the labelling
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// GET /api/decisions/queue?limit=&since=&project=&rules= — answered decisions
// awaiting the operator's ground truth, newest first (the Decisions page's
// labelling queue, and the Inbox's classifier rows). ?project=<slug|name|id>
// narrows it to decisions about that project's sessions; an unknown project
// yields an empty queue, never the whole fleet's. limit=all returns every open
// decision (the Inbox); a missing or out-of-range number keeps the 100 default.
// Answers the rules gave are left out — nothing in them needs the operator's
// judgement — unless rules=1 asks for them (the Inbox never does). Decisions
// about System-project sessions (the daemon's own utility runs) are left out
// unless system=1 asks for them.
func (h *Handler) decisionsQueue(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if strings.TrimSpace(r.URL.Query().Get("limit")) == "all" {
		limit = -1
	}
	var projectID int64
	if p := strings.TrimSpace(r.URL.Query().Get("project")); p != "" {
		err := h.DB.QueryRow(`SELECT id FROM projects WHERE `+projectMatchExpr("")+` ORDER BY id LIMIT 1`,
			scopeArgs(p)...).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, map[string]any{"items": []decide.QueueItem{}}, nil)
			return
		}
		if err != nil {
			writeErr(w, err)
			return
		}
	}
	items, err := decide.LabelQueue(h.DB, decide.QueueOptions{
		Limit:         limit,
		Since:         strings.TrimSpace(r.URL.Query().Get("since")),
		ProjectID:     projectID,
		IncludeRules:  queryFlag(r, "rules"),
		SystemPath:    ingest.SystemDir(),
		IncludeSystem: queryFlag(r, "system"),
	})
	writeJSON(w, map[string]any{"items": items}, err)
}

// queryFlag reports whether query parameter name is "1" or "true".
func queryFlag(r *http.Request, name string) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get(name))) {
	case "1", "true":
		return true
	}
	return false
}

func (h *Handler) sessionDecisionLabels(w http.ResponseWriter, r *http.Request) {
	l, err := decide.LabelFor(h.DB, r.PathValue("uuid"))
	writeJSON(w, map[string]any{"labels": l}, err)
}
