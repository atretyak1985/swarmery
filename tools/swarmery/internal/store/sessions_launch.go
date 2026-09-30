package store

import "database/sql"

// DriftRow is a session whose transcript landed under a different account than
// the one it was launched as.
type DriftRow struct {
	SessionUUID   string
	Account       string // where the transcript landed (sessions.account)
	LaunchAccount string // what the run was launched as
	StartedAt     string
}

// AccountDrift returns, newest first, up to limit sessions whose account and
// launch_account are both known and differ. limit <= 0 means 100.
//
// sessions.launch_account is written by the SessionStart hook handler
// (api/prockill.go) and by ingest from a parked hook; nothing reads this query
// in production yet — it is the one a drift view or doctor finding builds on.
func AccountDrift(db *sql.DB, limit int) ([]DriftRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.Query(`SELECT session_uuid, account, launch_account, COALESCE(started_at, '')
		FROM sessions
		WHERE launch_account <> '' AND account <> '' AND launch_account <> account
		ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DriftRow{}
	for rows.Next() {
		var r DriftRow
		if err := rows.Scan(&r.SessionUUID, &r.Account, &r.LaunchAccount, &r.StartedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
