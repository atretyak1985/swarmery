// Package cachecold is the prompt-cache side of the fat-session cost driver.
//
// A prompt cache lives for its TTL — 1 h for the cache Claude Code writes on a
// subscription (migration 0075), 5 min on an API key. A fat session left idle
// past it pays for its WHOLE context again on the next message: the request
// reads nothing from cache and re-writes everything at the cache-write rate.
// This package records that twice:
//
//   - the PREDICTION (predict.go, sync.go): on the daemon tick, a live session
//     whose footprint is at the handoff threshold and whose last turn ended
//     longer ago than its TTL is flagged «cache cold» — the operator can still
//     continue from the handoff brief instead of paying for the re-write;
//   - the FACT (this file): ingest marks a turn cold_restart when, after such a
//     pause, it re-wrote ~the whole previous context and read ~nothing of it
//     from cache (IsColdRestart).
//
// The fact never requires the prediction: a session the tick never saw (the
// daemon was down, the footprint was under the threshold) is still marked.
//
// The package is a leaf on purpose — ingest calls it inside the tail
// transaction, and claudeacct/handoff both import ingest — so everything that
// would need those packages (the account's billing channel, the footprint
// threshold) is injected through Config.
package cachecold

import (
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/guards"
)

// Kind is the billing channel of a session's account: it decides the TTL.
type Kind string

const (
	// KindSubscription is a Claude subscription login — Claude Code writes the
	// 1 h cache. The default whenever the channel cannot be told (legacy rows,
	// an account whose config dir is gone).
	KindSubscription Kind = "subscription"
	// KindAPI is an API-key account — the 5 min cache.
	KindAPI Kind = "api"
)

// Factor is how much of the previous context a turn must re-write to count as
// a cold restart: written ≥ Factor × (previous footprint − what the cache still
// served). 0.8, not 1.0: a turn that also trimmed a few tool results still
// re-wrote the bulk. `swarmery cachecold-backfill` prints what 0.7 / 0.8 / 0.9
// would mark, so the factor can be calibrated against real history.
const Factor = 0.8

// SharedPrefixTokens is how much a cold turn may still read from cache. A pause
// past the TTL does NOT leave cache_read at 0: the system prompt and tool
// definitions are one prefix shared by every session of the account, and
// another live session keeps it warm. Measured on the operator store
// (2026-09-22..2026-10-22, 151 main-thread turns after a > 60 min pause): only
// 10 read 0; the rest read 23k–35k — the prefix — and re-wrote the remainder.
// Below this bound (or ReadNearZero × the previous footprint, when larger) a
// turn read "~nothing" of its own session's context.
const SharedPrefixTokens = 40_000

// ReadNearZero is the proportional half of the "read ~nothing" bound, for
// contexts so large that the shared prefix is a rounding error.
const ReadNearZero = 0.1

// Turn is the usage and timing of one assistant turn, as stored on turns.
type Turn struct {
	StartedAt time.Time
	EndedAt   time.Time
	TokensIn  int64
	CacheRead int64
	// CacheWrite is the flat tokens_cache_write total; CacheWrite5m/1h are the
	// same tokens split by TTL (migration 0075), known only when Split is set.
	CacheWrite   int64
	CacheWrite5m int64
	CacheWrite1h int64
	Split        bool
}

// Footprint is the context footprint of a turn: tokens_in + cache_read +
// cache_write — the measure handoff.Threshold is compared against (handoff.go
// exposes it as handoff.Footprint) and the one the session card shows as
// contextTokens.
func Footprint(t Turn) int64 { return t.TokensIn + t.CacheRead + t.CacheWrite }

// RealTurnSQL is the SQL predicate (over turns columns) for an assistant turn
// that was a real API request: a positive footprint. Claude Code also writes
// `<synthetic>` assistant messages — an API error, an interrupt — with all-zero
// usage; they refresh no cache, so they are skipped when looking for the turn a
// pause is measured from. On the operator store, 8 of the 16 expensive turns
// after a pause that the first backfill missed sat right behind one.
const RealTurnSQL = `(COALESCE(tokens_in, 0) + COALESCE(tokens_cache_read, 0) + COALESCE(tokens_cache_write, 0)) > 0`

// Written is what the turn wrote to cache: cache_write_5m + cache_write_1h when
// the split is known, the flat total otherwise (rows ingested before 0075).
func (t Turn) Written() int64 {
	if t.Split {
		return t.CacheWrite5m + t.CacheWrite1h
	}
	return t.CacheWrite
}

// Rule is the cold-restart test for one billing channel.
type Rule struct {
	TTL    time.Duration
	Factor float64
}

// Config carries what the package cannot import.
type Config struct {
	// ColdMinutes is guards.Load().CacheColdMinutes.
	ColdMinutes guards.CacheCold
	// MinFootprint gates the PREDICTION only — handoff.Threshold in production:
	// below it a cold cache is cheap and the badge would be noise.
	MinFootprint int64
	// KindOf resolves a sessions.account key ("" = the default account) to its
	// billing channel. nil → KindSubscription for every account.
	KindOf func(account string) Kind
}

// DefaultConfig is the shipped guards defaults with every account read as a
// subscription and no footprint gate — what ingest uses until the daemon
// injects the real one. An error means the embedded guards.json is broken.
func DefaultConfig() (Config, error) {
	g, err := guards.Defaults()
	if err != nil {
		return Config{}, err
	}
	return Config{ColdMinutes: g.CacheColdMinutes}, nil
}

// Kind resolves an account's billing channel; anything but KindAPI reads as a
// subscription.
func (c Config) Kind(account string) Kind {
	if c.KindOf != nil && c.KindOf(account) == KindAPI {
		return KindAPI
	}
	return KindSubscription
}

// TTL is the cache lifetime of a billing channel.
func (c Config) TTL(k Kind) time.Duration {
	if k == KindAPI {
		return time.Duration(c.ColdMinutes.API) * time.Minute
	}
	return time.Duration(c.ColdMinutes.Subscription) * time.Minute
}

// Rule is the cold-restart rule of a billing channel at the shipped Factor.
func (c Config) Rule(k Kind) Rule { return Rule{TTL: c.TTL(k), Factor: Factor} }

// IsColdRestart reports whether cur — the assistant turn after prev in the same
// thread — paid for a cold cache:
//
//   - it started more than r.TTL after prev ended;
//   - it read ~nothing of the session's own context: cache_read is at most the
//     shared prefix (SharedPrefixTokens, or ReadNearZero × prev's footprint
//     when that is larger);
//   - it re-wrote at least r.Factor of the rest: written ≥ r.Factor ×
//     (prev footprint − cache_read).
//
// A previous context no larger than the shared prefix is never marked: a warm
// read and a cold one look alike there, and the re-write is cheap. Nor is a turn
// with no timestamps or a non-positive TTL.
func IsColdRestart(prev, cur Turn, r Rule) bool {
	prevFP := Footprint(prev)
	if prevFP <= SharedPrefixTokens || r.TTL <= 0 || prev.EndedAt.IsZero() || cur.StartedAt.IsZero() {
		return false
	}
	if cur.StartedAt.Sub(prev.EndedAt) <= r.TTL {
		return false
	}
	fp := float64(prevFP)
	readCap := max(ReadNearZero*fp, SharedPrefixTokens)
	if float64(cur.CacheRead) > readCap {
		return false
	}
	return float64(cur.Written()) >= r.Factor*(fp-float64(cur.CacheRead))
}
