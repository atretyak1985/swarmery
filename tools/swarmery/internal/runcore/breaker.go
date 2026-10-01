package runcore

// The account circuit breaker: the FOURTH admission refusal, beside ErrBusy,
// ErrNoSlot and ErrLowQuota.
//
// The quota gate (quota.go) reads a number the poller already has. It cannot
// see an account that simply does not work — an expired login, an organisation
// that switched subscription access off, a usage limit the poller has not read
// yet. Every run admitted onto such an account dies on its first API call, and
// the next one is admitted right behind it. The breaker is the daemon
// remembering that: ONE auth or quota failure opens it for the account, and
// every engine's admission then refuses that account until it can run again.
//
// Two halves:
//
//   - the stored state (account_breaker, migration 0091), opened by a run's
//     classified exit (runtruth), a fresh API-error transcript record (ingest)
//     or a probe, and closed by a reset, a ready probe, a login or the operator;
//   - the PRE-FLIGHT: before the first run after a quiet period a single-flight
//     probe checks the account, so a failure is caught before a volley of runs
//     starts rather than after the first of them has died.
//
// Only `auth` and `quota` ever trip it. An API error never does, a run that
// exited zero never does whatever it printed, and an UNKNOWN probe result
// (timeout, no binary) admits: the breaker must never become "the daemon
// stopped dispatching" because a probe could not answer.

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/systemspawn"
)

// PreflightTTLEnv is how long a stored account verdict vouches for the account:
// an admission whose account was last checked longer ago than this probes it
// first. A Go duration; "0" disables the pre-flight (the stored breaker state is
// still enforced).
const PreflightTTLEnv = "SWARMERY_PREFLIGHT_TTL"

// DefaultPreflightTTL is the TTL when PreflightTTLEnv says nothing.
const DefaultPreflightTTL = 15 * time.Minute

// PreflightPingEnv switches stage two of the probe — the `claude -p` ping — off
// when set to "off" (or 0 / false / no). Stage one (`claude auth status`) still
// runs; it costs no tokens, and it cannot see an account whose login is intact
// but which the API refuses.
const PreflightPingEnv = "SWARMERY_PREFLIGHT_PING"

// preflightSource is the account_runnable source a pre-flight verdict is stored
// under, beside 'probe', 'run' and 'pty-login'.
const preflightSource = "preflight"

// preflightTimeout bounds BOTH probe stages of one pre-flight. An admission
// waits at most this long, once per account per TTL, and a probe that runs into
// it is unknown — which admits.
const preflightTimeout = 90 * time.Second

// EventAccountBreaker is the run_events kind dispatch records when a Todo card
// is held back by an open breaker — the twin of EventQuotaWait.
const EventAccountBreaker = "account_breaker"

// ErrAccountBreaker is the admission refusal of an account whose breaker is
// open. Like ErrLowQuota it leaves no state behind: nothing is stamped, no slot
// is taken. Returned as an *AccountBreakerError, which errors.Is-matches it.
var ErrAccountBreaker = errors.New("account breaker open")

// AccountBreakerError is ErrAccountBreaker with the evidence: which account,
// why (kind and the fixed reason phrase), since when, and — for a quota opening
// — when it closes on its own.
type AccountBreakerError struct {
	Account  string
	Kind     string // store.BreakerKindAuth | store.BreakerKindQuota
	Reason   string // a claudeprobe.Reason* phrase, never CLI output
	OpenedAt string // RFC 3339
	ResetsAt string // RFC 3339, "" for an auth opening
}

func (e *AccountBreakerError) Error() string {
	var b strings.Builder
	b.WriteString("account " + e.Account + " is paused (" + e.Kind + ")")
	if e.Reason != "" {
		b.WriteString(": " + e.Reason)
	}
	if e.OpenedAt != "" {
		b.WriteString(", since " + e.OpenedAt)
	}
	if e.ResetsAt != "" {
		b.WriteString(", resets " + e.ResetsAt)
	}
	return b.String()
}

func (e *AccountBreakerError) Is(target error) bool { return target == ErrAccountBreaker }

