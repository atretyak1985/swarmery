package triage

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// MuteDays is how long a mute on a recurring error group lasts.
const MuteDays = 30

// MuteRow is one friction_mutes row. VerdictID 0 means none (an operator mute).
type MuteRow struct {
	Key, Reason, MutedAt, MutedUntil string
	VerdictID                        int64
}

// ActiveAt reports whether the mute is still in force at now.
func (m MuteRow) ActiveAt(now time.Time) bool { return m.MutedUntil > fmtTS(now) }

// Mute upserts a mute on error group key that lasts MuteDays from now. A
// re-mute replaces the reason and the verdict id and extends muted_until.
func Mute(db *sql.DB, key, reason string, verdictID int64, now time.Time) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("triage: mute key is empty")
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("triage: mute reason is empty")
	}
	var vid any // NULL for an operator mute
	if verdictID != 0 {
		vid = verdictID
	}
	_, err := db.Exec(
		`INSERT INTO friction_mutes(key, reason, verdict_id, muted_at, muted_until) VALUES(?,?,?,?,?)
		 ON CONFLICT(key) DO UPDATE SET reason=excluded.reason, verdict_id=excluded.verdict_id,
		   muted_at=excluded.muted_at, muted_until=excluded.muted_until`,
		key, reason, vid, fmtTS(now), fmtTS(now.AddDate(0, 0, MuteDays)))
	return err
}

func scanMute(sc interface{ Scan(...any) error }) (MuteRow, error) {
	var m MuteRow
	var vid sql.NullInt64
	err := sc.Scan(&m.Key, &m.Reason, &vid, &m.MutedAt, &m.MutedUntil)
	m.VerdictID = vid.Int64
	return m, err
}

// Unmute deletes the mute on key; it returns the row it removed (ok=false when
// there was none).
func Unmute(db *sql.DB, key string) (MuteRow, bool, error) {
	m, err := scanMute(db.QueryRow(
		`DELETE FROM friction_mutes WHERE key=? RETURNING key, reason, verdict_id, muted_at, muted_until`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return MuteRow{}, false, nil
	}
	if err != nil {
		return MuteRow{}, false, err
	}
	return m, true, nil
}

// ActiveMutes returns the mutes whose muted_until is after now, by key. An
// expired row is not active; it is not deleted.
func ActiveMutes(db *sql.DB, now time.Time) (map[string]MuteRow, error) {
	rows, err := db.Query(
		`SELECT key, reason, verdict_id, muted_at, muted_until FROM friction_mutes WHERE muted_until > ?`, fmtTS(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]MuteRow{}
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			return nil, err
		}
		out[m.Key] = m
	}
	return out, rows.Err()
}
