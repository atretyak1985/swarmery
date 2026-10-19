package api

// Usage modal (fusion phase 14 → live quotas): GET /api/usage serves a PROVIDER
// ARRAY — the live Claude subscription reading first, the telemetry estimate
// second.
//
// # Policy note — the 2026-06 OAuth spike is reversed
//
// This file used to open with a spike note declaring the OAuth path
// "credential exfiltration + a fragile private-API dependency, out of policy",
// and self-estimated every window from our own indexed telemetry. That decision
// is reversed. The reasoning is recorded in full in the internal/usage package
// doc (tools/swarmery/internal/usage/types.go); in short: the daemon reads the
// operator's OWN credential on the operator's OWN machine and sends it ONLY to
// Anthropic — the exact call `claude /usage` makes — never persisting, logging,
// or serializing it. See that package doc before touching this path.
//
// Providers:
//
//   - "Claude" (source:"oauth") — usage.Client.Fetch. Never returns an error;
//     every failure mode (opted out, not logged in, auth rejected, rate limited,
//     malformed payload) degrades to a visible per-provider error card, so one
//     broken provider can never break the endpoint.
//   - "Telemetry estimate" (source:"estimate") — the previous behaviour, demoted
//     to a SECOND card and emitted ONLY when SWARMERY_USAGE_LIMITS is set. It is
//     a self-estimate and must never masquerade as the real reading.
//
// The old top-level `configured` flag is gone: the frontend and the daemon ship
// together (go:embed), so there is no compatibility window to preserve, and the
// estimate card's presence now carries the same information.
//
// Configuration: SWARMERY_USAGE_LIMITS is a JSON object of window quotas, e.g.
//
//	{"session5h":{"label":"5-hour session","tokens":50000000,"windowHours":5},
//	 "weekly":{"label":"Weekly","tokens":300000000,"windowHours":168}}
//
// Unset/blank → no estimate card at all. `used` = indexed input+output tokens in
// the rolling window across ALL projects (archived included — quota is billed
// regardless).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/usage"
)

// claudeFetchTimeout bounds the whole live-provider fetch. Fusion budgets 75s
// only because of its node-pty fallback (usage.ts:2346); without that fallback,
// 3 retries plus backoff fit comfortably inside 20s.
const claudeFetchTimeout = 20 * time.Second

// estimateProviderName is the second card's label. Deliberately explicit: the
// operator must never mistake the estimate for the real subscription reading.
const estimateProviderName = "Telemetry estimate"

// usageClient is the DEFAULT account's live-quota client. The refreshed-token
// cache lives on it, so it must outlive a single request. Tests swap this for a
// client wired to a stub endpoint through usage.Client's seams.
//
// Its Src names the default account even though the ConfigDir is empty (the
// legacy chain resolves that account). The NAME is load-bearing on its own:
// LoadCredsFor checks swarmery's own credential store by account key first, and
// the Connect flow writes there — so a client with an anonymous Src would be the
// one account that could never be connected from the dashboard.
var usageClient = &usage.Client{Src: usage.Source{Account: ingest.DefaultAccount}}

// Named accounts get their own clients, minted on first use and kept for the
// same reason: one refreshed-token cache per credential. Keyed by account, which
// accountsFromRoots has already deduped.
var (
	usageClientsMu sync.Mutex
	usageClients   = map[string]*usage.Client{}
)

// usageClientFor returns the client that speaks for src. The default account
// deliberately keeps using the package-level `usageClient` var: it is the seam
// the existing tests install a stub through, and it is the account whose
// credential resolves via the legacy chain.
func usageClientFor(src usage.Source) *usage.Client {
	if src.ConfigDir == "" {
		return usageClient
	}
	usageClientsMu.Lock()
	defer usageClientsMu.Unlock()
	if c, ok := usageClients[src.Account]; ok {
		return c
	}
	c := &usage.Client{Src: src}
	usageClients[src.Account] = c
	return c
}

