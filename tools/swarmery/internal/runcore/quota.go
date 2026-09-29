package runcore

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/quota"
)

// QuotaFloorEnv is the admission floor: a headless run is refused while its
// account's FRESH quota headroom (percent left in the tightest window) is below
// this many percent. 0 disables the gate.
const QuotaFloorEnv = "SWARMERY_QUOTA_FLOOR"

// DefaultQuotaFloor is the floor when QuotaFloorEnv says nothing (operator's
// choice, 2026-09-29).
const DefaultQuotaFloor = 10.0

// QuotaIntervalEnv is the poller's cadence knob (cmd/swarmery reads the same
// name). The gate reads it too so its staleness bound follows the cadence the
// poller actually runs at — quota.MaxAge is the one shared bound.
const QuotaIntervalEnv = "SWARMERY_QUOTA_INTERVAL"

// EventQuotaWait is the run_events kind dispatch records when a Todo card is
// held back by the quota gate — the card's "why am I still waiting" line.
const EventQuotaWait = "quota_wait"

// ErrLowQuota is the THIRD admission refusal, beside ErrBusy and ErrNoSlot.
// Like ErrNoSlot it is transient and leaves no state behind: nothing is
// stamped, no slot is taken, and the same request succeeds once the window
// resets. Returned as a *LowQuotaError, which errors.Is-matches this sentinel.
var ErrLowQuota = errors.New("account quota below floor")

// LowQuotaError is ErrLowQuota with the evidence: which account, which window,
// how much is left against which floor, and when it resets — the facts that
// turn "refused" into "retry after <reset>, or switch the account".
type LowQuotaError struct {
	Account     string
	Window      string
	Label       string
	ResetsAt    string // RFC 3339, "" when the window reports none
	PercentLeft float64
	Floor       float64
}

func (e *LowQuotaError) Error() string {
	return fmt.Sprintf("%s%s%% left (floor %s%%)%s",
		e.identity(), fmtPct(e.PercentLeft), fmtPct(e.Floor), e.resetsSuffix())
}

// identity is the head of Error() — "account <a>: <window label> at " — which
// names the account and the window. The label falls back to the window key; the
// poller derives both from the same usage window, so within one account a label
// names exactly one window. The trailing " at " keeps "work" from matching
// "work2". RecordQuotaWait keys its dedupe on this and on resetsSuffix, so the
// dedupe and the text can never disagree.
func (e *LowQuotaError) identity() string {
	label := e.Label
	if label == "" {
		label = e.Window
	}
	return "account " + e.Account + ": " + label + " at "
}

// resetsSuffix is the tail of Error().
func (e *LowQuotaError) resetsSuffix() string {
	r := e.ResetsAt
	if r == "" {
		r = "unknown"
	}
	return ", resets " + r
}

func (e *LowQuotaError) Is(target error) bool { return target == ErrLowQuota }

