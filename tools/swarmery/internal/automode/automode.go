// Package automode makes an outage of Claude Code's server-side auto mode
// classifier visible.
//
// In auto mode every tool call is cleared by a permission check that runs on
// Claude's servers. When that check fails to answer, the call is refused with
// "The server-side auto mode classifier gave no verdict (…)", and after ten
// such answers in a row the turn stops by itself. The cause is outside this
// daemon and is not fixed here: the package only counts the refusals the
// transcript ingest already stored, and raises ONE finding per burst so the
// Inbox and /api/health say so while it is happening. Nothing here changes how
// a run behaves.
package automode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
)

const (
	// Rule is the config_lint_findings rule of the outage alert.
	Rule = "auto_mode_no_verdict"
	// Target is the finding's target. There is one classifier, so one target:
	// (Target, Rule) is the key that folds a whole burst into one row.
	Target = "auto-mode-classifier"
	// severity of the finding: the outage pauses the sessions it hits, and they
	// continue once the check answers again.
	severity = "warn"

	// AlertWindow is how far back Evaluate counts to decide a burst is under way.
	AlertWindow = 10 * time.Minute
	// QuietWindow is how long the check must answer every time before the alert
	// resolves.
	QuietWindow = 30 * time.Minute
	// HealthWindow is the window /api/health reports.
	HealthWindow = time.Hour

	// DefaultAlertMin is the number of refusals inside AlertWindow that raises
	// the alert. One or two are the transient failure the message itself
	// describes; three in ten minutes is a burst.
	DefaultAlertMin = 3
	// EnvAlertMin overrides DefaultAlertMin (a whole number ≥ 1).
	EnvAlertMin = "SWARMERY_AUTOMODE_ALERT_MIN"

	// DefaultInterval is the evaluation period.
	DefaultInterval = 60 * time.Second
)

// tsLayout is how events.ts is stored: UTC with exactly three fractional digits
// ("2026-09-28T06:38:21.896Z"). The window bound is compared as TEXT, so it has
// to be written the same way — a whole-second RFC 3339 bound ("…21Z") sorts
// AFTER every row of that same second ("." < "Z") and would drop them.
const tsLayout = "2006-01-02T15:04:05.000Z"

// Counts is what one window holds.
type Counts struct {
	// Events is the number of tool calls refused for want of a verdict.
	Events int
	// Sessions is how many distinct sessions those calls belong to.
	Sessions int
	// LastAt is the newest such call's timestamp as stored; "" when Events is 0.
	LastAt string
}

// Count counts the no-verdict refusals at or after since.
//
// The rows are already ingested — events of type 'tool_call' with status
// 'error', the refusal text inside payload — so this is one query and no parser
// change. It rides idx_events_type (type, ts): the lower bound is what keeps the
// payload scan small, so callers always pass a recent since.
func Count(db *sql.DB, since time.Time) (Counts, error) {
	var (
		c    Counts
		last sql.NullString
	)
	err := db.QueryRow(`
		SELECT count(*), count(DISTINCT session_id), max(ts)
		  FROM events
		 WHERE type = 'tool_call' AND ts >= ? AND status = 'error'
		   AND payload LIKE '%' || ? || '%'`,
		since.UTC().Format(tsLayout), claudeprobe.AutoModeNoVerdictMarker,
	).Scan(&c.Events, &c.Sessions, &last)
	if err != nil {
		return Counts{}, err
	}
	c.LastAt = last.String
	return c, nil
}

// Alerting reports whether the outage alert is open right now.
func Alerting(db *sql.DB) (bool, error) {
	var one int
	err := db.QueryRow(
		`SELECT 1 FROM config_lint_findings WHERE target = ? AND rule = ? AND resolved_at IS NULL LIMIT 1`,
		Target, Rule).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// AlertMin is the burst threshold: EnvAlertMin when it holds a whole number ≥ 1,
// DefaultAlertMin otherwise.
func AlertMin() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvAlertMin))); err == nil && n >= 1 {
		return n
	}
	return DefaultAlertMin
}

// Message is the alert's sentence. Its shape is fixed; only the two numbers
// move.
func Message(c Counts) string {
	return fmt.Sprintf(
		"%d permission %s got no verdict in the last 10 minutes across %d %s — "+
			"Claude Code's server-side classifier is failing; affected sessions pause until it recovers.",
		c.Events, plural(c.Events, "check", "checks"), c.Sessions, plural(c.Sessions, "session", "sessions"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// Evaluate raises, keeps or resolves the outage alert as of now.
//
//   - AlertMin or more refusals in the last AlertWindow → the finding is upserted.
//     Upsert is keyed on (Target, Rule), so a burst is one row — and one Inbox
//     item — however many evaluations see it.
//   - none at all in the last QuietWindow → the finding is resolved.
//   - in between (a burst tailing off) → left as it is.
func Evaluate(db *sql.DB, now time.Time) error {
	recent, err := Count(db, now.Add(-AlertWindow))
	if err != nil {
		return err
	}
	if recent.Events >= AlertMin() {
		return findings.Upsert(db, Target, Rule, severity, Message(recent))
	}
	quiet, err := Count(db, now.Add(-QuietWindow))
	if err != nil {
		return err
	}
	if quiet.Events == 0 {
		return findings.Resolve(db, Target, Rule)
	}
	return nil
}

// Ticker evaluates the alert periodically.
type Ticker struct {
	DB       *sql.DB
	Interval time.Duration
	// Now is the clock; nil means time.Now. A seam for tests.
	Now func() time.Time
}

// Run evaluates once immediately, then on every tick, until ctx is cancelled.
func (t *Ticker) Run(ctx context.Context) {
	iv := t.Interval
	if iv <= 0 {
		iv = DefaultInterval
	}
	t.Once()
	tick := time.NewTicker(iv)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.Once()
		}
	}
}

// Once runs a single evaluation. A failure is logged and the alert is left as it
// was: a query that could not run says nothing about the classifier.
func (t *Ticker) Once() {
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	if err := Evaluate(t.DB, now()); err != nil {
		log.Printf("automode: evaluate: %v", err)
	}
}