// accountsFromRoots derives the accounts to poll from the ingest transcript
// roots. Pure — the roots are the enumeration, and ingest.AccountFor is the SAME
// key the sessions table is stamped with, so a usage card and a session row
// agree on what "nabu-org" means.
//
// Each root is "<configDir>/projects", so the account's config dir is the root's
// parent. Duplicate keys are dropped (a root need not end in /projects, and two
// roots can name one account), and roots with no account context at all are
// skipped.
//
// The DEFAULT account is given an EMPTY ConfigDir on purpose: it then resolves
// through the legacy chain (CLAUDE_CONFIG_DIR, ~/.claude, ~/.config/claude, and
// the plain keychain item on darwin), which is both what shipped and the only
// source that works for the stock account on macOS. NO roots at all yields
// exactly one default account, so an unusual boot config — and the api test
// binary, where transcriptsRoots is empty — behaves exactly as before.
//
// Honesty note: on a stock config the roots are just ~/.claude/projects, so more
// than one card REQUIRES SWARMERY_PROJECTS_ROOTS=auto (or an explicit list) on
// every OS. Without it this returns the single default account.
func accountsFromRoots(roots []string) []usage.Source {
	out := make([]usage.Source, 0, len(roots)+1)
	seen := make(map[string]bool, len(roots)+1)
	for _, root := range roots {
		key := ingest.AccountFor(root)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		src := usage.Source{Account: key}
		if key != ingest.DefaultAccount {
			src.ConfigDir = filepath.Dir(filepath.Clean(strings.TrimSpace(root)))
		}
		out = append(out, src)
	}
	if len(out) == 0 {
		return []usage.Source{{Account: ingest.DefaultAccount}}
	}
	return out
}

// usageAccount is one subscription's row: its cards, or the reason there are
// none. A row-level Error is NOT how a failed quota read surfaces — that is a
// per-provider error/hint card (usage.Client.Fetch never returns an error) — it
// is the last-resort guard for an account whose lookup blew up entirely, so one
// account can never take the endpoint down with it.
type usageAccount struct {
	Account   string           `json:"account"`
	Providers []usage.Provider `json:"providers"`
	Error     string           `json:"error,omitempty"`
}

// fetchAccounts polls every account CONCURRENTLY, one goroutine each. Serial
// fetches would share the handler's 20s budget across N accounts — with up to 3
// retries and backoff per account, the tail cards would time out — while the
// calls are independent and each client has its own token cache.
func fetchAccounts(ctx context.Context, srcs []usage.Source) []usageAccount {
	rows := make([]usageAccount, len(srcs))
	var wg sync.WaitGroup
	for i, src := range srcs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows[i] = fetchAccount(ctx, src)
		}()
	}
	wg.Wait()
	return rows
}

// fetchAccount builds one account's row. The recover is the containment: Fetch
// is documented never to return an error, so anything escaping it is a bug, and
// a bug in one account's credential path must still leave the other accounts'
// cards on screen. The recovered value is deliberately NOT echoed into the
// response — it can carry arbitrary interpolated state.
func fetchAccount(ctx context.Context, src usage.Source) (row usageAccount) {
	row = usageAccount{Account: src.Account, Providers: []usage.Provider{}}
	defer func() {
		if rec := recover(); rec != nil {
			row.Providers = []usage.Provider{}
			row.Error = "usage lookup failed for this account"
		}
	}()
	p := usageClientFor(src).Fetch(ctx)
	// The ROW is the authority on which account a card belongs to: a client
	// installed by a test (or any client built without a Src) would otherwise
	// ship an unlabelled card and collide with the default account's in the UI.
	p.Account = src.Account
	row.Providers = append(row.Providers, p)
	return row
}

// defaultAccountRow is the row the top-level alias fields speak for. A daemon
// configured with only a named root has no default account at all, so the first
// row stands in — the chip still needs one card to speak for.
func defaultAccountRow(rows []usageAccount) *usageAccount {
	for i := range rows {
		if rows[i].Account == ingest.DefaultAccount {
			return &rows[i]
		}
	}
	if len(rows) > 0 {
		return &rows[0]
	}
	return nil
}

// errBadUsageLimits marks a malformed SWARMERY_USAGE_LIMITS so the handler can
// answer with the specific 500 body this endpoint has always returned, rather
// than the generic error shape.
var errBadUsageLimits = errors.New("invalid SWARMERY_USAGE_LIMITS JSON")

// usageWindowConfig is one configured quota window from SWARMERY_USAGE_LIMITS.
type usageWindowConfig struct {
	Label       string  `json:"label"`
	Tokens      int64   `json:"tokens"`      // quota for the window
	WindowHours float64 `json:"windowHours"` // rolling window length
}

