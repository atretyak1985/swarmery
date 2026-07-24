package api

// Usage popover (fusion phase 14): Claude subscription windows with a pace
// indicator, adapted from Fusion's Command-Center usage widget.
//
// OAuth SPIKE OUTCOME — telemetry estimate only (source:"estimate").
// Reaching Anthropic's real subscription-usage endpoint requires harvesting the
// subscription OAuth bearer from the credential store (~/.claude/.credentials
// or the macOS Keychain) and replaying it against an undocumented, unstable
// endpoint — credential exfiltration + a fragile private-API dependency, out of
// policy. So this endpoint self-estimates from OUR indexed telemetry and marks
// every window `source:"estimate"`; the UI badges it plainly and never presents
// it as exact. The `source` field is kept so a future in-policy OAuth
// integration can flip windows to `"oauth"` without a contract change.
//
// Configuration: SWARMERY_USAGE_LIMITS is a JSON object of window quotas, e.g.
//   {"session5h":{"label":"5-hour session","tokens":50000000,"windowHours":5},
//    "weekly":{"label":"Weekly","tokens":300000000,"windowHours":168}}
// Unset/blank → the endpoint returns an empty window list with configured:false
// (the popover shows a "set SWARMERY_USAGE_LIMITS to track quota" hint rather
// than fabricating limits). `used` = indexed input+output tokens in the rolling
// window across ALL projects (archived included — quota is billed regardless).

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// usageWindowConfig is one configured quota window from SWARMERY_USAGE_LIMITS.
type usageWindowConfig struct {
	Label       string  `json:"label"`
	Tokens      int64   `json:"tokens"`      // quota for the window
	WindowHours float64 `json:"windowHours"` // rolling window length
}

// usageWindowDTO is one rendered window. usedPct/pace are fractions; the UI
// renders them as percentages. resetsAt is when the current rolling window's
// oldest edge rolls off (now + windowHours in this simple rolling model).
type usageWindowDTO struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Used     int64   `json:"used"`
	Limit    int64   `json:"limit"`
	UsedPct  float64 `json:"usedPct"`  // used/limit, clamped 0..1+ (may exceed 1)
	Pace     float64 `json:"pace"`     // usedPct/elapsedPct - 1 (positive = over pace)
	ResetsAt string  `json:"resetsAt"` // RFC3339
	Source   string  `json:"source"`   // "estimate" (see spike note) | "oauth"
}

type usageDTO struct {
	Configured bool             `json:"configured"`
	Source     string           `json:"source"` // "estimate"
	GeneratedAt string          `json:"generatedAt"`
	Windows    []usageWindowDTO `json:"windows"`
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

// pace is Fusion's "N% over/under pace" figure: how the consumption rate so far
// compares to a linear burn of the quota across the window.
//
//	pace = usedPct / elapsedPct - 1
//
// where usedPct = used/limit and elapsedPct = elapsed/windowLength. Positive =
// burning faster than linear (over pace); negative = under. A full or past
// window (elapsedPct >= 1) has no forward pace signal → 0. A zero limit → 0.
// Pure; unit-tested against fixed clock fixtures.
func pace(used, limit int64, elapsed, window time.Duration) float64 {
	if limit <= 0 || window <= 0 {
		return 0
	}
	elapsedPct := elapsed.Hours() / window.Hours()
	if elapsedPct <= 0 || elapsedPct >= 1 {
		return 0
	}
	usedPct := float64(used) / float64(limit)
	return usedPct/elapsedPct - 1
}

// usageWindowElapsed models where "now" sits inside the current rolling window.
// For a simple rolling window anchored at the local calendar boundary of its
// length, elapsed is (now - windowStart). We anchor session windows to the
// hour and the weekly window to the local week to give a meaningful "resets in"
// countdown without needing Anthropic's real reset schedule (this is an
// estimate — the popover says so). Pure; unit-tested.
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

// usageCacheEntry is the 60s-cached computed response.
type usageCacheEntry struct {
	at   time.Time
	body usageDTO
}

var (
	usageCacheMu sync.Mutex
	usageCache   *usageCacheEntry
)

const usageCacheTTL = 60 * time.Second

// usedTokensSince sums indexed input+output tokens across ALL projects since
// the given UTC bound (quota is billed regardless of project archival, so no
// archived filter here — unlike the cost analytics). start is the zone-suffix
// free bound form used elsewhere.
func (h *Handler) usedTokensSince(startUTC string) (int64, error) {
	var n int64
	err := h.DB.QueryRow(`
		SELECT COALESCE(SUM(COALESCE(tokens_in,0) + COALESCE(tokens_out,0)), 0)
		FROM turns
		WHERE started_at >= ?`, startUTC).Scan(&n)
	return n, err
}

// GET /api/usage — subscription-window usage with pace. See the file header for
// the OAuth spike outcome (estimate-only). Cached 60s; the Refresh button in
// the popover appends ?fresh=1 to bypass the cache.
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("fresh") != "1" {
		usageCacheMu.Lock()
		if usageCache != nil && time.Since(usageCache.at) < usageCacheTTL {
			body := usageCache.body
			usageCacheMu.Unlock()
			writeJSON(w, body, nil)
			return
		}
		usageCacheMu.Unlock()
	}

	cfg, err := parseUsageLimits(os.Getenv("SWARMERY_USAGE_LIMITS"))
	if err != nil {
		http.Error(w, `{"error":"invalid SWARMERY_USAGE_LIMITS JSON"}`, http.StatusInternalServerError)
		return
	}

	now := time.Now()
	out := usageDTO{
		Source:      "estimate",
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Windows:     []usageWindowDTO{},
	}
	out.Configured = len(cfg) > 0

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

	const bound = "2006-01-02T15:04:05"
	for _, k := range keys {
		c := cfg[k]
		elapsed, window, resetsAt := usageWindowElapsed(now, c.WindowHours)
		windowStart := now.Add(-elapsed)
		used, err := h.usedTokensSince(windowStart.UTC().Format(bound))
		if err != nil {
			writeErr(w, err)
			return
		}
		label := c.Label
		if label == "" {
			label = k
		}
		usedPct := 0.0
		if c.Tokens > 0 {
			usedPct = float64(used) / float64(c.Tokens)
		}
		out.Windows = append(out.Windows, usageWindowDTO{
			Key:      k,
			Label:    label,
			Used:     used,
			Limit:    c.Tokens,
			UsedPct:  usedPct,
			Pace:     pace(used, c.Tokens, elapsed, window),
			ResetsAt: resetsAt.UTC().Format(time.RFC3339),
			Source:   "estimate",
		})
	}

	usageCacheMu.Lock()
	usageCache = &usageCacheEntry{at: now, body: out}
	usageCacheMu.Unlock()

	writeJSON(w, out, nil)
}
