package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/linksig"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/store"
	"github.com/atretyak1985/swarmery/tools/swarmery/internal/term"
)

// termTestServer builds an httptest server with a fresh DB and a term.Manager
// whose "shell" is /bin/cat — a real but harmless process on a real PTY, so the
// accept path is exercised end-to-end without spawning an interactive shell.
// Returns the server plus the two allow-listed paths (a project root, a live
// task worktree) it seeded.
func termTestServer(t *testing.T) (srv *httptest.Server, projectDir, worktreeDir string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	// Two real directories on disk so EvalSymlinks resolves them.
	projectDir = t.TempDir()
	worktreeDir = t.TempDir()

	if _, err := db.Exec(
		`INSERT INTO projects (id, path, slug, first_seen) VALUES (1, ?, 'proj', ?)`,
		projectDir, "2026-07-24T00:00:00Z"); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// A live dispatched task holding a worktree (worktree_path set).
	if _, err := db.Exec(
		`INSERT INTO tasks (id, project_id, title, prompt, status, source, external_id, created_at, started_at, worktree_path)
		 VALUES (10, 1, 'wt task', 'do it', 'running', 'queue', 'T-abc', ?, ?, ?)`,
		"2026-07-24T00:00:00Z", "2026-07-24T00:00:00Z", worktreeDir); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	// The PTY starter always launches `<shell> -l`. A bare `/bin/cat -l` is a
	// VALID flag on BSD cat (macOS, advisory lock) but an INVALID option on GNU
	// cat (Linux CI), where it makes cat exit immediately — freeing the session
	// slot and breaking the concurrency-cap assertion. Use a tiny script that
	// ignores its login-flag arg and execs cat, so every platform gets the same
	// never-exiting, PTY-echoing child.
	shellPath := filepath.Join(t.TempDir(), "fakeshell.sh")
	if err := os.WriteFile(shellPath, []byte("#!/bin/sh\nexec cat\n"), 0o755); err != nil {
		t.Fatalf("write fake shell: %v", err)
	}
	mgr := term.NewManager(term.Config{Shell: shellPath, MaxSessions: 5})
	AttachTermManager(mgr)
	t.Cleanup(func() { mgr.CloseAll(); AttachTermManager(nil) })

	// The terminal upgrade needs a dashboard session, verified under the
	// link token (session.go); termSessionProtocols mints one under it.
	attachHookToken(t)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, projectDir, worktreeDir
}

// termSessionProtocols is the subprotocol list a dashboard terminal offers:
// swarmery.v1 and a fresh session under the attached link token.
func termSessionProtocols() []string {
	s, err := linksig.MintSession(hookToken(), time.Now())
	if err != nil {
		panic(err)
	}
	return sessionProtocols(s)
}

func termWSURL(srv *httptest.Server, cwd string) string {
	base := strings.Replace(srv.URL, "http://", "ws://", 1)
	return base + "/api/term/ws?cwd=" + url.QueryEscape(cwd)
}

func termAttachWSURL(srv *httptest.Server, id string) string {
	base := strings.Replace(srv.URL, "http://", "ws://", 1)
	return base + "/api/term/ws?attach=" + url.QueryEscape(id)
}

// readTermHello reads the mandatory first frame of every successful upgrade
// (phase 4: {"type":"hello","id":"<id>"}) and returns the id it names.
func readTermHello(t *testing.T, ctx context.Context, c *websocket.Conn) string {
	t.Helper()
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("first frame = (%v, %q), want the text hello frame", typ, data)
	}
	var hello struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(data, &hello); err != nil || hello.Type != "hello" || hello.ID == "" {
		t.Fatalf("first frame = %q, want a hello frame carrying an id", data)
	}
	return hello.ID
}

// readTermBinary skips the hello frame (if not already consumed) and any
// other leading text frames, returning the first binary frame — the shape
// every pre-phase-4 "write a byte, read the echo" test wants.
func readTermBinary(t *testing.T, ctx context.Context, c *websocket.Conn) []byte {
	t.Helper()
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if typ == websocket.MessageBinary {
			return data
		}
	}
}

