// Package quota records and reads per-account quota HEADROOM — the signal that
// triggers every account switch, which the system used to discover from prose.
//
// The Poller (this file) is the producer: a daemon ticker that asks the usage
// endpoint for each account's windows and stores them in account_quota with a
// fetched_at. Headroom (headroom.go) is the single reader: `swarmery account
// switch` gates on it and refuses an account whose headroom it cannot vouch
// for, and runcore.CheckQuota gates headless run admission on it.
//
// # Decision — the admission gate fails OPEN
//
// internal/runcore/slots.go's TryAcquire still does NOT read this package; the
// admission gate is a separate refusal (runcore.ErrLowQuota) the engines call
// before it. It refuses only on a FRESH reading below the floor. A stale row,
// no rows, or a failed read is UNKNOWN and admits: a gate reading a table one
// missed ticker left stale must never turn into "the daemon stopped
// dispatching" with no error anywhere. MaxAge is the one staleness bound both
// readers share.
package quota

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// DefaultInterval is the poll cadence when SWARMERY_QUOTA_INTERVAL is unset.
const DefaultInterval = 10 * time.Minute

// DefaultStartDelay is how long Run waits before its first pass, so a daemon
// restart does not hit the usage endpoint before it has finished booting.
const DefaultStartDelay = 30 * time.Second

// ParseInterval reads a SWARMERY_QUOTA_INTERVAL value. "" is DefaultInterval;
// "0", "off", "false" and "disabled" (any case) turn the poller off
// (enabled=false); anything else must be a positive time.ParseDuration value —
// an invalid one falls back to DefaultInterval with a non-nil err for the
// caller to log.
func ParseInterval(v string) (d time.Duration, enabled bool, err error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return DefaultInterval, true, nil
	case "0", "off", "false", "disabled":
		return 0, false, nil
	}
	d, err = time.ParseDuration(strings.TrimSpace(v))
	if err != nil || d < 0 {
		return DefaultInterval, true, fmt.Errorf("invalid SWARMERY_QUOTA_INTERVAL %q — using %s", v, DefaultInterval)
	}
	if d == 0 {
		return 0, false, nil
	}
	return d, true, nil
}

// Poller refreshes account_quota for every account on a ticker. The zero value
// of every seam has a working default except DB and Accounts.
type Poller struct {
	DB       *sql.DB
	Interval time.Duration // <= 0 means DefaultInterval
	// Accounts enumerates the accounts to poll (claudeacct.DiscoverWithDefault
	// in the daemon).
	Accounts func() []claudeacct.Account
	// Fetch reads one account's usage. nil means one usage.Client per account
	// key, cached for the process lifetime, built with NoRefresh: the poller
	// NEVER refreshes an OAuth token — an expired one leaves the account's
	// headroom unknown (one log line) until the operator's own `claude` renews
	// it. The default account's Source ignores an inherited CLAUDE_CONFIG_DIR
	// (usage.SourceForAccount).
	Fetch func(context.Context, usage.Source) usage.Provider
	Now   func() time.Time
	// StartDelay overrides DefaultStartDelay (tests); negative means none.
	StartDelay time.Duration

	mu      sync.Mutex
	clients map[string]*usage.Client
}

// Run polls until ctx is done: first pass after the start delay, then every
// Interval.
func (p *Poller) Run(ctx context.Context) {
	delay := p.StartDelay
	if delay == 0 {
		delay = DefaultStartDelay
	}
	if delay > 0 {
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
	p.PollOnce(ctx)
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.PollOnce(ctx)
		}
	}
}

// PollOnce fetches every account once and stores what came back. A provider
// whose Status is not OK writes NOTHING — the previous reading ages out and
// absence stays the honest "unknown" — and logs one line naming the account
// and the status, never the provider's Error body. It returns how many
// accounts were written.
func (p *Poller) PollOnce(ctx context.Context) int {
	if p.DB == nil || p.Accounts == nil {
		return 0
	}
	written := 0
	for _, a := range p.Accounts() {
		if ctx.Err() != nil {
			return written
		}
		src := usage.SourceForAccount(a.Key, a.ConfigDir, a.IsDefault)
		prov := p.fetch(ctx, src)
		if prov.TokenExpired {
			log.Printf("quota: account=%s token expired, not refreshing — nothing recorded", a.Key)
			continue
		}
		if prov.Status != usage.StatusOK {
			log.Printf("quota: account=%s status=%s — nothing recorded", a.Key, prov.Status)
			continue
		}
		rows := Rows(prov)
		if len(rows) == 0 {
			log.Printf("quota: account=%s reported no windows — nothing recorded", a.Key)
			continue
		}
		if err := store.PutAccountQuota(p.DB, a.Key, rows, p.now()); err != nil {
			log.Printf("quota: account=%s: store: %v", a.Key, err)
			continue
		}
		written++
	}
	return written
}

// Rows maps a provider's windows to stored rows. Only numbers, the window's
// key/label and its reset time cross over — never the provider's Error text.
func Rows(prov usage.Provider) []store.QuotaRow {
	out := make([]store.QuotaRow, 0, len(prov.Windows))
	for _, w := range prov.Windows {
		if w.Key == "" {
			continue
		}
		out = append(out, store.QuotaRow{
			WindowKey:   w.Key,
			Label:       w.Label,
			PercentUsed: w.PercentUsed,
			PercentLeft: w.PercentLeft,
			ResetsAt:    w.ResetAt,
			WindowMs:    w.WindowMs,
			Source:      "poller",
		})
	}
	return out
}

func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Poller) fetch(ctx context.Context, src usage.Source) usage.Provider {
	if p.Fetch != nil {
		return p.Fetch(ctx, src)
	}
	return p.client(src).Fetch(ctx)
}

// client returns the cached usage.Client for src's account, minting it once.
func (p *Poller) client(src usage.Source) *usage.Client {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.clients == nil {
		p.clients = map[string]*usage.Client{}
	}
	if c, ok := p.clients[src.Account]; ok {
		return c
	}
	c := &usage.Client{Src: src, NoRefresh: true}
	p.clients[src.Account] = c
	return c
}
