package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

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

	mgr := term.NewManager(term.Config{Shell: "/bin/cat", MaxSessions: 5})
	AttachTermManager(mgr)
	t.Cleanup(func() { mgr.CloseAll(); AttachTermManager(nil) })

	h, err := NewServer(db, false)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	srv = httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, projectDir, worktreeDir
}

func termWSURL(srv *httptest.Server, cwd string) string {
	base := strings.Replace(srv.URL, "http://", "ws://", 1)
	return base + "/api/term/ws?cwd=" + url.QueryEscape(cwd)
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
				HTTPHeader: localOrigin(srv),
			})
			if err != nil {
				t.Fatalf("dial should have upgraded for %s: %v", tc.cwd, err)
			}
			defer c.Close(websocket.StatusNormalClosure, "")

			// /bin/cat echoes: a binary write comes back as a binary frame.
			if err := c.Write(ctx, websocket.MessageBinary, []byte("ping\n")); err != nil {
				t.Fatalf("write: %v", err)
			}
			typ, data, err := c.Read(ctx)
			if err != nil {
				t.Fatalf("read echo: %v", err)
			}
			if typ != websocket.MessageBinary || !strings.Contains(string(data), "ping") {
				t.Errorf("echo = %q (type %v), want binary containing 'ping'", data, typ)
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
				HTTPHeader: localOrigin(srv),
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
// rejected too. Only a local Origin upgrades.
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
		{"local origin accepted", srv.URL, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			opts := &websocket.DialOptions{HTTPHeader: http.Header{}}
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
			HTTPHeader: localOrigin(srv),
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
		HTTPHeader: localOrigin(srv),
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
		HTTPHeader: localOrigin(srv),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

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
	typ, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read after resize: %v", err)
	}
	if typ != websocket.MessageBinary || !strings.Contains(string(data), "after-resize") {
		t.Errorf("echo after resize = %q (type %v), want binary containing 'after-resize'", data, typ)
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
		HTTPHeader: localOrigin(srv),
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
