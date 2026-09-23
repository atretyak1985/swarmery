package api

// Lesson review queue (learning-loop phase 14, internal/lessons):
//
//	GET   /api/lessons?status=candidate|active|retired|dismissed|merged → {lessons:[Lesson]}
//	      &area=<repo-relative dir or file> keeps only lessons whose area globs
//	      overlap it (the injection matcher, phase 15; read by core's area-lessons skill)
//	POST  /api/lessons/{id}/promote   {area?} → Lesson   (active only; writes the
//	      lesson into <repo>/<area>/CLAUDE.md on a NEW branch, phase 15.4)
//	PATCH /api/lessons/{id}           {title?, guidance?, areaGlobs?} → Lesson
//	POST  /api/lessons/{id}/accept    → Lesson   (candidate → active; the ONLY path to active)
//	POST  /api/lessons/{id}/merge     {lessonId? | normTitle?} → Lesson
//	POST  /api/lessons/{id}/dismiss   {reason?} → Lesson
//	POST  /api/lessons/{id}/retire    {reason?} → Lesson
//
// Every mutation is an operator action behind requireLocalOrigin. Errors:
// 404 unknown lesson, 409 wrong state, 400 a body that breaks the contract.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

var lessonStatuses = map[string]bool{
	"": true, lessons.StatusCandidate: true, lessons.StatusActive: true, lessons.StatusRetired: true,
	lessons.StatusDismissed: true, lessons.StatusMerged: true,
}

func (h *Handler) listLessons(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if !lessonStatuses[status] {
		writeClientErr(w, http.StatusBadRequest, "status must be candidate, active, retired, dismissed or merged")
		return
	}
	ls, err := lessons.List(h.DB, status)
	if area := r.URL.Query().Get("area"); err == nil && area != "" {
		ls = filterLessonsByArea(ls, area)
	}
	writeJSON(w, map[string]any{"lessons": ls}, err)
}

// filterLessonsByArea keeps the lessons a run working in area would be handed
// (lessons.Matches — the same rule injection applies).
func filterLessonsByArea(ls []lessons.Lesson, area string) []lessons.Lesson {
	out := []lessons.Lesson{}
	sc := lessons.Scope{Areas: []string{area}}
	for _, l := range ls {
		if lessons.Matches(l.AreaGlobs, sc) {
			out = append(out, l)
		}
	}
	return out
}

// promoteLesson graduates an active lesson into the consumer repo's nested
// CLAUDE.md, on a new branch for the operator to review (internal/lessons).
func (h *Handler) promoteLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	var body lessons.PromoteInput
	if !decodeLessonBody(w, r, &body) {
		return
	}
	l, err := lessons.Promote(h.DB, worktree.ExecGit{}, repopath.Resolve, id, body, time.Now())
	writeLesson(w, l, err)
}

func lessonID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeClientErr(w, http.StatusBadRequest, "invalid lesson id")
		return 0, false
	}
	return id, true
}

// decodeLessonBody decodes an optional JSON body; an empty body is allowed.
func decodeLessonBody(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(v)
	if err != nil && !errors.Is(err, io.EOF) {
		writeClientErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeLesson(w http.ResponseWriter, l lessons.Lesson, err error) {
	switch {
	case errors.Is(err, lessons.ErrNotFound):
		writeClientErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, lessons.ErrState):
		writeClientErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, lessons.ErrInvalid):
		writeClientErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, l, err)
	}
}

func (h *Handler) acceptLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	l, err := lessons.Accept(h.DB, id, time.Now())
	writeLesson(w, l, err)
}

func (h *Handler) editLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	var body lessons.EditInput
	if !decodeLessonBody(w, r, &body) {
		return
	}
	l, err := lessons.Edit(h.DB, id, body, time.Now())
	writeLesson(w, l, err)
}

func (h *Handler) mergeLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	var body lessons.MergeTarget
	if !decodeLessonBody(w, r, &body) {
		return
	}
	l, err := lessons.Merge(h.DB, id, body, time.Now())
	writeLesson(w, l, err)
}

type lessonReason struct {
	Reason string `json:"reason"`
}

func (h *Handler) dismissLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	var body lessonReason
	if !decodeLessonBody(w, r, &body) {
		return
	}
	l, err := lessons.Dismiss(h.DB, id, body.Reason, time.Now())
	writeLesson(w, l, err)
}

func (h *Handler) retireLesson(w http.ResponseWriter, r *http.Request) {
	id, ok := lessonID(w, r)
	if !ok {
		return
	}
	var body lessonReason
	if !decodeLessonBody(w, r, &body) {
		return
	}
	l, err := lessons.Retire(h.DB, id, body.Reason, time.Now())
	writeLesson(w, l, err)
}