// AccountCheckFunc is the admission check's shape — the seam each engine holds
// so its tests can refuse or admit without seeding account_breaker.
type AccountCheckFunc func(ctx context.Context, db *sql.DB, res claudeacct.Resolution, now time.Time) error

// AccountProbeFunc checks whether the CLI can actually run under env — the
// COMPLETE environment of the run being vouched for, built the way a real spawn
// builds it (RunEnv / AccountEnv). It is an environment and not a config dir on
// purpose: a run's account is whatever its environment selects, and that is not
// always what its account KEY suggests (an unbound project keeps the config dir
// the daemon inherited). ProbeAccount is production; tests substitute a stub.
type AccountProbeFunc func(ctx context.Context, env []string) claudeprobe.Result

// RunEnv is the environment a run under res is spawned with — the SAME
// composition ClaudeRunner.Start uses (claudeacct.SpawnEnvResolved), so the
// pre-flight probes exactly what it admits:
//
//   - an UNBOUND project: os.Environ() untouched. A CLAUDE_CONFIG_DIR the daemon
//     inherited (`swarmery install --claude-config-dir`) stays, because the run
//     keeps it too;
//   - a project bound to the default account: that variable removed;
//   - a named account: its config dir, its secret store, the estate's store.
func RunEnv(res claudeacct.Resolution) []string {
	return claudeacct.SpawnEnvResolved(os.Environ(), res)
}

// AccountEnv is RunEnv for a caller that holds only a breaker's account KEY —
// "Probe & resume" — and no project.
//
// The default key is the key of every UNBOUND project's runs (QuotaAccountKey),
// so it gets the unbound environment: os.Environ() untouched, an inherited
// CLAUDE_CONFIG_DIR kept. Stripping it here would re-probe ~/.claude while the
// runs the breaker stopped use another directory, and the resume would answer
// about the wrong account for ever. A named key gets the account layer
// (claudeacct.SpawnEnv): its config dir and its secret store.
func AccountEnv(key string) []string {
	if key = strings.TrimSpace(key); key == "" || key == ingest.DefaultAccount {
		return RunEnv(claudeacct.Resolution{})
	}
	return claudeacct.SpawnEnv(os.Environ(), key)
}

// preflight is the package's pre-flight state: the probe, the per-account
// single-flight, and the memo of accounts whose last probe could not answer.
//
// The probe is NIL until cmd/swarmery installs one (SetPreflightProbe), and a
// nil probe means "no pre-flight": the stored breaker state is still enforced,
// but nothing is spawned. That default is deliberate — every engine's Service is
// gated by CheckAccount even when built as a bare struct literal, so a probe
// that was on by default would run the real CLI, against the operator's real
// accounts, from every test that starts a run.
var preflight = struct {
	mu        sync.Mutex
	probe     AccountProbeFunc
	flights   map[string]*preflightFlight
	unknownAt map[string]time.Time // account key → when its probe last answered unknown
	// onWait, when set, is called by a caller that found a flight already in
	// progress, just before it blocks on it. nil in production; the single-flight
	// test uses it to know every waiter has joined before it lets the probe answer.
	onWait func(key string)
}{flights: map[string]*preflightFlight{}, unknownAt: map[string]time.Time{}}

// preflightFlight is one in-flight pre-flight; waiters share its one result.
type preflightFlight struct {
	done chan struct{}
	err  error
}

// SetPreflightProbe installs the pre-flight probe and returns the previous one.
// The daemon calls it once at startup with ProbeAccount; nil switches the
// pre-flight off.
func SetPreflightProbe(p AccountProbeFunc) AccountProbeFunc {
	preflight.mu.Lock()
	defer preflight.mu.Unlock()
	prev := preflight.probe
	preflight.probe = p
	return prev
}

