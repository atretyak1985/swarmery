package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// healthOn serves one GET /api/health on h directly and decodes it.
func healthOn(t *testing.T, h *Handler) (healthDTO, time.Duration) {
	t.Helper()
	rr := httptest.NewRecorder()
	start := time.Now()
	h.health(rr, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	elapsed := time.Since(start)
	if rr.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200\n%s", rr.Code, rr.Body)
	}
	var dto healthDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return dto, elapsed
}

// waitHealthRefresh blocks until no background refresh is running.
func waitHealthRefresh(t *testing.T, h *Handler) {
	t.Helper()
	h.healthSlow.mu.Lock()
	done := h.healthSlow.refreshing
	h.healthSlow.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("health refresh did not land within 10s")
	}
}

func healthCacheHandler(t *testing.T) *Handler {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "health-cache.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// RO is left nil on purpose: h.rdb() then falls back to the writer, so
	// holdStoreConn on h.DB also blocks every snapshot read. These tests measure
	// the snapshot path (an answer with no query on the request), which must hold
	// even when the daemon runs without a read pool. With a pool attached, the
	// held writer would not block the refresh and the assertions on cold or
	// stale values would test nothing.
	return &Handler{DB: db}
}

// With a snapshot in hand, a probe runs no query: it answers at once while the
// store's only connection is held, and still reports the live pool counters.
func TestHealthAnswersFromSnapshotWhileConnectionHeld(t *testing.T) {
	h := healthCacheHandler(t)
	first, _ := healthOn(t, h)
	if first.MigrationVersion <= 0 || first.DBSizeBytes <= 0 {
		t.Fatalf("first answer = %+v, want a filled snapshot (migrationVersion, size > 0)", first)
	}

	holdStoreConn(t, h.DB, 3*time.Second)
	// Make the snapshot stale, so the probe also starts a refresh — which
	// queues for the held connection in the background, not on the request.
	h.healthSlow.mu.Lock()
	h.healthSlow.at = time.Now().Add(-2 * healthSlowTTL)
	h.healthSlow.mu.Unlock()

	got, elapsed := healthOn(t, h)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("health took %s with the connection held, want an answer from the snapshot", elapsed)
	}
	if got.MigrationVersion != first.MigrationVersion || got.DBSizeBytes != first.DBSizeBytes {
		t.Errorf("held answer = %+v, want the snapshot's values %+v", got, first)
	}
	if got.DB.InUse != 1 || got.DB.OpenConnections != 1 {
		t.Errorf("db pool = %+v, want the live counters: 1 open, 1 in use", got.DB)
	}
	waitHealthRefresh(t, h)
}

// The very first probe waits at most healthFirstFillWait for a snapshot: on a
// cold, starved daemon it answers 200 with zero values rather than queueing.
func TestHealthFirstFillIsBounded(t *testing.T) {
	h := healthCacheHandler(t)
	holdStoreConn(t, h.DB, healthFirstFillWait+2*time.Second)

	got, elapsed := healthOn(t, h)
	if elapsed > healthFirstFillWait+time.Second {
		t.Fatalf("first health took %s with the connection held, want ≤ %s", elapsed, healthFirstFillWait)
	}
	if got.MigrationVersion != 0 || got.IngestLagSec != nil {
		t.Errorf("cold answer = %+v, want zero values while the fill is queued", got)
	}

	waitHealthRefresh(t, h)
	after, _ := healthOn(t, h)
	if after.MigrationVersion <= 0 {
		t.Errorf("after the fill landed: migrationVersion = %d, want > 0", after.MigrationVersion)
	}
}

// A stale snapshot is served as is once, and the refresh it starts brings the
// new numbers to the next probe.
func TestHealthSnapshotRefreshesAfterTTL(t *testing.T) {
	h := healthCacheHandler(t)
	if got, _ := healthOn(t, h); got.PluginDrift.Error != 0 {
		t.Fatalf("pluginDrift.error = %d on a clean store, want 0", got.PluginDrift.Error)
	}
	seedFinding(t, h.DB, "plugin:a@m|/p1", "plugin_enabled_not_installed", "error", "m", "")

	if got, _ := healthOn(t, h); got.PluginDrift.Error != 0 {
		t.Errorf("inside the TTL: pluginDrift.error = %d, want the cached 0", got.PluginDrift.Error)
	}
	if _, ok := h.healthSnapshot(time.Now().Add(healthSlowTTL)); !ok {
		t.Fatal("no snapshot after two probes")
	}
	waitHealthRefresh(t, h)
	if got, _ := healthOn(t, h); got.PluginDrift.Error != 1 {
		t.Errorf("after the refresh: pluginDrift.error = %d, want 1", got.PluginDrift.Error)
	}
}

// ingestLagSec is computed at answer time from the cached newest event, so it
// keeps counting between refreshes instead of freezing.
func TestHealthLagSince(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	if got := lagSince(nil, now); got != nil {
		t.Errorf("lagSince(nil) = %v, want nil", *got)
	}
	newest := now.Add(-90 * time.Second)
	if got := lagSince(&newest, now); got == nil || *got != 90 {
		t.Errorf("lagSince(-90s) = %v, want 90", got)
	}
	future := now.Add(time.Minute)
	if got := lagSince(&future, now); got == nil || *got != 0 {
		t.Errorf("lagSince(future) = %v, want clamped 0", got)
	}
}
