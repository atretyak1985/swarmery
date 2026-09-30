package store

// The account_breaker rows (0090): the per-account circuit breaker every
// engine's admission reads before it spends a run. Package-level functions,
// exactly like account_runnable.go and account_quota.go — but over a Querier
// rather than a *sql.DB, because ingest opens the breaker from INSIDE its tail
// transaction (the store runs one connection; a second handle would deadlock).
//
// NO ROW HERE EVER CARRIES MESSAGE TEXT OR CREDENTIAL MATERIAL: kind, source
// and closed_by are fixed tags, reason is a fixed phrase from
// internal/claudeprobe, and the rest are an account key and timestamps.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Querier is what the breaker helpers read and write through: a *sql.DB, or the
// *sql.Tx a caller already holds.
type Querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Breaker vocabulary — the closed sets migration 0090 documents.
const (
	BreakerOpen   = "open"
	BreakerClosed = "closed"

	// The only two failures that trip a breaker. They are claudeprobe's
	// FailureAuth / FailureQuota values, spelled here so the store stays a leaf.
	BreakerKindAuth  = "auth"
	BreakerKindQuota = "quota"

	BreakerSourceRun        = "run"
	BreakerSourceProbe      = "probe"
	BreakerSourceTranscript = "transcript"

	BreakerClosedByReset    = "reset"
	BreakerClosedByProbe    = "probe"
	BreakerClosedByLogin    = "login"
	BreakerClosedByOperator = "operator"
)

// AccountBreakerRule is the config_lint_findings rule an open breaker is
// surfaced under (one active row per account, target AccountBreakerTarget).
const AccountBreakerRule = "account_breaker_open"

// breakerTargetPrefix is the finding target's namespace.
const breakerTargetPrefix = "account:"

// quotaResetFallback is how long a quota breaker stays open when nothing says
// when the window resets.
const quotaResetFallback = time.Hour

// AccountBreakerTarget is the finding target of account's breaker.
func AccountBreakerTarget(account string) string { return breakerTargetPrefix + account }

// AccountFromBreakerTarget is AccountBreakerTarget's inverse.
func AccountFromBreakerTarget(target string) (account string, ok bool) {
	account, ok = strings.CutPrefix(target, breakerTargetPrefix)
	return account, ok && account != ""
}

// AccountBreakerMessage is the FIXED sentence an open breaker's finding carries.
// It depends on the kind only — never on anything the CLI printed.
func AccountBreakerMessage(kind string) string {
	if kind == BreakerKindQuota {
		return "This account hit a Claude usage limit. Runs on it are paused until the limit resets."
	}
	return "Claude refused this account. Runs on it are paused until a probe succeeds."
}

// AccountBreaker is one account's breaker row.
type AccountBreaker struct {
	Account  string
	State    string // BreakerOpen | BreakerClosed
	Kind     string // BreakerKindAuth | BreakerKindQuota
	Reason   string // a claudeprobe.Reason* phrase
	OpenedAt string // RFC 3339
	ResetsAt string // RFC 3339, "" when nothing says when
	Source   string // BreakerSource*
	ClosedAt string // RFC 3339, "" while open
	ClosedBy string // BreakerClosedBy*, "" while open
}

// IsOpen reports whether the breaker currently refuses runs.
func (b AccountBreaker) IsOpen() bool { return b.State == BreakerOpen }

// ErrBreakerKind is returned for an opening whose kind is not one that may trip
// a breaker.
var ErrBreakerKind = errors.New("account breaker: kind must be auth or quota")

// OpenAccountBreaker opens b.Account's breaker with b's kind, reason, opened_at,
// resets_at and source. changed reports whether a row was written:
//
//   - no row, or a closed one → a NEW opening (opened_at is b's);
//   - already open as quota and b is auth → escalated in place: auth needs a
//     probe to close, and a limit's reset time must not close it (opened_at is
//     kept — it is still the same opening);
//   - already open otherwise → nothing is written, so a burst of failing runs
//     or a re-tailed transcript cannot move opened_at or the reset time.
func OpenAccountBreaker(q Querier, b AccountBreaker) (changed bool, err error) {
	if b.Kind != BreakerKindAuth && b.Kind != BreakerKindQuota {
		return false, fmt.Errorf("%w: %q", ErrBreakerKind, b.Kind)
	}
	if b.Account == "" {
		return false, errors.New("account breaker: empty account")
	}
	cur, ok, err := GetAccountBreaker(q, b.Account)
	if err != nil {
		return false, err
	}
	if ok && cur.IsOpen() {
		if cur.Kind != BreakerKindQuota || b.Kind != BreakerKindAuth {
			return false, nil
		}
		_, err = q.Exec(`
			UPDATE account_breaker
			   SET kind = ?, reason = ?, source = ?, resets_at = NULL
			 WHERE account = ? AND state = 'open'`,
			b.Kind, b.Reason, b.Source, b.Account)
		return err == nil, err
	}
	_, err = q.Exec(`
		INSERT INTO account_breaker
		  (account, state, kind, reason, opened_at, resets_at, source, closed_at, closed_by)
		VALUES (?, 'open', ?, ?, ?, ?, ?, NULL, NULL)
		ON CONFLICT(account) DO UPDATE SET
		  state = 'open',
		  kind = excluded.kind,
		  reason = excluded.reason,
		  opened_at = excluded.opened_at,
		  resets_at = excluded.resets_at,
		  source = excluded.source,
		  closed_at = NULL,
		  closed_by = NULL`,
		b.Account, b.Kind, b.Reason, b.OpenedAt, nullIfEmpty(b.ResetsAt), b.Source)
	return err == nil, err
}