// ProbeAccount is the production two-stage probe. BOTH stages run under env —
// the environment of the run being vouched for — and nothing else:
//
//  1. claudeprobe.ProbeEnv — `claude auth status`: sub-second, no tokens. It
//     separates a logged-in config dir from one with no login, and that is all
//     it sees.
//  2. claudeprobe.ProbeRun — the minimal `claude -p` ping, only when stage one
//     answered ready: the one check that sees an account the API refuses. It
//     starts in the System project's directory (systemspawn.Dir), the cwd
//     convention of the daemon's other utility runs, so its transcript lands
//     there — but it takes ONLY the directory from the System project: not its
//     account environment and not its composed `--settings`, which belong to a
//     different project than the run in question. PreflightPingEnv=off skips
//     this stage.
func ProbeAccount(ctx context.Context, env []string) claudeprobe.Result {
	r := claudeprobe.ProbeEnv(ctx, env)
	if r.Status != claudeprobe.StatusReady || !preflightPingEnabled() {
		return r
	}
	dir, _ := systemspawn.Dir() // "" when there is no System project: cwd left alone
	return claudeprobe.ProbeRun(ctx, env, dir)
}

// safeProbe runs probe and turns a panic inside it into an UNKNOWN result: a
// probe that blew up has answered nothing, and unknown admits. The gate sits on
// every engine's admission path, so a bug in a probe must cost one log line,
// not the scheduler.
func safeProbe(ctx context.Context, probe AccountProbeFunc, env []string, account string) (r claudeprobe.Result) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("error: runcore: account probe for account=%s panicked: %v", account, p)
			r = claudeprobe.Result{Status: claudeprobe.StatusUnknown, Reason: claudeprobe.ReasonStartFailed}
		}
	}()
	return probe(ctx, env)
}

// preflightPingEnabled reads PreflightPingEnv. Anything but an explicit "off"
// spelling leaves the ping on.
func preflightPingEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(PreflightPingEnv))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

var (
	ttlWarnMu   sync.Mutex
	ttlWarnedAt string // the last invalid raw value warned about
)

// PreflightTTLFromEnv reads PreflightTTLEnv. Unset or blank → DefaultPreflightTTL;
// "0" (or any duration <= 0) → 0, the pre-flight is off. A value that does not
// parse falls back to the default with ONE log line per distinct bad value —
// the gate runs once per candidate per scheduling pass.
func PreflightTTLFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(PreflightTTLEnv))
	if raw == "" {
		return DefaultPreflightTTL
	}
	if raw == "0" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		ttlWarnMu.Lock()
		if ttlWarnedAt != raw {
			ttlWarnedAt = raw
			log.Printf("warning: runcore: ignoring invalid %s=%q — using %s",
				PreflightTTLEnv, raw, DefaultPreflightTTL)
		}
		ttlWarnMu.Unlock()
		return DefaultPreflightTTL
	}
	if d <= 0 {
		return 0
	}
	return d
}

// CheckAccount is the admission gate: an *AccountBreakerError when the run's
// account cannot run, nil otherwise. Called right after the quota gate, before
// any slot or worktree is taken.
//
//  1. An OPEN breaker refuses — except a quota opening whose reset time has
//     passed, which is closed ('reset') and falls through to step 2.
//  2. Pre-flight: when the account's stored verdict (account_runnable) is older
//     than the TTL, the account is probed first — through a per-account
//     single-flight, so five cards admitted in one scheduling pass make ONE
//     probe and share its result.
//  3. A negative probe (no login, access refused, a usage limit) opens the
//     breaker and refuses. A ready one is stored and admits.
//
// It FAILS OPEN. A nil db, a failed read, a database that predates the breaker
// migration, no probe installed and an unknown probe result (timeout, no
// binary, an API error) all admit.
func CheckAccount(ctx context.Context, db *sql.DB, res claudeacct.Resolution, now time.Time) error {
	if db == nil {
		return nil
	}
	key := QuotaAccountKey(res)
	if refusal := breakerRefusal(db, key, now); refusal != nil {
		return refusal
	}
	ttl := PreflightTTLFromEnv()
	if !preflightDue(db, key, now, ttl) {
		return nil
	}
	return preflightDo(key, func(probe AccountProbeFunc) error {
		// Re-checked inside the flight: a caller that lost the race to a flight
		// which has just finished must see what that flight stored, not probe again.
		if refusal := breakerRefusal(db, key, now); refusal != nil {
			return refusal
		}
		if !preflightDue(db, key, now, ttl) {
			return nil
		}
		// The probe's context is deliberately cut loose from the caller's
		// cancellation: waiters share this one result, so a caller that gives up
		// must not abort the probe the others are blocked on.
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), preflightTimeout)
		defer cancel()
		// Probed under RunEnv(res): the environment the run being admitted gets,
		// composed by the same function its spawn uses. Anything else vouches for
		// a different account than the one the run executes under.
		return settlePreflight(db, key, safeProbe(pctx, probe, RunEnv(res), key), now, ttl)
	})
}

