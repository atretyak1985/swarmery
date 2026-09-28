package store

import "database/sql"

// SetSessionAccount re-points one session's account column. It is the ONLY
// writer permitted to overwrite a NON-EMPTY sessions.account: ingest stamps the
// column first-writer-wins (it sets account only while the column is still
// empty, internal/ingest) and must stay unable to revise it, so a
// transcript present under two config dirs keeps the root that first knew it.
// An explicit cross-account move (`swarmery account move-session`) is the one
// act that knows better, and it goes through here. changed reports whether a
// row matched.
func SetSessionAccount(db *sql.DB, sessionUUID, account string) (changed bool, err error) {
	res, err := db.Exec(`UPDATE sessions SET account = ? WHERE session_uuid = ?`, account, sessionUUID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