// localOrigin returns the httptest server's own origin (a 127.0.0.1 URL), which
// passes the strict local-origin gate.
func localOrigin(srv *httptest.Server) http.Header {
	h := http.Header{}
	h.Set("Origin", srv.URL) // http://127.0.0.1:PORT
	return h
}

// TestTermCwdAllowList is the security matrix: only a registered project path or
// a live worktree_path may open a terminal; everything else is 403 BEFORE the
// upgrade (a rejected dial returns a non-101 status, surfaced by websocket.Dial).
func TestTermCwdAllowList(t *testing.T) {
	srv, projectDir, worktreeDir := termTestServer(t)

	// A symlink that escapes to /etc — must be rejected even though it "starts"
	// as a fresh path, because EvalSymlinks follows it out of the allow-list.
	escape := filepath.Join(t.TempDir(), "escape")
	if err := os.Symlink("/etc", escape); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	accept := []struct {
		name string
		cwd  string
	}{
		{"project root", projectDir},
		{"live worktree", worktreeDir},
	}
	for _, tc := range accept {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, termWSURL(srv, tc.cwd), &websocket.DialOptions{
				HTTPHeader:   localOrigin(srv),
				Subprotocols: termSessionProtocols(),
			})
			if err != nil {
				t.Fatalf("dial should have upgraded for %s: %v", tc.cwd, err)
			}
			defer c.Close(websocket.StatusNormalClosure, "")

			readTermHello(t, ctx, c)
			// /bin/cat echoes: a binary write comes back as a binary frame.
			if err := c.Write(ctx, websocket.MessageBinary, []byte("ping\n")); err != nil {
				t.Fatalf("write: %v", err)
			}
			data := readTermBinary(t, ctx, c)
			if !strings.Contains(string(data), "ping") {
				t.Errorf("echo = %q, want binary containing 'ping'", data)
			}
		})
	}

	reject := []struct {
		name string
		cwd  string
	}{
		{"etc", "/etc"},
		{"symlink escape", escape},
		{"unregistered abs", t.TempDir()},
		{"relative", "relative/path"},
		{"empty", ""},
	}
	for _, tc := range reject {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, resp, err := websocket.Dial(ctx, termWSURL(srv, tc.cwd), &websocket.DialOptions{
				HTTPHeader:   localOrigin(srv),
				Subprotocols: termSessionProtocols(),
			})
			if err == nil {
				c.Close(websocket.StatusNormalClosure, "")
				t.Fatalf("dial should have been rejected for cwd=%q", tc.cwd)
			}
			if resp == nil || resp.StatusCode != http.StatusForbidden {
				got := 0
				if resp != nil {
					got = resp.StatusCode
				}
				t.Errorf("cwd=%q rejected with status %d, want 403", tc.cwd, got)
			}
		})
	}
}

// TestTermOriginGate proves the STRICT origin rule: unlike requireLocalOrigin, a
// MISSING Origin is rejected (browser-only endpoint), and a foreign Origin is
// rejected too — localhost on a port the daemon does not serve included, since
// any local dev server could otherwise open a shell. Only the daemon's own
// Origin upgrades.
func TestTermOriginGate(t *testing.T) {
	srv, projectDir, _ := termTestServer(t)

	cases := []struct {
		name   string
		origin string // "" ⇒ no Origin header sent
		ok     bool
	}{
		{"no origin rejected", "", false},
		{"foreign origin rejected", "https://evil.example.com", false},
		{"non-http scheme rejected", "file://localhost", false},
		{"localhost on another port rejected", "http://localhost:" + otherPort(t, srv), false},
		{"127.0.0.1 on another port rejected", "http://127.0.0.1:" + otherPort(t, srv), false},
		{"local origin accepted", srv.URL, true},
		{"own origin by name accepted", "http://localhost:" + serverPort(t, srv), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Every case carries a valid session: the Origin rule is what
			// is under test (the session gate: session_test.go).
			opts := &websocket.DialOptions{HTTPHeader: http.Header{}, Subprotocols: termSessionProtocols()}
			if tc.origin != "" {
				opts.HTTPHeader.Set("Origin", tc.origin)
			}
			c, resp, err := websocket.Dial(ctx, termWSURL(srv, projectDir), opts)
			if tc.ok {
				if err != nil {
					t.Fatalf("local origin should upgrade: %v", err)
				}
				c.Close(websocket.StatusNormalClosure, "")
				return
			}
			if err == nil {
				c.Close(websocket.StatusNormalClosure, "")
				t.Fatalf("origin %q should have been rejected", tc.origin)
			}
			if resp == nil || resp.StatusCode != http.StatusForbidden {
				got := 0
				if resp != nil {
					got = resp.StatusCode
				}
				t.Errorf("origin %q rejected with status %d, want 403", tc.origin, got)
			}
		})
	}
}

