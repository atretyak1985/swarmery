package api

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// holdStoreConn checks out the store's only connection (store.Open:
// SetMaxOpenConns(1)) and gives it back after d, from another goroutine — the
// shape of the live stall, where a long dashboard read or a busy writer keeps
// POST /api/link/events queued.
func holdStoreConn(t *testing.T, db *sql.DB, d time.Duration) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold the store connection: %v", err)
	}
	go func() {
		time.Sleep(d)
		conn.Close()
	}()
}

// postLink sends one batch with a client-side timeout, like the mod's 5 s
// budget. It returns the status (0 when the client gave up), the
// Retry-After header and how long the call took.
func (e *linkTestEnv) postLink(t *testing.T, body string, timeout time.Duration) (int, string, time.Duration) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/api/link/events", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return 0, "", elapsed
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("Retry-After"), elapsed
}

// TestLinkEventsDoNotHangBehindLongHolder is the regression test for the
// link-ingest stall (reports/link-ingest-stall.md, H1): with the store's
// connection held for 6 s, POST /api/link/events answers inside the mod's 5 s
// budget — 202 if it got the connection, or the busy answer (504 +
// Retry-After) the mod retries. Never 503: the mod reads 503 as "link
// disabled" and turns the link off for the whole session.
func TestLinkEventsDoNotHangBehindLongHolder(t *testing.T) {
	e := newLinkTestEnv(t)
	holdStoreConn(t, e.db, 6*time.Second)

	code, retryAfter, elapsed := e.postLink(t, linkBatch("s-held", "/work/proj", 3), 6*time.Second)
	if elapsed >= 5*time.Second {
		t.Fatalf("POST took %s with the connection held (status %d), want < 5s — the mod gives up at 5s", elapsed, code)
	}
	switch code {
	case http.StatusAccepted:
	case http.StatusGatewayTimeout:
		if retryAfter != "2" {
			t.Errorf("busy answer Retry-After = %q, want \"2\"", retryAfter)
		}
	default:
		t.Fatalf("status = %d, want 202 or 504 (never 503: the mod turns the link off on 503)", code)
	}
}

// TestLinkEventsSurviveClientHangup: a batch queued behind a holder is not
// thrown away when the client gives up first. The daemon finishes it on its own
// budget, and the mod's resend of the same batch is all duplicates — nothing
// is stored twice (link_events UNIQUE(session_id, kind, at)).
func TestLinkEventsSurviveClientHangup(t *testing.T) {
	e := newLinkTestEnv(t)
	holdStoreConn(t, e.db, time.Second)

	batch := linkBatch("s-hangup", "/work/proj", 4)
	if code, _, _ := e.postLink(t, batch, 200*time.Millisecond); code != 0 {
		t.Fatalf("the client was meant to give up before the holder let go, got status %d", code)
	}

	deadline := time.Now().Add(4 * time.Second)
	var n int
	for {
		if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_events`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if n != 4 {
		t.Fatalf("link_events = %d after the client hung up, want the batch's 4 (the daemon dropped it)", n)
	}

	code, body := e.do(t, http.MethodPost, "/api/link/events", e.token, batch)
	if code != http.StatusAccepted {
		t.Fatalf("resend: status = %d body %s, want 202", code, body)
	}
	if r := decodeLinkResult(t, body); r.Accepted != 0 || r.Duplicates != 4 {
		t.Errorf("resend = %+v, want 4 duplicates and nothing new", r)
	}
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM link_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("link_events = %d after the resend, want still 4", n)
	}
}
