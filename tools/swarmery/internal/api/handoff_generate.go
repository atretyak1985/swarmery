package api

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/handoff"
)

// handoffRunner generates briefs for POST /api/sessions/{id}/handoff — the
// daemon's handoff runner (the same pinned model as the 30 min tick), attached
// only when handoff generation is on. nil answers 503.
var handoffRunner handoff.Runner

// AttachHandoff records the handoff runner the «continue from handoff» action
// generates with.
func AttachHandoff(r handoff.Runner) { handoffRunner = r }

// handoffGo dispatches a generation; a test seam for deterministic runs.
var handoffGo = func(f func()) { go f() }

// handoffInFlight single-flights generations per session id: a double click must
// not buy two paid runs.
var handoffInFlight sync.Map

// handoffRequestDTO is the POST /api/sessions/{id}/handoff body.
type handoffRequestDTO struct {
	// Status is "ready" (a brief exists — Path is it) or "generating" (a run
	// started, or one was already running; session_updated announces the brief).
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
}

// postSessionHandoff serves POST /api/sessions/{id}/handoff — the «continue
// from handoff» action of a cache-cold session. It never regenerates: when the
// session already has a brief, that brief is the answer (200 ready). Otherwise
// it runs the EXISTING candidate path for that one session (handoff.Eligible:
// the threshold, the System/archived/hidden/pruned exclusions and the regrowth
// cooldown, without the activity window) and, when the session passes, starts
// handoff.Generate in the background (202 generating). A session that is not a
// candidate is 409 — the action never mints briefs the tick would refuse.
func (h *Handler) postSessionHandoff(w http.ResponseWriter, r *http.Request) {
	idArg := r.PathValue("id")
	where := `session_uuid = ?`
	if _, err := strconv.ParseInt(idArg, 10, 64); err == nil {
		where = `id = ?`
	}
	var id int64
	err := h.DB.QueryRow(`SELECT id FROM sessions WHERE `+where, idArg).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		writeClientErr(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}

	var path string
	err = h.DB.QueryRow(`SELECT path FROM handoffs WHERE session_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, id).Scan(&path)
	if err == nil {
		writeJSON(w, handoffRequestDTO{Status: "ready", Path: path}, nil)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeErr(w, err)
		return
	}

	if handoffRunner == nil {
		writeClientErr(w, http.StatusServiceUnavailable, "handoff generation is off (SWARMERY_HANDOFF=off)")
		return
	}
	if _, running := handoffInFlight.Load(id); running {
		writeJSONStatus(w, http.StatusAccepted, handoffRequestDTO{Status: "generating"})
		return
	}
	ok, err := handoff.Eligible(h.DB, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeClientErr(w, http.StatusConflict,
			"not a handoff candidate: under the context threshold, a System or archived session, or no assistant turn yet")
		return
	}
	if _, running := handoffInFlight.LoadOrStore(id, struct{}{}); running {
		writeJSONStatus(w, http.StatusAccepted, handoffRequestDTO{Status: "generating"})
		return
	}
	runner, db := handoffRunner, h.DB
	handoffGo(func() {
		defer handoffInFlight.Delete(id)
		p, err := handoff.Generate(db, runner, id, time.Now())
		if err != nil {
			log.Printf("error: handoff: session %d (on request): %v", id, err)
			return
		}
		log.Printf("handoff: session %d (on request) → %s", id, p)
		publishSessionUpdated(id)
	})
	writeJSONStatus(w, http.StatusAccepted, handoffRequestDTO{Status: "generating"})
}
