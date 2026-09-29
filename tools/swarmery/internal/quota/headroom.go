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

// MaxAge is how old a stored quota reading may be and still vouch for an
// account: three EFFECTIVE poll intervals of the configured
// SWARMERY_QUOTA_INTERVAL value, so one missed tick does not turn a healthy
// account into "unknown" and a slow cadence does not turn every reading stale.
// polling=false when the value disables the poller; the bound then falls back
// to three default intervals (no new reading will arrive to refresh it).
//
// It lives here, beside Headroom, because every reader needs the same bound:
// `swarmery account switch` (acctops.HeadroomMaxAge) and the run admission gate
// (runcore.CheckQuota) must agree on what "fresh" means.
func MaxAge(intervalValue string) (maxAge time.Duration, polling bool) {
	d, on, _ := ParseInterval(intervalValue) // an invalid value is the daemon's default, as it runs
	if !on {
		return 3 * DefaultInterval, false
	}
	return 3 * d, true
}

// Headroom reads account's headroom. ok=false means UNKNOWN — and "no answer"
// is kept distinct from "a bad answer" on purpose, because that is the whole
// reason `switch --force` exists. Unknown when: db is nil; the read fails for
// any reason, including a database that predates the account_quota migration
// (detected from the query's own error, never by pre-querying sqlite_master);
// the account has no rows; or every row is older than maxAge (maxAge <= 0
// disables the staleness bound).
func Headroom(db *sql.DB, account string, now time.Time, maxAge time.Duration) (H, bool) {
	return headroom(db, account, now, maxAge, false)
}

// LiveHeadroom is Headroom that also treats a window whose reset time has
// already passed as UNKNOWN: its percent_left describes a window that no longer
// exists, however recently it was read. A reset time that does not parse is
// kept (no evidence it passed). The run admission gate reads this; `account
// switch` keeps Headroom's semantics unchanged.
func LiveHeadroom(db *sql.DB, account string, now time.Time, maxAge time.Duration) (H, bool) {
	return headroom(db, account, now, maxAge, true)
}

func headroom(db *sql.DB, account string, now time.Time, maxAge time.Duration, skipReset bool) (H, bool) {
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
		if skipReset && r.ResetsAt != "" {
			if at, err := time.Parse(time.RFC3339, r.ResetsAt); err == nil && !at.After(now) {
				continue
			}
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
