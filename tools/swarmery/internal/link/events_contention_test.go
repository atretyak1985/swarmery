package link

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIngestDoesNotHangBehindLongHolder: with the store's single connection
// (store.Open: SetMaxOpenConns(1)) held for 6 s by someone else — a long
// dashboard read, a busy writer — Ingest answers within 5 s (the mod's request
// budget, plugins/swarmery-link/hooks/lib/client.ts REQUEST_TIMEOUT_MS) with
// the ErrBusy sentinel — never queued until the holder lets go. The caller's
// context has no deadline: the bound is Ingest's own.
func TestIngestDoesNotHangBehindLongHolder(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	held := make(chan struct{})
	go func() {
		conn, err := db.Conn(ctx)
		if err != nil {
			t.Errorf("hold the connection: %v", err)
			close(held)
			return
		}
		close(held)
		time.Sleep(6 * time.Second)
		conn.Close()
	}()
	<-held

	batch := []Event{{SessionID: "u-busy", Cwd: "/work/busy", Kind: KindTurnStart, At: "2026-10-19T10:00:00Z"}}
	start := time.Now()
	res, err := Ingest(ctx, db, nil, batch)
	elapsed := time.Since(start)
	if elapsed >= 5*time.Second {
		t.Fatalf("Ingest returned after %s with the connection held, want < 5s (err=%v)", elapsed, err)
	}
	if err == nil {
		t.Fatalf("Ingest succeeded (%+v) while the only connection was held", res)
	}
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Ingest error = %v, want the ErrBusy sentinel", err)
	}
}

// After the holder lets go inside the bound, the same batch is stored: the
// bound caps the wait, it does not refuse a contended pool outright.
func TestIngestWaitsOutAShortHolder(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		conn.Close()
	}()
	res := mustIngest(t, db, nil, []Event{{SessionID: "u-short", Cwd: "/work/short", Kind: KindTurnStart, At: "2026-10-19T10:00:00Z"}})
	if res.Accepted != 1 {
		t.Fatalf("Ingest after a short hold = %+v, want 1 accepted", res)
	}
}

// A caller whose own context ends first still gets context.Canceled, not
// ErrBusy: the sentinel means "the pool was busy for the whole bound", nothing
// else.
func TestIngestCallerCancelIsNotBusy(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = Ingest(ctx, db, nil, []Event{{SessionID: "u-c", Kind: KindTurnStart, At: "2026-10-19T10:00:00Z"}})
	if err == nil || errors.Is(err, ErrBusy) {
		t.Fatalf("Ingest with the caller's deadline gone = %v, want a context error that is not ErrBusy", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Ingest error = %v, want it to wrap the caller's context.DeadlineExceeded", err)
	}
}
