package ingest

// The usage-limit detector: the PRIMARY recorder of account_limit_hits.
//
// Every daemon-spawned run writes a transcript that ingest tails, and so does
// every terminal session — which is where the limit hits actually happen — so
// reading them here needs no per-engine spawn hook. The gate is the record's
// isApiErrorMessage flag, never the text alone: plan documents and design
// discussions quote the marker strings verbatim, and a text-only matcher would
// invent limit hits out of prose.
//
// No row carries the message text: only the account, the record's timestamp,
// the fixed-vocabulary scope and the two uuids.

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeprobe"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/findings"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// recordLimitHit inserts one account_limit_hits row when r is a flagged API
// error record whose text carries a recorded usage-limit shape. A no-op for any
// other record, for a record with no uuid, and for a transcript read with no
// projects-root context (its
// account is unknown; a later tail that knows the root records it, the
// record_uuid unique index keeping that to one row).
//
// The same flagged record is also what opens the account's circuit breaker
// (tripBreaker) — under its own, stricter rule: the text must BE an auth or
// quota failure line and the record must be fresh.
func (in *ingester) recordLimitHit(r *record) error {
	// No uuid, no row: the record_uuid unique index is what makes a re-tail
	// idempotent, and it does not cover the empty string.
	if !r.IsAPIErrorMessage || r.UUID == "" {
		return nil
	}
	account := AccountFor(in.originRoot)
	if account == "" {
		return nil
	}
	text := apiErrorText(r)
	if err := in.tripBreaker(account, r, text); err != nil {
		return err
	}
	scope, ok := claudeprobe.LimitScope(text)
	if !ok {
		return nil
	}
	_, err := store.InsertAccountLimitHit(in.tx, store.LimitHit{
		Account:     account,
		ObservedAt:  r.Timestamp,
		Scope:       scope,
		Source:      "transcript",
		SessionUUID: in.sessionUUID,
		RecordUUID:  r.UUID,
	})
	return err
}

// breakerRecency bounds how old (or how far ahead) a flagged record's timestamp
// may be and still open the breaker. A breaker is about NOW: a backfill or a
// re-tail walks months of transcripts, and last week's expired login must not
// pause an account that has been working since.
const breakerRecency = 10 * time.Minute

// breakerClock is tripBreaker's clock — a package var so a test can place "now"
// beside a fixture's timestamp.
var breakerClock = time.Now

// breakerProbe is the confirming probe tripBreaker consults before opening an
// AUTH breaker from a transcript — claudeprobe.Probe in production. A
// transcript is not necessarily a run the daemon spawned (a leaked test
// process, a terminal session with a broken env), so an auth failure line
// alone is not trusted; a quota failure is — LimitScope already reads the
// line's own wording, nothing to confirm.
var breakerProbe = claudeprobe.Probe

// pendingAuthTrip is an auth-kind breaker opening deferred until a confirming
// probe runs, after the tail's write transaction has committed.
type pendingAuthTrip struct {
	keys       []string
	reason     string
	configDir  string
	observedAt time.Time
}

// probeConfigDir derives the CLAUDE_CONFIG_DIR a transcript's account was
// discovered under, mirroring AccountFor's own path math: originRoot is
// "<configDir>/projects", so configDir is its parent. "" (the default
// account's own config dir) when originRoot carries no root context.
func probeConfigDir(originRoot string) string {
	root := strings.TrimSpace(originRoot)
	if root == "" {
		return ""
	}
	return filepath.Dir(filepath.Clean(root))
}

