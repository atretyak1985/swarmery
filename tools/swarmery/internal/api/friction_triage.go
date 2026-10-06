package api

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/triage"
)

// Friction triage states, derived per error-group key on every read.
const (
	frictionUntriaged   = "untriaged"
	frictionMuted       = "muted"
	frictionTracked     = "tracked"
	frictionFixProposed = "fix_proposed"
)

// frictionTriageDTO is an error group's triage state. It is derived in
// buildRetroFriction from friction_mutes, recommendations and triage_verdicts
// and stored nowhere else.
type frictionTriageDTO struct {
	State            string `json:"state"` // untriaged | muted | tracked | fix_proposed
	Reason           string `json:"reason,omitempty"`
	MutedUntil       string `json:"mutedUntil,omitempty"`
	RecommendationID int64  `json:"recommendationId,omitempty"`
}

// frictionRec is the newest open error_group recommendation for one key.
type frictionRec struct {
	id     int64
	status string
}

// frictionTriageStates resolves the triage state of every key with three
// queries regardless of len(keys): active mutes, the open error_group
// recommendations targeting the keys, and the advisor fix-card suggestions on
// those recommendations. Precedence: muted > fix_proposed/tracked > untriaged.
func (h *Handler) frictionTriageStates(keys []string, now time.Time) (map[string]frictionTriageDTO, error) {
	out := make(map[string]frictionTriageDTO, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	mutes, err := triage.ActiveMutes(h.DB, now)
	if err != nil {
		return nil, err
	}

	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := h.DB.Query(`
		SELECT id, target, status FROM recommendations
		 WHERE target_kind = 'error_group'
		   AND status IN ('proposed','accepted','adopted')
		   AND target IN (?`+strings.Repeat(",?", len(keys)-1)+`)
		 ORDER BY created_at DESC, id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recs := map[string]frictionRec{}
	for rows.Next() {
		var target string
		var rec frictionRec
		if err := rows.Scan(&rec.id, &target, &rec.status); err != nil {
			return nil, err
		}
		if _, seen := recs[target]; !seen { // newest first: keep the first
			recs[target] = rec
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	fixCard := map[string]bool{} // rec id as text → has a suggested fix-card verdict
	if len(recs) > 0 {
		refs := make([]any, 0, len(recs))
		for _, rec := range recs {
			refs = append(refs, strconv.FormatInt(rec.id, 10))
		}
		vrows, err := h.DB.Query(`
			SELECT DISTINCT ref FROM triage_verdicts
			 WHERE kind = 'advisor' AND state = 'suggested' AND value = 'fix-card'
			   AND ref IN (?`+strings.Repeat(",?", len(refs)-1)+`)`, refs...)
		if err != nil {
			return nil, err
		}
		defer vrows.Close()
		for vrows.Next() {
			var ref string
			if err := vrows.Scan(&ref); err != nil {
				return nil, err
			}
			fixCard[ref] = true
		}
		if err := vrows.Err(); err != nil {
			return nil, err
		}
	}

	for _, k := range keys {
		if m, ok := mutes[k]; ok {
			out[k] = frictionTriageDTO{State: frictionMuted, Reason: m.Reason, MutedUntil: m.MutedUntil}
			continue
		}
		rec, ok := recs[k]
		if !ok {
			out[k] = frictionTriageDTO{State: frictionUntriaged}
			continue
		}
		state := frictionTracked
		if rec.status == "accepted" || rec.status == "adopted" || fixCard[strconv.FormatInt(rec.id, 10)] {
			state = frictionFixProposed
		}
		out[k] = frictionTriageDTO{State: state, RecommendationID: rec.id}
	}
	return out, nil
}

// DELETE /api/retro/friction/mute?key=<key> — the operator lifts a mute. When
// a triage verdict made the mute, that verdict is recorded as undone (the
// action was reverted here, so Source.Undo is not called). A mute that already
// expired is no mute: 404, and no verdict is touched — the group is untriaged
// again and a later run may judge it.
func (h *Handler) unmuteFrictionGroup(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		writeClientErr(w, http.StatusBadRequest, "key is required")
		return
	}
	m, ok, err := triage.Unmute(h.DB, key)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok || !m.ActiveAt(time.Now()) {
		writeClientErr(w, http.StatusNotFound, "no mute for this key")
		return
	}
	if triageSvc != nil {
		vid := m.VerdictID
		if vid == 0 {
			// The friction Source mutes before its verdict row exists, so its
			// mute carries no verdict id: link it by the newest applied noise
			// verdict on the key instead.
			v, found, err := triageSvc.LatestApplied(frictionKind, key, frictionNoise)
			if err != nil {
				log.Printf("friction unmute %q: find applied verdict: %v", key, err)
			} else if found {
				vid = v.ID
			}
		}
		if vid != 0 {
			if _, err := triageSvc.MarkUndone(vid); err != nil &&
				!errors.Is(err, triage.ErrNotUndoable) && !errors.Is(err, triage.ErrNotFound) {
				log.Printf("friction unmute %q: mark verdict %d undone: %v", key, vid, err)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