// TestTermConcurrencyCap: the 6th concurrent PTY is refused. The cap lives in
// term.Manager; over the socket it surfaces as an immediate close (code 1013),
// so the dial succeeds but the connection is closed before any data.
func TestTermConcurrencyCap(t *testing.T) {
	srv, projectDir, _ := termTestServer(t) // MaxSessions = 5

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var open []*websocket.Conn
	t.Cleanup(func() {
		for _, c := range open {
			c.Close(websocket.StatusNormalClosure, "")
		}
	})
	for i := 0; i < 5; i++ {
		c, _, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
			HTTPHeader:   localOrigin(srv),
			Subprotocols: termSessionProtocols(),
		})
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		// Force the PTY to actually start by writing a byte, then confirm it's live.
		if err := c.Write(ctx, websocket.MessageBinary, []byte("x")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if _, _, err := c.Read(ctx); err != nil {
			t.Fatalf("read %d (session should be live): %v", i, err)
		}
		open = append(open, c)
	}

	// The 6th upgrades (origin+cwd pass) but the PTY start is refused, so the
	// server closes the socket; the first Read returns a close error.
	c6, _, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err != nil {
		t.Fatalf("6th dial (upgrade should still succeed): %v", err)
	}
	defer c6.Close(websocket.StatusNormalClosure, "")
	if _, _, err := c6.Read(ctx); err == nil {
		t.Fatal("6th terminal should have been closed by the cap, but Read succeeded")
	} else if status := websocket.CloseStatus(err); status != websocket.StatusCode(1013) {
		t.Errorf("6th close status = %v, want 1013 (try again later)", status)
	}
}