// tripBreaker opens account's circuit breaker when r — already known to be a
// flagged API-error record — IS an auth or quota failure line
// (claudeprobe.AccountFailure; an `API Error:` line never trips) and its
// timestamp is within breakerRecency of now. The opening and its alert are
// written through in.tx, like everything else this tail writes.
//
// No row carries the message text: the breaker stores the kind and a fixed
// reason phrase, the finding a fixed sentence per kind.
func (in *ingester) tripBreaker(account string, r *record, text string) error {
	kind, reason, ok := claudeprobe.AccountFailure(text)
	if !ok {
		return nil
	}
	at, err := time.Parse(time.RFC3339, r.Timestamp)
	if err != nil {
		return nil // no usable timestamp, no evidence the failure is current
	}
	now := breakerClock()
	if age := now.Sub(at); age > breakerRecency || age < -breakerRecency {
		return nil
	}
	// The limit line says WHICH limit was hit (session, weekly, a model's); that
	// picks the window whose reset time the breaker waits for. "" for an auth
	// trip and for a limit whose wording names no scope.
	scope, _ := claudeprobe.LimitScope(text)
	// The dir's own key, and the default key when this is the dir unbound runs
	// execute under (UnboundAccount): they are paused under that key, and they
	// would die on the same failure.
	keys := []string{account}
	if account != DefaultAccount && account == UnboundAccount() {
		keys = append(keys, DefaultAccount)
	}
	if kind == claudeprobe.FailureAuth {
		// An auth line alone is not trusted — it may come from a transcript the
		// daemon never spawned (a leaked test process, a terminal session with a
		// broken env). Defer the opening until a confirming probe, run AFTER this
		// tail's write transaction commits, also says the account isn't runnable.
		in.pendingAuthTrips = append(in.pendingAuthTrips, pendingAuthTrip{
			keys: keys, reason: reason, configDir: probeConfigDir(in.originRoot),
			observedAt: now,
		})
		return nil
	}
	for _, key := range keys {
		changed, err := store.TripAccountBreaker(in.tx, key, kind, reason, store.BreakerSourceTranscript, scope, now)
		if err != nil {
			return err
		}
		if !changed {
			continue
		}
		log.Printf("ingest: account breaker OPEN account=%s kind=%s source=transcript", key, kind)
		if err := findings.Upsert(in.tx, store.AccountBreakerTarget(key), store.AccountBreakerRule,
			"error", store.AccountBreakerMessage(kind)); err != nil {
			return err
		}
	}
	return nil
}

// confirmPendingAuthTrips runs breakerProbe against each deferred auth trip's
// config dir, AFTER the tail's write transaction has committed — see
// pendingAuthTrip's doc for why it can't run inside that transaction. Only a
// confirmed StatusNoLogin opens the breaker; StatusReady and StatusUnknown
// both fail open (an inconclusive probe must never pause a working account),
// matching runcore's own rule for an inconclusive pre-flight probe.
func confirmPendingAuthTrips(db *sql.DB, trips []pendingAuthTrip) error {
	for _, trip := range trips {
		result := breakerProbe(context.Background(), trip.configDir)
		if result.Status != claudeprobe.StatusNoLogin {
			log.Printf("ingest: deferred auth trip NOT confirmed account=%v status=%s reason=%s",
				trip.keys, result.Status, result.Reason)
			continue
		}
		for _, key := range trip.keys {
			changed, err := store.TripAccountBreaker(db, key, claudeprobe.FailureAuth, trip.reason,
				store.BreakerSourceTranscript, "", trip.observedAt)
			if err != nil {
				return err
			}
			if !changed {
				continue
			}
			log.Printf("ingest: account breaker OPEN account=%s kind=%s source=transcript (confirmed)",
				key, claudeprobe.FailureAuth)
			if err := findings.Upsert(db, store.AccountBreakerTarget(key), store.AccountBreakerRule,
				"error", store.AccountBreakerMessage(claudeprobe.FailureAuth)); err != nil {
				return err
			}
		}
	}
	return nil
}

// apiErrorText is the prose of an assistant record: its text blocks joined, or
// a plain-string content — the same shapes processRecords reads.
func apiErrorText(r *record) string {
	var m apiMessage
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return ""
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			texts = append(texts, b.Text)
		}
	}
	return strings.Join(texts, "\n")
}
