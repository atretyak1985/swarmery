package quota

import (
	"database/sql"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// H is one account's current headroom: the TIGHTEST fresh window.
type H struct {
	PercentLeft float64 // the minimum percent_left across the fresh windows
	Window      string  // window key of that minimum
	Label       string  // its label
	ResetsAt    string  // its reset time, RFC 3339 or ""
	FetchedAt   time.Time
}

// Headroom reads account's headroom. ok=false means UNKNOWN — and "no answer"
// is kept distinct from "a bad answer" on purpose, because that is the whole
// reason `switch --force` exists. Unknown when: db is nil; the read fails for
// any reason, including a database that predates the account_quota migration
// (detected from the query's own error, never by pre-querying sqlite_master);
// the account has no rows; or every row is older than maxAge (maxAge <= 0
// disables the staleness bound).
func Headroom(db *sql.DB, account string, now time.Time, maxAge time.Duration) (H, bool) {
	if db == nil {
		return H{}, false
	}
	rows, err := store.QuotaForAccount(db, account)
	if err != nil {
		return H{}, false
	}
	var best H
	found := false
	for _, r := range rows {
		if maxAge > 0 && now.Sub(r.FetchedAt) > maxAge {
			continue
		}
		if !found || r.PercentLeft < best.PercentLeft {
			best = H{PercentLeft: r.PercentLeft, Window: r.WindowKey, Label: r.Label,
				ResetsAt: r.ResetsAt, FetchedAt: r.FetchedAt}
			found = true
		}
	}
	return best, found
}

// LastReading reports when account was last read, however old: the newest
// fetched_at among its rows. ok=false means there is no reading at all (no
// rows, a nil db, or a failed read) — which is what lets a caller tell "stale"
// from "never read".
func LastReading(db *sql.DB, account string) (time.Time, bool) {
	if db == nil {
		return time.Time{}, false
	}
	rows, err := store.QuotaForAccount(db, account)
	if err != nil || len(rows) == 0 {
		return time.Time{}, false
	}
	last := rows[0].FetchedAt
	for _, r := range rows[1:] {
		if r.FetchedAt.After(last) {
			last = r.FetchedAt
		}
	}
	return last, true
}