// CheckAccountWith runs check, or CheckAccount when check is nil — so a Service
// built as a struct literal (not through its NewService) is still gated.
func CheckAccountWith(ctx context.Context, check AccountCheckFunc, db *sql.DB, res claudeacct.Resolution, now time.Time) error {
	if check == nil {
		check = CheckAccount
	}
	return check(ctx, db, res, now)
}

// breakerRefusal is step 1: the stored state. nil admits.
func breakerRefusal(db *sql.DB, key string, now time.Time) error {
	b, ok, err := store.GetAccountBreaker(db, key)
	if err != nil || !ok || !b.IsOpen() {
		// A failed read is UNKNOWN, and unknown admits — including a database that
		// predates migration 0091 (detected from the query's own error).
		return nil
	}
	if b.Kind == store.BreakerKindQuota && b.ResetsAt != "" {
		if at, perr := time.Parse(time.RFC3339, b.ResetsAt); perr == nil && !at.After(now) {
			if _, cerr := CloseBreaker(db, key, store.BreakerClosedByReset, now); cerr != nil {
				log.Printf("warning: runcore: close breaker account=%s after its reset: %v", key, cerr)
			}
			return nil
		}
	}
	return refusalOf(b)
}

func refusalOf(b store.AccountBreaker) *AccountBreakerError {
	return &AccountBreakerError{
		Account: b.Account, Kind: b.Kind, Reason: b.Reason, OpenedAt: b.OpenedAt, ResetsAt: b.ResetsAt,
	}
}

// preflightDue reports whether the account must be probed before this
// admission: the pre-flight is on, a probe is installed, the stored verdict is
// older than ttl (or absent), and the last probe did not just answer unknown.
func preflightDue(db *sql.DB, key string, now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	preflight.mu.Lock()
	probe := preflight.probe
	unknownAt, wasUnknown := preflight.unknownAt[key]
	preflight.mu.Unlock()
	if probe == nil {
		return false
	}
	// An unknown answer is remembered for one TTL: a machine with no CLI, or an
	// API that is down, must not cost every admission a probe timeout.
	if wasUnknown && now.Sub(unknownAt) < ttl {
		return false
	}
	verdict, ok, err := store.GetAccountRunnable(db, key)
	if err != nil {
		return false // unknown admits, and a store that cannot be read cannot hold the result either
	}
	return !ok || now.Sub(verdict.CheckedAt) >= ttl
}

// preflightDo runs fn once per account at a time; latecomers block on the
// leader's result and return it. fn is handed the installed probe.
//
// The flight is torn down in a DEFER: whatever happens inside fn — a panic
// included — the map entry is removed and the waiters are released. A flight
// that ended without a result carries a nil error, so its waiters are admitted
// (the gate fails open); the panic itself still propagates to the leader's
// caller. A probe's own panic never gets that far: safeProbe turns it into an
// unknown result.
func preflightDo(key string, fn func(AccountProbeFunc) error) error {
	preflight.mu.Lock()
	if f, ok := preflight.flights[key]; ok {
		onWait := preflight.onWait
		preflight.mu.Unlock()
		if onWait != nil {
			onWait(key)
		}
		<-f.done
		return f.err
	}
	f := &preflightFlight{done: make(chan struct{})}
	preflight.flights[key] = f
	probe := preflight.probe
	preflight.mu.Unlock()

	defer func() {
		preflight.mu.Lock()
		delete(preflight.flights, key)
		preflight.mu.Unlock()
		close(f.done)
	}()
	if probe != nil {
		f.err = fn(probe)
	}
	return f.err
}