// fmtPct renders a percentage to one decimal, trimming a trailing ".0" — so a
// 9.6% reading never prints as "10% left (floor 10%)".
func fmtPct(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

var (
	floorWarnMu   sync.Mutex
	floorWarnedAt string // the last invalid raw value warned about
)

// QuotaFloorFromEnv reads QuotaFloorEnv. Unset or blank → DefaultQuotaFloor;
// "0" (or any value <= 0 that parses) → 0, the gate is off. An unusable value
// (not a number, NaN/Inf, above 100) falls back to the default with ONE log
// line per distinct bad value — the gate runs once per candidate per
// scheduling pass, and a typo must not flood the log.
func QuotaFloorFromEnv() float64 {
	raw := strings.TrimSpace(os.Getenv(QuotaFloorEnv))
	if raw == "" {
		return DefaultQuotaFloor
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f > 100 {
		floorWarnMu.Lock()
		if floorWarnedAt != raw {
			floorWarnedAt = raw
			log.Printf("warning: runcore: ignoring invalid %s=%q — using %s%%",
				QuotaFloorEnv, raw, fmtPct(DefaultQuotaFloor))
		}
		floorWarnMu.Unlock()
		return DefaultQuotaFloor
	}
	if f <= 0 {
		return 0
	}
	return f
}

// QuotaAccountKey maps a Resolution to the key the quota poller stores rows
// under. The poller enumerates claudeacct.DiscoverWithDefault, whose default
// profile carries ingest.DefaultAccount ("default"); an unbound project ("")
// and an explicit default both run on that profile.
func QuotaAccountKey(res claudeacct.Resolution) string {
	if res.Account == "" || res.DefaultProfile {
		return ingest.DefaultAccount
	}
	return res.Account
}

// QuotaCheckFunc is the admission check's shape — the seam each engine holds
// so its tests can refuse or admit without seeding account_quota.
type QuotaCheckFunc func(db *sql.DB, res claudeacct.Resolution, now time.Time) error

// CheckQuota is the admission gate: a *LowQuotaError when the run's account
// has a FRESH reading below the floor, nil otherwise.
//
// It FAILS OPEN. No rows, a stale reading (older than quota.MaxAge of the
// configured poll interval), a window whose reset time has already passed (its
// percent_left describes a window that no longer exists), a failed read or a
// nil db are all UNKNOWN, and unknown admits: the gate must never become "the
// daemon stopped dispatching" because a poller tick was missed. It reads the
// database only — never the usage endpoint — so admission stays as fast as the
// slot check beside it.
func CheckQuota(db *sql.DB, res claudeacct.Resolution, now time.Time) error {
	floor := QuotaFloorFromEnv()
	if floor <= 0 {
		return nil
	}
	key := QuotaAccountKey(res)
	maxAge, _ := quota.MaxAge(os.Getenv(QuotaIntervalEnv))
	h, ok := quota.LiveHeadroom(db, key, now, maxAge)
	if !ok || h.PercentLeft >= floor {
		return nil
	}
	return &LowQuotaError{
		Account: key, Window: h.Window, Label: h.Label, ResetsAt: h.ResetsAt,
		PercentLeft: h.PercentLeft, Floor: floor,
	}
}

// CheckQuotaWith runs check, or CheckQuota when check is nil — so a Service
// built as a struct literal (not through its NewService) is still gated.
func CheckQuotaWith(check QuotaCheckFunc, db *sql.DB, res claudeacct.Resolution, now time.Time) error {
	if check == nil {
		check = CheckQuota
	}
	return check(db, res, now)
}

// RecordQuotaWait records one EventQuotaWait for a subject held back by the
// gate, at most once per (account, window, reset): a card re-evaluated every
// scheduling pass would otherwise write a row per pass, while a different
// account, a different window or a new reset time is news and records again.
//
// When the window reports NO reset time there is nothing to key the window on,
// and "once forever" would hide every later wait; such refusals dedupe per
// (account, window) per clock hour instead. A refusal that is not a
// *LowQuotaError (a bare sentinel) dedupes on its exact text, per hour.
//
// The event is stamped now (RFC 3339, UTC). Reports whether a row was written.
// Best-effort, like RecordRunEvent.
func RecordQuotaWait(db *sql.DB, engine string, subjectID int64, refusal error, now time.Time) bool {
	if db == nil || refusal == nil {
		return false
	}
	detail := refusal.Error()
	identity, resetsAt := detail, "" // bare sentinel: its whole text, no reset
	var low *LowQuotaError
	if errors.As(refusal, &low) {
		identity, resetsAt = low.identity(), low.ResetsAt
	}
	hour := now.UTC().Truncate(time.Hour)
	for _, e := range RunEvents(db, engine, subjectID) {
		if e.Kind != EventQuotaWait || !strings.HasPrefix(e.Detail, identity) {
			continue
		}
		if resetsAt != "" {
			if strings.HasSuffix(e.Detail, ", resets "+resetsAt) {
				return false // same account, window and reset
			}
			continue
		}
		if at, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil && at.UTC().Truncate(time.Hour).Equal(hour) {
			return false // no reset to key on: same account and window this hour
		}
	}
	RecordRunEvent(db, engine, subjectID, "", EventQuotaWait, 0, detail, now.UTC().Format(time.RFC3339))
	return true
}