// usageResp is the /api/usage body. The window/provider shapes are owned by
// internal/usage and serialized verbatim — this package does not redefine or
// re-map their JSON tags.
//
// `accounts` is the real payload: one row per subscription the daemon can see.
// `providers` is a deliberate ALIAS of the default account's row — the header
// chip wants one card and nothing else, and making it walk accounts[] to find
// the default one would be work on every render for a value the daemon already
// knows. It is not a compatibility shim: the SPA and the daemon ship together
// (go:embed), so no version of one ever meets a different version of the other.
type usageResp struct {
	GeneratedAt string           `json:"generatedAt"`
	Providers   []usage.Provider `json:"providers"`
	Accounts    []usageAccount   `json:"accounts"`
}

// parseUsageLimits parses the SWARMERY_USAGE_LIMITS JSON. Blank → (nil, nil):
// not configured, not an error. Invalid JSON or a non-positive quota/window is
// an error the caller surfaces. Pure; unit-tested.
func parseUsageLimits(raw string) (map[string]usageWindowConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var cfg map[string]usageWindowConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, err
	}
	for k, v := range cfg {
		if v.Tokens <= 0 || v.WindowHours <= 0 {
			delete(cfg, k)
		}
	}
	return cfg, nil
}

// usageWindowElapsed models where "now" sits inside the current rolling window.
// For a simple rolling window anchored at the local calendar boundary of its
// length, elapsed is (now - windowStart). We anchor session windows to the
// hour and the weekly window to the local week to give a meaningful "resets in"
// countdown without needing Anthropic's real reset schedule (this is an
// estimate — the card says so). Pure; unit-tested.
func usageWindowElapsed(now time.Time, windowHours float64) (elapsed, window time.Duration, resetsAt time.Time) {
	window = time.Duration(windowHours * float64(time.Hour))
	// Anchor the rolling window to a deterministic grid so "resets in" is stable
	// between polls: number of whole windows since the Unix epoch, in local time.
	epoch := now.Unix()
	winSec := int64(window / time.Second)
	if winSec <= 0 {
		return 0, window, now
	}
	startSec := (epoch / winSec) * winSec
	windowStart := time.Unix(startSec, 0).In(now.Location())
	elapsed = now.Sub(windowStart)
	resetsAt = windowStart.Add(window)
	return elapsed, window, resetsAt
}

// usageCacheEntry is the cached computed response.
type usageCacheEntry struct {
	at   time.Time
	body usageResp
}

var (
	usageCacheMu sync.Mutex
	usageCache   *usageCacheEntry
)

// usageCacheTTL matches Fusion's CACHE_TTL_MS (usage.ts:107). It is deliberately
// short: this now costs a live Anthropic round-trip per miss.
const usageCacheTTL = 30 * time.Second

// resetUsageCache clears the process-wide usage cache. Used by tests to make
// assertions independent of a prior computed body (the ?fresh=1 query bypasses
// the cache read but still repopulates it).
func resetUsageCache() {
	usageCacheMu.Lock()
	usageCache = nil
	usageCacheMu.Unlock()
}

// cachedUsage returns the cached body when it is still within the TTL.
func cachedUsage() (usageResp, bool) {
	usageCacheMu.Lock()
	defer usageCacheMu.Unlock()
	if usageCache != nil && time.Since(usageCache.at) < usageCacheTTL {
		return usageCache.body, true
	}
	return usageResp{}, false
}

// storeUsage replaces the cached body.
func storeUsage(body usageResp, at time.Time) {
	usageCacheMu.Lock()
	usageCache = &usageCacheEntry{at: at, body: body}
	usageCacheMu.Unlock()
}

// usedTokensSince sums indexed input+output tokens across ALL projects since
// the given UTC bound (quota is billed regardless of project archival, so no
// archived filter here — unlike the cost analytics). start is the zone-suffix
// free bound form used elsewhere.
func (h *Handler) usedTokensSince(startUTC string) (int64, error) {
	var n int64
	err := h.rdb().QueryRow(`
		SELECT COALESCE(SUM(COALESCE(tokens_in,0) + COALESCE(tokens_out,0)), 0)
		FROM turns
		WHERE started_at >= ?`, startUTC).Scan(&n)
	return n, err
}

