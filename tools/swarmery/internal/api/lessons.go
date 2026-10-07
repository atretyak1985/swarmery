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
//
// Edit, dismiss and retire are also operator CORRECTIONS of what the lesson
// generator produced, so each writes one operator_corrections row after its
// own write succeeded (internal/corrections; memory-engineering phase 3). The
// ledger is written here, at the endpoint, and never inside lessons.Retire —
// the verifier's auto-retire calls that too, and a daemon retiring its own
// lesson is not an operator correction.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/corrections"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/lessons"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/repopath"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/worktree"
)

// lessonRef is the ledger's pointer at a lesson ('lesson:12').
func lessonRef(id int64) string { return "lesson:" + strconv.FormatInt(id, 10) }

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
	// The lesson BEFORE the edit is the correction's `before`; Edit returns
	// only the result. A Get failure here is not the operator's problem — Edit
	// reports its own 404/409 and the ledger simply gets no row.
	before, beforeErr := lessons.Get(h.DB, id)
	now := time.Now()
	l, err := lessons.Edit(h.DB, id, body, now)
	if err == nil && beforeErr == nil {
		if c, changed := lessonEditCorrection(before, l); changed {
			corrections.Record(h.DB, c, now) // logged inside; never fails the edit
		}
	}
	writeLesson(w, l, err)
}

// lessonEditCorrection describes an edit as a correction: only the title and
// the guidance count (an area-glob change re-scopes the lesson, it does not
// contradict it), and an edit that changed neither writes no row.
func lessonEditCorrection(before, after lessons.Lesson) (corrections.Correction, bool) {
	var was, is []string
	if before.Title != after.Title {
		was, is = append(was, before.Title), append(is, after.Title)
	}
	if before.Guidance != after.Guidance {
		was, is = append(was, before.Guidance), append(is, after.Guidance)
	}
	if len(is) == 0 {
		return corrections.Correction{}, false
	}
	return corrections.Correction{
		Source: corrections.SourceLessonEdit,
		Ref:    lessonRef(after.ID),
		Before: strings.Join(was, "\n"),
		After:  strings.Join(is, "\n"),
	}, true
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
	now := time.Now()
	l, err := lessons.Dismiss(h.DB, id, body.Reason, now)
	if err == nil {
		corrections.Record(h.DB, corrections.Correction{
			Source: corrections.SourceLessonDismiss, Ref: lessonRef(id),
			Before: l.Title, Reason: body.Reason,
		}, now)
	}
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
	now := time.Now()
	l, err := lessons.Retire(h.DB, id, body.Reason, now)
	if err == nil {
		corrections.Record(h.DB, corrections.Correction{
			Source: corrections.SourceLessonRetire, Ref: lessonRef(id),
			Before: l.Title, Reason: body.Reason,
		}, now)
	}
	writeLesson(w, l, err)
}