// TripAccountBreaker is OpenAccountBreaker for a failure observed at now: it
// stamps opened_at, and gives a QUOTA opening its reset time (QuotaResetHint).
// It is the one place a trip's row is put together, so the run path, the probe
// and the transcript detector cannot open breakers of different shapes. The
// caller surfaces the alert when changed is true.
//
// scope is the limit scope of a quota trip when the caller knows it
// ("session" | "weekly" | "model" — claudeprobe.LimitScope of the limit line),
// "" when it does not; it only chooses which window's reset time is used.
func TripAccountBreaker(q Querier, account, kind, reason, source, scope string, now time.Time) (changed bool, err error) {
	b := AccountBreaker{
		Account: account, Kind: kind, Reason: reason, Source: source,
		OpenedAt: now.UTC().Format(time.RFC3339),
	}
	if kind == BreakerKindQuota {
		b.ResetsAt = QuotaResetHint(q, account, scope, now)
	}
	return OpenAccountBreaker(q, b)
}

// CloseAccountBreaker closes account's open breaker, recording who closed it.
// closed=false when there was nothing open to close.
func CloseAccountBreaker(q Querier, account, closedBy string, at time.Time) (closed bool, err error) {
	res, err := q.Exec(`
		UPDATE account_breaker
		   SET state = 'closed', closed_at = ?, closed_by = ?
		 WHERE account = ? AND state = 'open'`,
		at.UTC().Format(time.RFC3339), closedBy, account)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// GetAccountBreaker reads account's row. ok=false means the breaker never
// opened — which readers must treat as closed.
func GetAccountBreaker(q Querier, account string) (AccountBreaker, bool, error) {
	b, err := scanBreaker(q.QueryRow(breakerSelect+` WHERE account = ?`, account))
	if errors.Is(err, sql.ErrNoRows) {
		return AccountBreaker{}, false, nil
	}
	if err != nil {
		return AccountBreaker{}, false, err
	}
	return b, true, nil
}

// ListAccountBreakers returns every breaker row ordered by account; openOnly
// keeps the open ones.
func ListAccountBreakers(q Querier, openOnly bool) ([]AccountBreaker, error) {
	query := breakerSelect
	if openOnly {
		query += ` WHERE state = 'open'`
	}
	rows, err := q.Query(query + ` ORDER BY account`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountBreaker
	for rows.Next() {
		b, err := scanBreaker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// scopeWindows maps a limit scope (claudeprobe.LimitScope's vocabulary, the one
// account_limit_hits stores) to the account_quota window keys that scope runs
// out of. A scope absent from this map — "" included — names no window.
var scopeWindows = map[string][]string{
	"session": {"five_hour", "session"},
	"weekly":  {"seven_day"},
	"model":   {"seven_day_opus", "seven_day_sonnet"},
}

// QuotaResetHint is the resets_at a QUOTA opening at now gets, read from the
// account's FRESHEST account_quota poll:
//
//  1. scope known (the limit line said which limit was hit): the earliest
//     still-ahead reset among that scope's windows;
//  2. otherwise, or when that scope's window is not stored: the EARLIEST
//     still-ahead reset of any window. Earliest on purpose — a guess that is too
//     short costs one probe when the breaker closes and the account is still
//     limited; a guess that is too long (a session limit read as the weekly
//     reset) pauses a healthy account for days;
//  3. no stored window says (never read, a failed read, every reset already
//     passed): now + 1h.
//
// Always RFC 3339 and always after now, so a quota breaker can never be left
// without a time to close at.
func QuotaResetHint(q Querier, account, scope string, now time.Time) string {
	fallback := now.Add(quotaResetFallback).UTC().Format(time.RFC3339)
	rows, err := q.Query(`
		SELECT window_key, resets_at
		  FROM account_quota
		 WHERE account = ?
		   AND fetched_at = (SELECT MAX(fetched_at) FROM account_quota WHERE account = ?)`,
		account, account)
	if err != nil {
		return fallback
	}
	defer rows.Close()
	var scoped, earliest time.Time
	for rows.Next() {
		var key, resetsAt string
		if err := rows.Scan(&key, &resetsAt); err != nil {
			return fallback
		}
		at, err := time.Parse(time.RFC3339, resetsAt)
		if err != nil || !at.After(now) {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
		if inScope(scope, key) && (scoped.IsZero() || at.Before(scoped)) {
			scoped = at
		}
	}
	if rows.Err() != nil {
		return fallback
	}
	switch {
	case !scoped.IsZero():
		return scoped.UTC().Format(time.RFC3339)
	case !earliest.IsZero():
		return earliest.UTC().Format(time.RFC3339)
	}
	return fallback
}

// inScope reports whether window key belongs to limit scope.
func inScope(scope, key string) bool {
	for _, k := range scopeWindows[scope] {
		if k == key {
			return true
		}
	}
	return false
}

const breakerSelect = `
	SELECT account, state, kind, reason, opened_at, COALESCE(resets_at, ''),
	       source, COALESCE(closed_at, ''), COALESCE(closed_by, '')
	  FROM account_breaker`

// rowScanner is the Scan half shared by *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBreaker(r rowScanner) (AccountBreaker, error) {
	var b AccountBreaker
	err := r.Scan(&b.Account, &b.State, &b.Kind, &b.Reason, &b.OpenedAt, &b.ResetsAt,
		&b.Source, &b.ClosedAt, &b.ClosedBy)
	return b, err
}

// nullIfEmpty stores "" as NULL — resets_at's "nothing says when".
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
