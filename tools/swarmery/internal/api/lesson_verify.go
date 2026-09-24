package api

// Lesson verification and forecast calibration (learning-loop phase 16):
//
//	GET  /api/lessons/retirements?all=1           → {proposals:[Proposal], autoRetireDays}
//	     the open retirement queue (every proposal with all=1)
//	POST /api/lessons/retirements/{id}/confirm    → Proposal  (retires the lesson, retire_reason = reason)
//	POST /api/lessons/retirements/{id}/keep       → Proposal  (keeps it; the reason is held off 30 days)
//	GET  /api/calibration?by=agent,model,effort,project → calibration.Report
//	     groups under calibration.MinSamples non-post-hoc samples are NOT in
//	     the response; only their count is (hiddenGroups / hiddenRuns).
//
// Mutations sit behind requireLocalOrigin. Errors: 404 unknown proposal, 409 a
// proposal that is no longer open (or a lesson no longer active), 400 a bad
// dimension list.

import (
	"net/http"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/calibration"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
)

// lessonVerifyCfg is the verification config the daemon runs with; the API
// uses it to state each open proposal's auto-retire date.
var lessonVerifyCfg = lessons.DefaultVerifyConfig()

// AttachLessonVerify wires the daemon's verification config into the api layer.
func AttachLessonVerify(cfg lessons.VerifyConfig) { lessonVerifyCfg = cfg }

func (h *Handler) listRetirements(w http.ResponseWriter, r *http.Request) {
	ps, err := lessons.ListProposals(h.DB, lessonVerifyCfg, r.URL.Query().Get("all") == "1")
	writeJSON(w, map[string]any{"proposals": ps, "autoRetireDays": lessonVerifyCfg.AutoRetireDays}, err)
}

func (h *Handler) decideRetirement(w http.ResponseWriter, r *http.Request,
	act func(id int64) (lessons.Proposal, error)) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	p, err := act(id)
	switch {
	case err == nil:
		writeJSON(w, p, nil)
	default:
		writeLesson(w, lessons.Lesson{}, err)
	}
}

func (h *Handler) confirmRetirement(w http.ResponseWriter, r *http.Request) {
	h.decideRetirement(w, r, func(id int64) (lessons.Proposal, error) {
		return lessons.ConfirmRetirement(h.DB, lessonVerifyCfg, id, time.Now())
	})
}

func (h *Handler) keepLesson(w http.ResponseWriter, r *http.Request) {
	h.decideRetirement(w, r, func(id int64) (lessons.Proposal, error) {
		return lessons.KeepLesson(h.DB, lessonVerifyCfg, id, time.Now())
	})
}

func (h *Handler) calibration(w http.ResponseWriter, r *http.Request) {
	dims, err := calibration.ParseDims(r.URL.Query().Get("by"))
	if err != nil {
		writeClientErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rep, err := calibration.Compute(h.DB, dims, calibration.MinSamples)
	writeJSON(w, rep, err)
}