// estimateProvider builds the secondary telemetry-estimate card from
// SWARMERY_USAGE_LIMITS and our own indexed token counts.
//
// ok=false (with a nil error) means SWARMERY_USAGE_LIMITS is unset or empty —
// no estimate card is emitted at all. A malformed value is errBadUsageLimits; a
// failed token query is returned as-is so the handler can 500.
//
// Pace is computed by usage.CalculatePace so both cards share ONE definition.
// This is a deliberate behavioural change from the old ratio helper — see the
// pace-semantics table in usage_test.go.
func (h *Handler) estimateProvider(now time.Time) (usage.Provider, bool, error) {
	cfg, err := parseUsageLimits(os.Getenv("SWARMERY_USAGE_LIMITS"))
	if err != nil {
		return usage.Provider{}, false, fmt.Errorf("%w: %v", errBadUsageLimits, err)
	}
	if len(cfg) == 0 {
		return usage.Provider{}, false, nil
	}

	// Deterministic order: shortest window first (session before weekly).
	keys := make([]string, 0, len(cfg))
	for k := range cfg {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if cfg[keys[i]].WindowHours != cfg[keys[j]].WindowHours {
			return cfg[keys[i]].WindowHours < cfg[keys[j]].WindowHours
		}
		return keys[i] < keys[j]
	})

	p := usage.Provider{
		Name:    estimateProviderName,
		Status:  usage.StatusOK,
		Source:  usage.SourceEstimate,
		Windows: make([]usage.Window, 0, len(keys)),
	}

	const bound = "2006-01-02T15:04:05"
	for _, k := range keys {
		c := cfg[k]
		elapsed, window, resetsAt := usageWindowElapsed(now, c.WindowHours)
		windowStart := now.Add(-elapsed)
		used, err := h.usedTokensSince(windowStart.UTC().Format(bound))
		if err != nil {
			return usage.Provider{}, false, err
		}
		label := c.Label
		if label == "" {
			label = k
		}
		// Clamped to 0..100 to match the oauth provider's windows; the raw
		// over-quota signal survives in Used/Limit.
		percentUsed := 0.0
		if c.Tokens > 0 {
			percentUsed = math.Min(100, math.Max(0, float64(used)/float64(c.Tokens)*100))
		}
		resetMs := resetsAt.Sub(now).Milliseconds()
		w := usage.Window{
			Key:         k,
			Label:       label,
			PercentUsed: percentUsed,
			PercentLeft: 100 - percentUsed,
			ResetMs:     resetMs,
			ResetAt:     resetsAt.UTC().Format(time.RFC3339),
			WindowMs:    window.Milliseconds(),
			Pace:        usage.CalculatePace(percentUsed, resetMs, window.Milliseconds()),
			Source:      usage.SourceEstimate,
			Used:        used,
			Limit:       c.Tokens,
		}
		if resetMs > 0 {
			w.ResetText = "resets in " + usage.FormatDuration(time.Duration(resetMs)*time.Millisecond)
		}
		p.Windows = append(p.Windows, w)
	}
	return p, true, nil
}

// GET /api/usage — one row per account, each carrying its live Claude card,
// plus the optional telemetry-estimate card on the default account's row. See
// the file header for the policy note. Cached 30s as ONE whole response (the
// fan-out is concurrent, so a per-account cache would buy nothing and would let
// rows drift to different instants); the Refresh button in the modal appends
// ?fresh=1 to bypass it.
//
// A failing account degrades to its own row — never to a 500. The only 500s
// left are the operator's own misconfiguration (SWARMERY_USAGE_LIMITS) and a
// broken local DB, both of which predate this endpoint's account dimension.
//
// The response body NEVER carries credential material — asserted by
// TestUsageResponseCarriesNoSecrets, which scans the WHOLE body including
// accounts[].
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("fresh") != "1" {
		if body, ok := cachedUsage(); ok {
			writeJSON(w, body, nil)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), claudeFetchTimeout)
	defer cancel()

	now := time.Now()
	// The live card always appears for every account, whatever its status — an
	// error or "not connected" card is the honest answer, an absent card would
	// be a silent failure.
	rows := fetchAccounts(ctx, accountsFromRoots(transcriptsRoots))
	def := defaultAccountRow(rows)

	est, ok, err := h.estimateProvider(now)
	switch {
	case errors.Is(err, errBadUsageLimits):
		http.Error(w, `{"error":"invalid SWARMERY_USAGE_LIMITS JSON"}`, http.StatusInternalServerError)
		return
	case err != nil:
		writeErr(w, err)
		return
	case ok && def != nil:
		// The estimate is derived from OUR index of every account's turns, so
		// it belongs to no single subscription; it rides the default row rather
		// than being duplicated onto each one.
		est.Account = def.Account
		def.Providers = append(def.Providers, est)
	}

	out := usageResp{
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Providers:   []usage.Provider{},
		Accounts:    rows,
	}
	if def != nil {
		out.Providers = def.Providers
	}

	storeUsage(out, now)
	writeJSON(w, out, nil)
}