// settlePreflight stores one probe result and turns it into the admission
// answer.
func settlePreflight(db *sql.DB, key string, r claudeprobe.Result, now time.Time, ttl time.Duration) error {
	switch r.Status {
	case claudeprobe.StatusReady:
		forgetUnknown(key)
		if err := store.PutAccountRunnable(db, key, string(r.Status), "", preflightSource, now); err != nil {
			log.Printf("warning: runcore: store pre-flight verdict account=%s: %v", key, err)
		}
		return nil
	case claudeprobe.StatusNoLogin:
		forgetUnknown(key)
		if err := store.PutAccountRunnable(db, key, string(r.Status), r.Reason, preflightSource, now); err != nil {
			log.Printf("warning: runcore: store pre-flight verdict account=%s: %v", key, err)
		}
		return openFromProbe(db, key, store.BreakerKindAuth, r.Reason, now)
	case claudeprobe.StatusLimited:
		// A limit says nothing about the login, so account_runnable is left alone
		// (runtruth's rule); the breaker carries it, with a reset time.
		forgetUnknown(key)
		return openFromProbe(db, key, store.BreakerKindQuota, r.Reason, now)
	}
	// Unknown: fail open, and say so once per account per TTL.
	preflight.mu.Lock()
	preflight.unknownAt[key] = now
	preflight.mu.Unlock()
	log.Printf("warning: runcore: pre-flight for account=%s could not answer (%s) — admitting; next check in %s",
		key, r.Reason, ttl)
	return nil
}

func forgetUnknown(key string) {
	preflight.mu.Lock()
	delete(preflight.unknownAt, key)
	preflight.mu.Unlock()
}

// openFromProbe opens the breaker on a negative pre-flight and returns the
// refusal. A breaker that could not be STORED still refuses this admission: the
// probe's answer is a fact whether or not the row was written.
func openFromProbe(db *sql.DB, key, kind, reason string, now time.Time) error {
	if err := OpenBreaker(db, key, kind, reason, store.BreakerSourceProbe, now); err != nil {
		log.Printf("warning: runcore: open breaker account=%s kind=%s: %v", key, kind, err)
	}
	if b, ok, err := store.GetAccountBreaker(db, key); err == nil && ok && b.IsOpen() {
		return refusalOf(b)
	}
	return &AccountBreakerError{Account: key, Kind: kind, Reason: reason, OpenedAt: now.UTC().Format(time.RFC3339)}
}

// OpenBreaker opens account's breaker and surfaces it as an alert: a
// config_lint_findings row under store.AccountBreakerRule, which GET /api/alerts
// lists and the Inbox renders.
//
// kind must be auth or quota — anything else (an API error) is refused and
// nothing is written. A quota opening takes its reset time from the account's
// freshest quota poll, else now + 1h (store.QuotaResetHint). The callers here —
// a run's classified exit, a probe — carry a verdict and not the limit line, so
// the limit's scope is unknown and the EARLIEST still-ahead reset is used: a
// breaker that closes too soon costs one probe, one that closes too late pauses
// a working account. (The transcript detector knows the scope and passes it to
// the store itself.) Opening an already-open breaker writes nothing, except
// that an auth failure escalates a quota opening (store.OpenAccountBreaker).
// Only the account key and fixed tags are ever logged.
func OpenBreaker(db *sql.DB, account, kind, reason, source string, now time.Time) error {
	if db == nil {
		return nil
	}
	changed, err := store.TripAccountBreaker(db, account, kind, reason, source, "", now)
	if err != nil || !changed {
		return err
	}
	log.Printf("runcore: account breaker OPEN account=%s kind=%s source=%s", account, kind, source)
	return findings.Upsert(db, store.AccountBreakerTarget(account), store.AccountBreakerRule,
		"error", store.AccountBreakerMessage(kind))
}

// CloseBreaker closes account's open breaker (closedBy is a
// store.BreakerClosedBy* tag) and resolves its alert. closed=false when nothing
// was open.
func CloseBreaker(db *sql.DB, account, closedBy string, now time.Time) (closed bool, err error) {
	if db == nil {
		return false, nil
	}
	closed, err = store.CloseAccountBreaker(db, account, closedBy, now)
	if err != nil {
		return false, err
	}
	if closed {
		log.Printf("runcore: account breaker CLOSED account=%s by=%s", account, closedBy)
	}
	// Resolved even when nothing was open: a stray active finding must not
	// outlive the breaker it describes.
	return closed, findings.Resolve(db, store.AccountBreakerTarget(account), store.AccountBreakerRule)
}

