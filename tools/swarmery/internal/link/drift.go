package link

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// KindDrift is the link_events kind of a plan-drift report (mods-swarmery-link
// phase 7): the mod judged the session's work against its open plan phase and
// POSTed /api/link/plan/drift. It is written by that route only — never accepted
// on POST /api/link/events (knownKinds) — and touches no sessions.link_* column.
const KindDrift = "drift"

// ErrUnknownSession is returned by RecordDrift when no sessions row carries the
// uuid: a drift report needs a session to belong to, and minting one from a
// report with no cwd would park it on the '(unknown)' project.
var ErrUnknownSession = errors.New("unknown session")

// RecordDrift stores one drift report as a link_events row (kind drift) of the
// session whose sessions.session_uuid is uuid, at `at` in the link's fixed-width
// ms UTC form, with payload marshalled verbatim. stored=false means a report of
// the same session already sits at that exact millisecond (the link_events
// UNIQUE(session_id, kind, at) dedup key) — not an error.
func RecordDrift(ctx context.Context, db *sql.DB, uuid string, at time.Time, payload any) (stored bool, err error) {
	var sid int64
	err = db.QueryRowContext(ctx, `SELECT id FROM sessions WHERE session_uuid = ?`, uuid).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrUnknownSession
	}
	if err != nil {
		return false, fmt.Errorf("link drift: find session: %w", err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("link drift: payload: %w", err)
	}
	r, err := db.ExecContext(ctx,
		`INSERT OR IGNORE INTO link_events (session_id, kind, at, payload) VALUES (?, ?, ?, ?)`,
		sid, KindDrift, at.UTC().Format(tsFormat), string(raw))
	if err != nil {
		return false, fmt.Errorf("link drift: insert: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("link drift: insert: %w", err)
	}
	return n == 1, nil
}
