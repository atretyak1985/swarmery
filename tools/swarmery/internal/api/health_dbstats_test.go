package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// getHealthPool GETs /api/health and decodes its db object, failing the test
// when the key is absent or a counter is missing.
func getHealthPool(t *testing.T, url string) healthDBPool {
	t.Helper()
	resp, err := http.Get(url + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, body)
	}
	var keys struct {
		DB map[string]json.RawMessage `json:"db"`
	}
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if keys.DB == nil {
		t.Fatalf("health has no db object: %s", body)
	}
	for _, k := range []string{"openConnections", "inUse", "waitCount", "waitDurationMs"} {
		if _, ok := keys.DB[k]; !ok {
			t.Errorf("health db is missing %q: %s", k, body)
		}
	}
	var got struct {
		DB healthDBPool `json:"db"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode db: %v", err)
	}
	return got.DB
}

// TestHealthDBPoolStats pins the db pool counters on /api/health: they are
// present, they describe the store's single connection, and a caller that had
// to queue for that connection shows up in waitCount / waitDurationMs.
func TestHealthDBPoolStats(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "health-pool.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	idle := getHealthPool(t, srv.URL)
	if idle.OpenConnections > 1 {
		t.Errorf("openConnections = %d, want at most 1 (store.Open pins one connection)", idle.OpenConnections)
	}
	if idle.InUse != 0 {
		t.Errorf("inUse = %d on an idle pool, want 0 (the snapshot is taken before health's own queries)", idle.InUse)
	}

	// Hold the only connection, make a second caller queue behind it, then
	// release: the pool must have counted that wait.
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("check out the connection: %v", err)
	}
	before := db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		var one int
		done <- db.QueryRowContext(ctx, `SELECT 1`).Scan(&one)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for db.Stats().WaitCount == before {
		if time.Now().After(deadline) {
			conn.Close()
			t.Fatal("the second caller never queued for the held connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
	held := dbPoolStats(db.Stats())
	if held.InUse != 1 {
		t.Errorf("inUse while held = %d, want 1", held.InUse)
	}
	time.Sleep(60 * time.Millisecond)
	if err := conn.Close(); err != nil {
		t.Fatalf("release the connection: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("queued query: %v", err)
	}

	after := getHealthPool(t, srv.URL)
	if after.WaitCount < before+1 {
		t.Errorf("waitCount = %d, want >= %d after a caller queued", after.WaitCount, before+1)
	}
	if after.WaitDurationMs < 50 {
		t.Errorf("waitDurationMs = %d, want >= 50 (the caller waited ~60ms)", after.WaitDurationMs)
	}
}

// TestDBPoolStatsMapping pins the field mapping, durations in milliseconds.
func TestDBPoolStatsMapping(t *testing.T) {
	got := dbPoolStats(sql.DBStats{
		OpenConnections: 1,
		InUse:           1,
		WaitCount:       42,
		WaitDuration:    1500 * time.Millisecond,
	})
	want := healthDBPool{OpenConnections: 1, InUse: 1, WaitCount: 42, WaitDurationMs: 1500}
	if got != want {
		t.Errorf("dbPoolStats = %+v, want %+v", got, want)
	}
}