// ResumeBreaker is the operator's "Probe & resume": it probes account with probe
// (both stages in production — ProbeAccount) and closes the breaker when, and
// only when, the account can run again.
//
//	ready    → the verdict is stored and the breaker closes ('probe')
//	no-login → the verdict is stored and the breaker stays (or becomes) an auth one
//	limited  → an auth opening is replaced by a quota one, which closes on its
//	           own at the reset; a quota opening is left as it is
//	unknown  → nothing changes: a probe that could not answer proves nothing
//
// The probe runs under AccountEnv(account) — the environment a run filed under
// that breaker key gets, so the resume answers about the account the stopped
// runs actually use (for the default key: the unbound environment, an inherited
// CLAUDE_CONFIG_DIR kept). The probe's result is returned so the caller can say
// why the account is still paused; a probe that panics is unknown.
func ResumeBreaker(ctx context.Context, db *sql.DB, account string, probe AccountProbeFunc, now time.Time) (claudeprobe.Result, error) {
	if probe == nil {
		probe = ProbeAccount
	}
	r := safeProbe(ctx, probe, AccountEnv(account), account)
	switch r.Status {
	case claudeprobe.StatusReady:
		forgetUnknown(account)
		if err := store.PutAccountRunnable(db, account, string(r.Status), "", store.BreakerSourceProbe, now); err != nil {
			return r, err
		}
		_, err := CloseBreaker(db, account, store.BreakerClosedByProbe, now)
		return r, err
	case claudeprobe.StatusNoLogin:
		if err := store.PutAccountRunnable(db, account, string(r.Status), r.Reason, store.BreakerSourceProbe, now); err != nil {
			return r, err
		}
		return r, OpenBreaker(db, account, store.BreakerKindAuth, r.Reason, store.BreakerSourceProbe, now)
	case claudeprobe.StatusLimited:
		// The login works again, so an auth opening no longer describes the
		// account; what is left is a limit, and that closes on its own.
		if b, ok, err := store.GetAccountBreaker(db, account); err != nil {
			return r, err
		} else if ok && b.IsOpen() && b.Kind == store.BreakerKindAuth {
			if _, err := CloseBreaker(db, account, store.BreakerClosedByProbe, now); err != nil {
				return r, err
			}
		}
		return r, OpenBreaker(db, account, store.BreakerKindQuota, r.Reason, store.BreakerSourceProbe, now)
	}
	return r, nil
}

// RecordBreakerWait records one EventAccountBreaker for a subject held back by
// an open breaker, at most once per OPENING: a card re-evaluated every
// scheduling pass would otherwise write a row per pass, while a later opening
// (a new opened_at), an escalation or a moved reset time is news and records
// again — all of those change the refusal's text, which is the dedupe key. A
// refusal that is not an *AccountBreakerError (a bare sentinel) has no opening
// to key on and dedupes on its text per clock hour instead.
//
// The event is stamped now (RFC 3339, UTC). Reports whether a row was written.
// Best-effort, like RecordRunEvent.
func RecordBreakerWait(db *sql.DB, engine string, subjectID int64, refusal error, now time.Time) bool {
	if db == nil || refusal == nil {
		return false
	}
	detail := refusal.Error()
	var open *AccountBreakerError
	perOpening := errors.As(refusal, &open) && open.OpenedAt != ""
	hour := now.UTC().Truncate(time.Hour)
	for _, e := range RunEvents(db, engine, subjectID) {
		if e.Kind != EventAccountBreaker || e.Detail != detail {
			continue
		}
		if perOpening {
			return false
		}
		if at, err := time.Parse(time.RFC3339, e.CreatedAt); err == nil && at.UTC().Truncate(time.Hour).Equal(hour) {
			return false
		}
	}
	RecordRunEvent(db, engine, subjectID, "", EventAccountBreaker, 0, detail, now.UTC().Format(time.RFC3339))
	return true
}
