package cachecold

import (
	"database/sql"
	"time"
)

// SyncResult is what one tick changed in session_cache_cold, as session ids so
// the caller can publish session_updated for exactly those rows.
type SyncResult struct {
	// Opened are sessions newly flagged (or re-flagged with a new cold spell).
	Opened []int64
	// Cleared are sessions whose open flag was closed: a new turn arrived, or
	// the session ended.
	Cleared []int64
}

// Sync is the daemon tick: it upserts a session_cache_cold row for every
// Candidates entry and clears every open row that no longer qualifies. A tick
// that changes nothing writes nothing — an unchanged candidate matches its row
// and the conditional upsert affects no row — so Opened/Cleared carry only real
// changes.
func Sync(db *sql.DB, now time.Time, cfg Config) (SyncResult, error) {
	var res SyncResult
	cands, err := Candidates(db, now, cfg)
	if err != nil {
		return res, err
	}
	want := make(map[string]bool, len(cands))
	for _, c := range cands {
		want[c.SessionUUID] = true
	}

	// Collect the open rows before writing: the store runs on one connection, so
	// an UPDATE under an open cursor would block.
	type open struct {
		uuid string
		id   sql.NullInt64
	}
	rows, err := db.Query(`
		SELECT scc.session_uuid, s.id
		FROM session_cache_cold scc
		LEFT JOIN sessions s ON s.session_uuid = scc.session_uuid
		WHERE scc.cleared_at IS NULL`)
	if err != nil {
		return res, err
	}
	var stale []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.uuid, &o.id); err != nil {
			rows.Close()
			return res, err
		}
		if !want[o.uuid] {
			stale = append(stale, o)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	for _, c := range cands {
		r, err := tx.Exec(`
			INSERT INTO session_cache_cold (session_uuid, since, kind, footprint, cleared_at)
			VALUES (?, ?, ?, ?, NULL)
			ON CONFLICT(session_uuid) DO UPDATE SET
			    since = excluded.since, kind = excluded.kind,
			    footprint = excluded.footprint, cleared_at = NULL
			WHERE session_cache_cold.cleared_at IS NOT NULL
			   OR session_cache_cold.since <> excluded.since
			   OR session_cache_cold.kind <> excluded.kind
			   OR session_cache_cold.footprint <> excluded.footprint`,
			c.SessionUUID, formatTS(c.Since), string(c.Kind), c.Footprint)
		if err != nil {
			return res, err
		}
		if n, _ := r.RowsAffected(); n > 0 {
			res.Opened = append(res.Opened, c.SessionID)
		}
	}
	stamp := formatTS(now)
	for _, o := range stale {
		if _, err := tx.Exec(
			`UPDATE session_cache_cold SET cleared_at = ? WHERE session_uuid = ? AND cleared_at IS NULL`,
			stamp, o.uuid); err != nil {
			return res, err
		}
		if o.id.Valid {
			res.Cleared = append(res.Cleared, o.id.Int64)
		}
	}
	if err := tx.Commit(); err != nil {
		return SyncResult{}, err
	}
	return res, nil
}