// TestTermResizeControl: a JSON text frame is treated as control (resize), not
// keystrokes — it must NOT be echoed by the PTY, and the session stays live.
// A malformed text frame is ignored without tearing the socket down.
func TestTermResizeControl(t *testing.T) {
	srv, projectDir, _ := termTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	readTermHello(t, ctx, c)

	// Control frame (resize) + a malformed control frame — neither is echoed.
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"resize":{"cols":120,"rows":40}}`)); err != nil {
		t.Fatalf("resize write: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageText, []byte(`{not json`)); err != nil {
		t.Fatalf("malformed write: %v", err)
	}

	// The session is still live: a binary keystroke still round-trips via cat.
	if err := c.Write(ctx, websocket.MessageBinary, []byte("after-resize\n")); err != nil {
		t.Fatalf("post-resize write: %v", err)
	}
	data := readTermBinary(t, ctx, c)
	if !strings.Contains(string(data), "after-resize") {
		t.Errorf("echo after resize = %q, want binary containing 'after-resize'", data)
	}
}

// TestTermServiceUnattached: with no manager attached, the endpoint 503s (the
// origin/cwd checks still run first, but an attached-nil short-circuits to 503).
func TestTermServiceUnattached(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	AttachTermManager(nil)

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, termWSURL(srv, "/tmp"), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err == nil {
		t.Fatal("dial should fail when the service is unattached")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		got := 0
		if resp != nil {
			got = resp.StatusCode
		}
		t.Errorf("unattached status = %d, want 503", got)
	}
}

// TestTermRefusedOnAnExposedListener (D7): a browser cannot sign a WebSocket,
// and on a listener beyond loopback an Origin header proves nothing — any
// client on the network can send the daemon's own. The terminal is therefore
// loopback-only: refused before the upgrade on an exposed listener — even for
// a signed request, which is what passes the exposed-listener gate
// (requireSignedWhenExposed) to reach the terminal's own refusal.
func TestTermRefusedOnAnExposedListener(t *testing.T) {
	srv, projectDir, _ := termTestServer(t)
	key := hookToken()
	upgrade := func(r *http.Request) {
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	}

	// (a) a connection on a LAN address, carrying that address as its Origin
	const lan = "192.168.1.5:7777"
	target := "/api/term/ws?cwd=" + url.QueryEscape(projectDir)
	req := listenerRequest(t, http.MethodGet, target, lan, nil)
	req.Header.Set("Origin", "http://"+lan)
	req.Header.Set("Authorization", signedAuth(t, key, http.MethodGet, target, ""))
	upgrade(req)
	rec := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "loopback") {
		t.Errorf("LAN listener terminal upgrade = %d %s, want 403 naming loopback", rec.Code, rec.Body.String())
	}

	// (b) a non-loopback --bind: even a loopback connection is refused
	AttachExposedBind(true)
	t.Cleanup(func() { AttachExposedBind(false) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	signed := localOrigin(srv)
	signed.Set("Authorization", signedAuth(t, key, http.MethodGet, target, ""))
	c, resp, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{HTTPHeader: signed, Subprotocols: termSessionProtocols()})
	if err == nil {
		c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("exposed bind: the terminal upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("exposed bind: resp %v, want 403", resp)
	}

	// (c) loopback-only again: the terminal upgrades as before
	AttachExposedBind(false)
	c, _, err = websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{HTTPHeader: localOrigin(srv), Subprotocols: termSessionProtocols()})
	if err != nil {
		t.Fatalf("loopback daemon: terminal should upgrade: %v", err)
	}
	c.Close(websocket.StatusNormalClosure, "")
}

// TestTermCloseSaysWhy: when the PTY side ends the socket, the close frame
// carries the reason the tab shows (web/src/terminal/closeReason.ts): the
// shell exiting is 1000 "shell-exited", the daemon stopping 1001
// "daemon-stopping".
func TestTermCloseSaysWhy(t *testing.T) {
	dial := func(t *testing.T, srv *httptest.Server, dir string, ctx context.Context) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, termWSURL(srv, dir), &websocket.DialOptions{
			HTTPHeader:   localOrigin(srv),
			Subprotocols: termSessionProtocols(),
		})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		t.Cleanup(func() { c.Close(websocket.StatusNormalClosure, "") })
		// Round-trip a byte so the PTY is up before the test ends it.
		if err := c.Write(ctx, websocket.MessageBinary, []byte("x")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, _, err := c.Read(ctx); err != nil {
			t.Fatalf("first read: %v", err)
		}
		return c
	}
	// readClose drains output until the server's close frame arrives.
	readClose := func(t *testing.T, c *websocket.Conn, ctx context.Context) websocket.CloseError {
		t.Helper()
		for {
			_, _, err := c.Read(ctx)
			if err == nil {
				continue
			}
			var ce websocket.CloseError
			if !errors.As(err, &ce) {
				t.Fatalf("read ended without a close frame: %v", err)
			}
			return ce
		}
	}

	t.Run("shell exits", func(t *testing.T) {
		srv, projectDir, _ := termTestServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c := dial(t, srv, projectDir, ctx)
		// Ctrl-D at the start of a line is EOF for cat on a PTY: the "shell" exits.
		if err := c.Write(ctx, websocket.MessageBinary, []byte("\n\x04")); err != nil {
			t.Fatalf("write EOF: %v", err)
		}
		ce := readClose(t, c, ctx)
		if ce.Code != websocket.StatusNormalClosure || ce.Reason != termCloseShellExited {
			t.Errorf("close = %d %q, want 1000 %q", ce.Code, ce.Reason, termCloseShellExited)
		}
	})

	t.Run("daemon stops", func(t *testing.T) {
		srv, projectDir, _ := termTestServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		c := dial(t, srv, projectDir, ctx)
		go termMgr.CloseAll()
		ce := readClose(t, c, ctx)
		if ce.Code != websocket.StatusGoingAway || ce.Reason != termCloseDaemonStopping {
			t.Errorf("close = %d %q, want 1001 %q", ce.Code, ce.Reason, termCloseDaemonStopping)
		}
	})
}

func TestTermCloseFrame(t *testing.T) {
	cases := []struct {
		in     term.CloseReason
		code   websocket.StatusCode
		reason string
	}{
		{term.ReasonNone, websocket.StatusNormalClosure, termCloseShellExited},
		{term.ReasonIdle, websocket.StatusNormalClosure, termCloseIdle},
		{term.ReasonShutdown, websocket.StatusGoingAway, termCloseDaemonStopping},
		{term.ReasonDetachExpired, websocket.StatusNormalClosure, termCloseDetachExpired},
	}
	for _, tc := range cases {
		code, reason := termCloseFrame(tc.in)
		if code != tc.code || reason != tc.reason {
			t.Errorf("termCloseFrame(%q) = %d %q, want %d %q", tc.in, code, reason, tc.code, tc.reason)
		}
	}
}

// TestTermAttachUnknownIDClosesNotFound: ?attach=<id> for a session that
// isn't live closes with the 4404 private-use code and reason "not-found" —
// the client shows "Термінал уже закрито" + a Start button (closeReason.ts).
func TestTermAttachUnknownIDClosesNotFound(t *testing.T) {
	srv, _, _ := termTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, termAttachWSURL(srv, "no-such-session"), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err != nil {
		t.Fatalf("dial (upgrade should still succeed): %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatal("attach to an unknown id should have closed the socket")
	} else if status := websocket.CloseStatus(err); status != websocket.StatusCode(4404) {
		t.Errorf("close status = %v, want 4404", status)
	}
}

// TestTermReattachReplaysSnapshotThenLiveOutput is the end-to-end reattach
// path: drop the socket (which Detaches, not Closes), write more through the
// PTY while detached, then ?attach=<id> and see both the detach-grace replay
// and new live output, in order.
func TestTermReattachReplaysSnapshotThenLiveOutput(t *testing.T) {
	srv, projectDir, _ := termTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	id := readTermHello(t, ctx, c)
	if err := c.Write(ctx, websocket.MessageBinary, []byte("before-detach\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(string(readTermBinary(t, ctx, c)), "before-detach") {
		t.Fatal("the first connection never saw its own echo")
	}
	// Drop the socket: this Detaches the session, it does not Close it.
	c.Close(websocket.StatusNormalClosure, "")

	// Wait for the session to actually report detached before reattaching —
	// Detach runs on the server after the client-side Close returns.
	waitForSessionDetached(t, srv, id)

	c2, _, err := websocket.Dial(ctx, termAttachWSURL(srv, id), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err != nil {
		t.Fatalf("reattach dial: %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")
	if got := readTermHello(t, ctx, c2); got != id {
		t.Errorf("reattach hello id = %q, want %q", got, id)
	}
	if !strings.Contains(string(readTermBinary(t, ctx, c2)), "before-detach") {
		t.Fatal("reattach never replayed the scrollback accumulated before it")
	}
}

// waitForSessionDetached polls GET /api/term/sessions until id reports
// Attached=false, or fails the test after 2s.
func waitForSessionDetached(t *testing.T, srv *httptest.Server, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/term/sessions", nil)
		resp, err := srv.Client().Do(req)
		if err == nil {
			var sessions []termSessionDTO
			_ = json.NewDecoder(resp.Body).Decode(&sessions)
			resp.Body.Close()
			for _, s := range sessions {
				if s.ID == id && !s.Attached {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session %s never reported detached", id)
}

// TestListTermSessionsFiltersByCwdPrefixAndNeedsLocalOrigin covers the
// GET /api/term/sessions contract: cwdPrefix narrows the list, and a foreign
// Origin is rejected (requireLocalOrigin, same fence as every other
// daemon-only list).
func TestListTermSessionsFiltersByCwdPrefixAndNeedsLocalOrigin(t *testing.T) {
	srv, projectDir, worktreeDir := termTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	dialOne := func(cwd string) {
		c, _, err := websocket.Dial(ctx, termWSURL(srv, cwd), &websocket.DialOptions{
			HTTPHeader:   localOrigin(srv),
			Subprotocols: termSessionProtocols(),
		})
		if err != nil {
			t.Fatalf("dial %s: %v", cwd, err)
		}
		readTermHello(t, ctx, c)
		t.Cleanup(func() { c.Close(websocket.StatusNormalClosure, "") })
	}
	dialOne(projectDir)
	dialOne(worktreeDir)

	get := func(cwdPrefix string) []termSessionDTO {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/term/sessions", nil)
		if cwdPrefix != "" {
			req.URL.RawQuery = "cwdPrefix=" + url.QueryEscape(cwdPrefix)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET /api/term/sessions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/term/sessions = %d, want 200", resp.StatusCode)
		}
		var out []termSessionDTO
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if all := get(""); len(all) != 2 {
		t.Fatalf("unfiltered list = %d sessions, want 2", len(all))
	}
	// Info.Cwd is the EvalSymlinks'd path (resolveTermCwd), which on macOS can
	// differ from t.TempDir()'s own return value (/var vs /private/var).
	resolvedProjectDir, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", projectDir, err)
	}
	if only := get(resolvedProjectDir); len(only) != 1 || only[0].Cwd != resolvedProjectDir {
		t.Fatalf("cwdPrefix=%s list = %+v, want exactly the project session", resolvedProjectDir, only)
	}

	// A foreign Origin is rejected (requireLocalOrigin).
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/term/sessions", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET with foreign origin: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET with foreign origin = %d, want 403", resp.StatusCode)
	}
}

// TestDeleteTermSessionClosesItAndNeedsLocalOrigin covers DELETE
// /api/term/sessions/{id}: it closes an unknown-to-the-caller id (ok, from a
// prior connection) outright, and a foreign Origin is rejected before it ever
// reaches the Manager.
func TestDeleteTermSessionClosesItAndNeedsLocalOrigin(t *testing.T) {
	srv, projectDir, _ := termTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c, _, err := websocket.Dial(ctx, termWSURL(srv, projectDir), &websocket.DialOptions{
		HTTPHeader:   localOrigin(srv),
		Subprotocols: termSessionProtocols(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	id := readTermHello(t, ctx, c)
	t.Cleanup(func() { c.Close(websocket.StatusNormalClosure, "") })

	// A foreign Origin is rejected before it ever reaches the Manager.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/term/sessions/"+id, nil)
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE with foreign origin: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("DELETE with foreign origin = %d, want 403", resp.StatusCode)
	}

	// From the daemon's own origin, DELETE closes the session outright.
	req2, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/term/sessions/"+id, nil)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("DELETE = %d, want 200", resp2.StatusCode)
	}
	deadline := time.Now().Add(2 * time.Second)
	for termMgr.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if termMgr.Count() != 0 {
		t.Fatal("DELETE did not close the session")
	}

	// An unknown id is a 404.
	req3, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/term/sessions/"+id, nil)
	resp3, err := srv.Client().Do(req3)
	if err != nil {
		t.Fatalf("DELETE (already gone): %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusNotFound {
		t.Errorf("DELETE already-closed id = %d, want 404", resp3.StatusCode)
	}
}
