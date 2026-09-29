package store

import "database/sql"

// SetSessionLaunchAccount records what session uuid was LAUNCHED as (the
// session_launch_account migration). An empty key is a no-op: '' is "unknown"
// and must never overwrite a recorded value. Reports whether a row was updated.
func SetSessionLaunchAccount(db *sql.DB, uuid, key string) (bool, error) {
	if uuid == "" || key == "" {
		return false, nil
	}
	res, err := db.Exec(`UPDATE sessions SET launch_account = ? WHERE session_uuid = ?`, key, uuid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

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
