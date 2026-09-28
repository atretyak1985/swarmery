package store

// The account_quota and account_limit_hits rows (0089): the per-account quota
// HEADROOM the daemon's poller records, and the durable history of every
// observed usage-limit hit. Package-level functions over *sql.DB, exactly like
// account_runnable.go.
//
// NO ROW HERE EVER CARRIES MESSAGE TEXT OR CREDENTIAL MATERIAL: a quota row is
// numbers, the endpoint's window key/label and a timestamp; a hit row is an
// account key, a timestamp, a fixed-vocabulary scope and two opaque uuids.

import (
	"database/sql"
	"fmt"
	"time"
)

// QuotaRow is one stored quota window for one account.
type QuotaRow struct {
	WindowKey   string    // usage.Window.Key
	Label       string    // usage.Window.Label
	PercentUsed float64   // 0-100
	PercentLeft float64   // 0-100
	ResetsAt    string    // RFC 3339, "" when the window reports none
	WindowMs    int64     // window length, 0 when unknown
	Source      string    // 'poller'
	FetchedAt   time.Time // when the reading was taken (unix-second precision)
}

// LimitHit is one observed usage-limit hit.
type LimitHit struct {
	Account     string // ingest.AccountFor key
	ObservedAt  string // RFC 3339 — the transcript record's timestamp, or the run's end
	Scope       string // 'session' | 'weekly' | 'model' | '' (claudeprobe.LimitScope)
	Source      string // 'transcript' | 'run'
	Engine      string // the engine that ran it, '' when unknown
	SessionUUID string
	RecordUUID  string // the transcript record's uuid; '' for a run verdict
}

// PutAccountQuota replaces account's whole window set with rows, in one
// transaction: latest reading wins, and a window the endpoint stopped reporting
// does not linger as a stale fresh-looking row. Every row is stamped fetchedAt.
// An empty rows slice clears the account — callers that mean "unknown" must not
// call this at all (absence is the unknown state).
func PutAccountQuota(db *sql.DB, account string, rows []QuotaRow, fetchedAt time.Time) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM account_quota WHERE account = ?`, account); err != nil {
		return err
	}
	for _, r := range rows {
		src := r.Source
		if src == "" {
			src = "poller"
		}
		if _, err = tx.Exec(`
			INSERT INTO account_quota
			  (account, window_key, label, percent_used, percent_left, resets_at, window_ms, source, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			account, r.WindowKey, r.Label, r.PercentUsed, r.PercentLeft, r.ResetsAt, r.WindowMs,
			src, fetchedAt.Unix()); err != nil {
			return fmt.Errorf("insert quota window %s: %w", r.WindowKey, err)
		}
	}
	return tx.Commit()
}

// QuotaForAccount returns account's stored windows, ordered by window key. An
// empty result means UNKNOWN (never fetched), not "no headroom".
func QuotaForAccount(db *sql.DB, account string) ([]QuotaRow, error) {
	rows, err := db.Query(`
		SELECT window_key, label, percent_used, percent_left, resets_at, window_ms, source, fetched_at
		  FROM account_quota WHERE account = ? ORDER BY window_key`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuotaRow
	for rows.Next() {
		r, err := scanQuota(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AllAccountQuota returns every stored window keyed by account.
func AllAccountQuota(db *sql.DB) (map[string][]QuotaRow, error) {
	rows, err := db.Query(`
		SELECT account, window_key, label, percent_used, percent_left, resets_at, window_ms, source, fetched_at
		  FROM account_quota ORDER BY account, window_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]QuotaRow{}
	for rows.Next() {
		var account string
		var r QuotaRow
		var fetched int64
		if err := rows.Scan(&account, &r.WindowKey, &r.Label, &r.PercentUsed, &r.PercentLeft,
			&r.ResetsAt, &r.WindowMs, &r.Source, &fetched); err != nil {
			return nil, err
		}
		r.FetchedAt = time.Unix(fetched, 0).UTC()
		out[account] = append(out[account], r)
	}
	return out, rows.Err()
}

func scanQuota(rows *sql.Rows) (QuotaRow, error) {
	var r QuotaRow
	var fetched int64
	if err := rows.Scan(&r.WindowKey, &r.Label, &r.PercentUsed, &r.PercentLeft,
		&r.ResetsAt, &r.WindowMs, &r.Source, &fetched); err != nil {
		return QuotaRow{}, err
	}
	r.FetchedAt = time.Unix(fetched, 0).UTC()
	return r, nil
}

// Execer is what InsertAccountLimitHit writes through: a *sql.DB, or the
// *sql.Tx ingest already holds (the store runs one connection, so ingest must
// write inside its own transaction rather than open a second one).
type Execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// InsertAccountLimitHit appends one hit. A hit whose RecordUUID is already
// stored inserts nothing (the partial UNIQUE index, ON CONFLICT DO NOTHING), so
// re-tailing a transcript can never double-count; a hit with an empty
// RecordUUID (a run verdict) always inserts — its debounce lives with the
// caller. inserted reports whether a row was written.
func InsertAccountLimitHit(db Execer, h LimitHit) (inserted bool, err error) {
	res, err := db.Exec(`
		INSERT INTO account_limit_hits
		  (account, observed_at, scope, source, engine, session_uuid, record_uuid)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(record_uuid) WHERE record_uuid != '' DO NOTHING`,
		h.Account, h.ObservedAt, h.Scope, h.Source, h.Engine, h.SessionUUID, h.RecordUUID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// LimitHitDays returns the distinct calendar days (YYYY-MM-DD, taken from the
// stored RFC 3339 timestamp as written) on which account hit a limit at or
// after since, oldest first.
func LimitHitDays(db *sql.DB, account string, since time.Time) ([]string, error) {
	rows, err := db.Query(`
		SELECT DISTINCT substr(observed_at, 1, 10) AS day
		  FROM account_limit_hits
		 WHERE account = ? AND observed_at >= ?
		 ORDER BY day`, account, since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
