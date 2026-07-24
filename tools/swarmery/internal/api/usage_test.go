package api

import (
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// ── pure-function tests (fixed clock, no DB) ───────────────────────────────

func TestParseUsageLimits(t *testing.T) {
	// Blank → not configured, not an error.
	if cfg, err := parseUsageLimits("   "); err != nil || cfg != nil {
		t.Errorf("blank: got cfg=%v err=%v, want nil,nil", cfg, err)
	}
	// Invalid JSON → error.
	if _, err := parseUsageLimits(`{not json`); err == nil {
		t.Errorf("invalid JSON: want error, got nil")
	}
	// Valid: a positive window is kept; a non-positive quota/window is dropped
	// (defensive — a misconfigured window must not divide-by-zero downstream).
	raw := `{
		"session5h":{"label":"5-hour session","tokens":50000000,"windowHours":5},
		"weekly":{"label":"Weekly","tokens":300000000,"windowHours":168},
		"bogusZeroTokens":{"tokens":0,"windowHours":5},
		"bogusZeroWindow":{"tokens":10,"windowHours":0}
	}`
	cfg, err := parseUsageLimits(raw)
	if err != nil {
		t.Fatalf("valid parse: %v", err)
	}
	if len(cfg) != 2 {
		t.Fatalf("kept %d windows, want 2 (two bogus dropped)", len(cfg))
	}
	if cfg["session5h"].Tokens != 50000000 || cfg["session5h"].WindowHours != 5 {
		t.Errorf("session5h = %+v", cfg["session5h"])
	}
	if _, ok := cfg["bogusZeroTokens"]; ok {
		t.Errorf("bogusZeroTokens should have been dropped")
	}
	if _, ok := cfg["bogusZeroWindow"]; ok {
		t.Errorf("bogusZeroWindow should have been dropped")
	}
}

func TestPace(t *testing.T) {
	hr := time.Hour
	cases := []struct {
		name    string
		used    int64
		limit   int64
		elapsed time.Duration
		window  time.Duration
		want    float64
	}{
		// Exactly linear: 50% used at 50% elapsed → pace 0.
		{"linear", 50, 100, 5 * hr, 10 * hr, 0},
		// Over pace: Fusion's canonical "17% over" — 35% used at ~30% elapsed.
		// usedPct/elapsedPct - 1 = 0.35/0.30 - 1 = 0.1666…
		{"over", 35, 100, 3 * hr, 10 * hr, 0.35/0.30 - 1},
		// Under pace: 10% used at 50% elapsed → 0.10/0.50 - 1 = -0.8.
		{"under", 10, 100, 5 * hr, 10 * hr, -0.8},
		// Full/past window → no forward signal → 0.
		{"full-window", 90, 100, 10 * hr, 10 * hr, 0},
		{"past-window", 90, 100, 12 * hr, 10 * hr, 0},
		// Zero elapsed → 0 (avoid div-by-zero).
		{"zero-elapsed", 5, 100, 0, 10 * hr, 0},
		// Zero limit → 0.
		{"zero-limit", 5, 0, 5 * hr, 10 * hr, 0},
		// Zero window → 0.
		{"zero-window", 5, 100, 5 * hr, 0, 0},
	}
	for _, c := range cases {
		got := pace(c.used, c.limit, c.elapsed, c.window)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: pace = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestUsageWindowElapsed(t *testing.T) {
	// Fixed clock. The window is anchored to a deterministic grid (whole windows
	// since the Unix epoch) so "resets in" is stable between polls.
	loc := time.UTC
	now := time.Date(2026, 7, 24, 13, 30, 0, 0, loc) // 13:30:00 UTC

	// 5-hour window. epoch = now.Unix(); winSec = 5h. windowStart = floor to the
	// 5h grid; elapsed = now - windowStart ∈ [0, 5h); resetsAt = windowStart+5h.
	elapsed, window, resetsAt := usageWindowElapsed(now, 5)
	if window != 5*time.Hour {
		t.Errorf("window = %v, want 5h", window)
	}
	if elapsed < 0 || elapsed >= 5*time.Hour {
		t.Errorf("elapsed = %v, want within [0,5h)", elapsed)
	}
	// resetsAt is exactly windowStart + window, and windowStart = now - elapsed.
	wantReset := now.Add(-elapsed).Add(window)
	if !resetsAt.Equal(wantReset) {
		t.Errorf("resetsAt = %v, want %v", resetsAt, wantReset)
	}
	// The grid is aligned: (windowStart since epoch) is a whole multiple of 5h.
	windowStart := now.Add(-elapsed)
	if windowStart.Unix()%int64((5 * time.Hour).Seconds()) != 0 {
		t.Errorf("windowStart %v not aligned to the 5h grid", windowStart)
	}

	// Determinism: two calls at the same instant agree.
	e2, _, r2 := usageWindowElapsed(now, 5)
	if e2 != elapsed || !r2.Equal(resetsAt) {
		t.Errorf("non-deterministic: (%v,%v) vs (%v,%v)", e2, r2, elapsed, resetsAt)
	}
}

// ── HTTP integration test ──────────────────────────────────────────────────

func TestUsageHTTP(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	now := time.Now()
	// Two turns inside the last hour (well within any multi-hour window), one far
	// in the past (outside a 5h window). tokens_in+tokens_out summed.
	recent := now.Add(-10 * time.Minute).UTC().Format("2006-01-02T15:04:05")
	old := now.Add(-240 * time.Hour).UTC().Format("2006-01-02T15:04:05")
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v\n%s", err, q)
		}
	}
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/w/a', '-w-a', 'A', ?)`, old)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES (1, 1, 'u1', 'm', 'completed', ?)`, recent)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out) VALUES
		(1, 1, 'assistant', ?, 1000, 500),
		(1, 2, 'assistant', ?, 200, 100),
		(1, 3, 'assistant', ?, 999999, 999999)`, recent, recent, old)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// Configured: a 5-hour window with a generous quota. used = 1500+300 = 1800
	// (the 5h-old turn is excluded). ?fresh=1 bypasses the 60s cache so the test
	// is not order-dependent on a prior run.
	t.Setenv("SWARMERY_USAGE_LIMITS", `{"session5h":{"label":"5-hour session","tokens":1000000,"windowHours":5}}`)
	resetUsageCache()

	var got usageDTO
	getJSON(t, srv.URL+"/api/usage?fresh=1", &got)
	if !got.Configured {
		t.Errorf("configured = false, want true")
	}
	if got.Source != "estimate" {
		t.Errorf("source = %q, want estimate", got.Source)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(got.Windows))
	}
	win := got.Windows[0]
	if win.Used != 1800 {
		t.Errorf("used = %d, want 1800 (old turn excluded)", win.Used)
	}
	if win.Limit != 1000000 {
		t.Errorf("limit = %d, want 1000000", win.Limit)
	}
	if win.Source != "estimate" {
		t.Errorf("window source = %q, want estimate", win.Source)
	}
	// resetsAt must be valid RFC3339 in the future.
	rt, perr := time.Parse(time.RFC3339, win.ResetsAt)
	if perr != nil {
		t.Errorf("resetsAt %q not RFC3339: %v", win.ResetsAt, perr)
	}
	if !rt.After(now.Add(-time.Second)) {
		t.Errorf("resetsAt %v should be ~now or later", rt)
	}

	// Unconfigured: blank env → configured:false, empty window list, HTTP 200.
	t.Setenv("SWARMERY_USAGE_LIMITS", "")
	resetUsageCache()
	var empty usageDTO
	getJSON(t, srv.URL+"/api/usage?fresh=1", &empty)
	if empty.Configured {
		t.Errorf("unconfigured: configured = true, want false")
	}
	if len(empty.Windows) != 0 {
		t.Errorf("unconfigured: windows = %d, want 0", len(empty.Windows))
	}
	if empty.Source != "estimate" {
		t.Errorf("unconfigured: source = %q, want estimate", empty.Source)
	}
}

// TestUsageTwoWindowsOrderAndLabelFallback covers the shortest-window-first sort
// comparator (needs ≥2 windows to fire) and the label fallback (a window with no
// label reports its key). It also exercises the 60s cache-hit fast path.
func TestUsageTwoWindowsOrderAndLabelFallback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage2.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	recent := time.Now().Add(-5 * time.Minute).UTC().Format("2006-01-02T15:04:05")
	mustExec(`INSERT INTO projects (id, path, slug, name, first_seen) VALUES (1, '/w/a', '-w-a', 'A', ?)`, recent)
	mustExec(`INSERT INTO sessions (id, project_id, session_uuid, model, status, started_at) VALUES (1, 1, 'u1', 'm', 'completed', ?)`, recent)
	mustExec(`INSERT INTO turns (session_id, seq, role, started_at, tokens_in, tokens_out) VALUES (1, 1, 'assistant', ?, 10, 5)`, recent)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	// weekly (168h) declared before session5h (5h); the "weekly" window has NO
	// label → must fall back to its key. Sort must put the 5h window first.
	t.Setenv("SWARMERY_USAGE_LIMITS",
		`{"weekly":{"tokens":9000000,"windowHours":168},"session5h":{"label":"5h","tokens":900000,"windowHours":5}}`)
	resetUsageCache()

	var got usageDTO
	getJSON(t, srv.URL+"/api/usage?fresh=1", &got)
	if len(got.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(got.Windows))
	}
	if got.Windows[0].Key != "session5h" {
		t.Errorf("first window = %q, want session5h (shortest first)", got.Windows[0].Key)
	}
	if got.Windows[1].Key != "weekly" || got.Windows[1].Label != "weekly" {
		t.Errorf("second window label = %q, want key fallback %q", got.Windows[1].Label, "weekly")
	}

	// Cache-hit fast path: a NON-fresh call returns the cached body (populated by
	// the ?fresh=1 call above) without recomputation.
	var cached usageDTO
	getJSON(t, srv.URL+"/api/usage", &cached)
	if len(cached.Windows) != 2 || cached.Windows[0].Key != "session5h" {
		t.Errorf("cache-hit body = %+v, want the cached 2-window response", cached.Windows)
	}
}

// TestUsageDBErrorPath proves the handler surfaces a 500 (not a panic or a
// half-written body) when the token query fails — here by closing the DB before
// the request so usedTokensSince errors.
func TestUsageDBErrorPath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage-err.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	t.Setenv("SWARMERY_USAGE_LIMITS", `{"session5h":{"label":"5h","tokens":900000,"windowHours":5}}`)
	resetUsageCache()
	db.Close() // force every subsequent query to fail

	resp, err := http.Get(srv.URL + "/api/usage?fresh=1")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 on DB error", resp.StatusCode)
	}
}
