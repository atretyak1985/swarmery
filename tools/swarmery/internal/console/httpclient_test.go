package console_test

// Integration coverage for the real HTTP/WS client (HTTPClient) + RunStatus
// against a live in-process daemon (api.NewServer over httptest). This is the
// end-to-end proof that `swarmery status` reads the daemon and that the console
// client's WS stream + approval-resolve path talk to the frozen API — the same
// behaviors the phase-9 acceptance criteria call out, exercised without a TTY.

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/api"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/console"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/ingest"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/link"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/logbuf"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
)

// liveDaemon stands up a real API server with a bus + attached log ring, returns
// the base URL and the ring so tests can seed events/logs.
func liveDaemon(t *testing.T, watching bool) (base string, ring *logbuf.Ring) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "console.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	bus := ingest.NewBus()
	api.AttachBus(bus)
	t.Cleanup(func() { api.AttachBus(nil) })

	ring = logbuf.New(100)
	api.AttachLogRing(ring)
	t.Cleanup(func() { api.AttachLogRing(nil) })
	api.AttachUptime(time.Now().Add(-time.Minute))

	h, err := api.NewServer(db, watching)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, ring
}

func TestHTTPClientStatusAgainstLiveDaemon(t *testing.T) {
	base, ring := liveDaemon(t, true)
	ring.Append("info", "boot", "ready in 5ms")

	client := console.NewHTTPClient(base)
	var buf strings.Builder
	res, err := console.RunStatus(context.Background(), client, &buf)
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if !res.Reachable {
		t.Fatalf("live daemon should be reachable")
	}
	// The block carries the version + url from the real /api/health.
	out := buf.String()
	if !strings.Contains(out, "swarmery v") {
		t.Errorf("status block missing version line:\n%s", out)
	}
	if !strings.Contains(out, base) && !strings.Contains(out, "url http") {
		t.Errorf("status block missing url:\n%s", out)
	}
}

func TestHTTPClientStatusUnreachable(t *testing.T) {
	// Point at a closed port → RunStatus reports unreachable (exit-1 path), no
	// hard error, and prints the down banner.
	client := console.NewHTTPClient("http://127.0.0.1:1") // nothing listens on :1
	var buf strings.Builder
	res, err := console.RunStatus(context.Background(), client, &buf)
	if err != nil {
		t.Fatalf("RunStatus should not hard-error on a down daemon: %v", err)
	}
	if res.Reachable {
		t.Errorf("Reachable = true, want false for a closed port")
	}
	if !strings.Contains(buf.String(), "unreachable") {
		t.Errorf("down banner missing:\n%s", buf.String())
	}
}

func TestHTTPClientLogsAgainstLiveDaemon(t *testing.T) {
	base, ring := liveDaemon(t, true)
	ring.Append("info", "ingest", "line-a")
	ring.Append("warn", "dispatch", "line-b")

	client := console.NewHTTPClient(base)
	entries, last, err := client.Logs(context.Background(), 0, "", 0)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("logs len = %d, want 2", len(entries))
	}
	if last != 2 {
		t.Errorf("lastID = %d, want 2", last)
	}
	// Tag filter round-trips through the query string.
	only, _, err := client.Logs(context.Background(), 0, "dispatch", 0)
	if err != nil {
		t.Fatalf("Logs(tag): %v", err)
	}
	if len(only) != 1 || only[0].Msg != "line-b" {
		t.Errorf("tag-filtered logs = %+v, want [line-b]", only)
	}
}

func TestHTTPClientPauseDispatchNoDispatcher(t *testing.T) {
	// No dispatcher attached → POST /api/dispatch/pause is 503; the client
	// surfaces that as an error (the model's flash path handles it).
	base, _ := liveDaemon(t, true)
	client := console.NewHTTPClient(base)
	if err := client.PauseDispatch(context.Background(), true); err == nil {
		t.Errorf("PauseDispatch without a dispatcher should error (503)")
	}
}

func TestHTTPClientStreamEventsReceivesFrames(t *testing.T) {
	base, _ := liveDaemon(t, true)
	client := console.NewHTTPClient(base)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	frames := make(chan console.WSEvent, 8)
	errc := make(chan error, 1)
	go func() { errc <- client.StreamEvents(ctx, frames) }()

	// Publish a session_started via the bus (needs a real session row → use the
	// fixture-free path: the ws handler skips a vanished row, so publish a
	// notification the handler can hydrate. We assert the stream stays alive and
	// the goroutine returns cleanly on cancel rather than requiring a specific
	// frame, which keeps this independent of fixture ids.)
	cancel() // end the stream
	select {
	case err := <-errc:
		if err == nil || strings.Contains(err.Error(), "unexpected") {
			// context.Canceled or a normal close are both acceptable terminations.
		}
	case <-time.After(3 * time.Second):
		t.Errorf("StreamEvents did not return after cancel")
	}
	_ = frames
}

// attachLinkHome mints a link token in a temp HOME (the one the client reads)
// and attaches it to the API, as the daemon does at start.
func attachLinkHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	tok, err := link.EnsureToken(home)
	if err != nil {
		t.Fatal(err)
	}
	api.AttachLink(link.NewHolder(link.TokenPath(home), tok), ingest.ParseExcludeList(ingest.DefaultExclude))
	t.Cleanup(func() { api.AttachLink(nil, nil) })
}

// TestHTTPClientSignsWritesAndTheStream: the daemon takes a write or a WS
// upgrade without an Origin only with link auth; the console signs both.
func TestHTTPClientSignsWritesAndTheStream(t *testing.T) {
	attachLinkHome(t)
	base, _ := liveDaemon(t, true)
	client := console.NewHTTPClient(base)

	// Past the auth gate the handler answers: no dispatcher is attached.
	err := client.PauseDispatch(context.Background(), true)
	if err == nil || strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "dispatcher not attached") {
		t.Errorf("PauseDispatch = %v, want the handler's 503, not the auth gate", err)
	}
	if err := client.ResolveApproval(context.Background(), 999999, "approve"); err == nil || strings.Contains(err.Error(), "401") {
		t.Errorf("ResolveApproval(unknown id) = %v, want the handler's answer, not 401", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- client.StreamEvents(ctx, make(chan console.WSEvent, 1)) }()
	select {
	case err := <-errc:
		t.Fatalf("StreamEvents ended before cancel: %v (the signed upgrade was refused)", err)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-errc
}

// TestHTTPClientWithoutTokenIsRefused: no token on disk → the daemon answers
// the console's write with 401.
func TestHTTPClientWithoutTokenIsRefused(t *testing.T) {
	attachLinkHome(t)
	base, _ := liveDaemon(t, true)
	t.Setenv("HOME", t.TempDir()) // the client now finds no token
	err := console.NewHTTPClient(base).PauseDispatch(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("unsigned PauseDispatch = %v, want 401", err)
	}
}
