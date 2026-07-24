// Embedded terminal (fusion phase 15): GET /api/term/ws upgrades to a WebSocket
// bridged to an interactive PTY (internal/term). One PTY infra serves two
// surfaces — the workspace bottom dock (cwd = project path) and a task's
// worktree terminal (cwd = worktree_path).
//
// Security contract (normative, phase-15 spec):
//   - The endpoint is browser-originated, so the origin gate is STRICTER than
//     requireLocalOrigin: a MISSING Origin is rejected too (only a local http/https
//     Origin passes). This closes DNS-rebinding on a raw ws:// dial.
//   - cwd MUST EvalSymlinks to either a registered project path or a live task
//     worktree_path — anything else (e.g. /etc, a symlink escape) is 403.
//   - The PTY runs in its own process group; the bridge Closes the Session on
//     disconnect, which SIGHUPs (then SIGKILLs) the whole group — no orphans.
//
// This is NOT part of the frozen event bus (ws.go): a dedicated PTY socket is
// separate infrastructure and adds no message types to that bus.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/term"
)

// termMgr owns every live PTY (attached once at startup, mirroring toolMgr).
var termMgr *term.Manager

// AttachTermManager wires the PTY manager into GET /api/term/ws. Nil until then;
// the handler 503s when unattached (serve --no-ingest style degradation).
func AttachTermManager(m *term.Manager) { termMgr = m }

const (
	// termWriteTimeout bounds a single frame write to a slow client.
	termWriteTimeout = 10 * time.Second
	// termReadLimit caps a control (text) frame; PTY input is small keystrokes.
	termReadLimit = 1 << 20
)

// term handles GET /api/term/ws?cwd=<abs path>. Security gates run BEFORE the
// upgrade so a rejected request gets a plain JSON status, not a half-open socket.
func (h *Handler) term(w http.ResponseWriter, r *http.Request) {
	if termMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "terminal service not attached"})
		return
	}
	// Strict origin gate: browser-originated ⇒ a local Origin is REQUIRED.
	if !isStrictLocalOrigin(r.Header.Get("Origin")) {
		writeJSONStatus(w, http.StatusForbidden,
			map[string]string{"error": "cross-origin or origin-less terminal upgrade rejected"})
		return
	}
	reqCwd := r.URL.Query().Get("cwd")
	cwd, ok := h.resolveTermCwd(reqCwd)
	if !ok {
		writeJSONStatus(w, http.StatusForbidden,
			map[string]string{"error": "cwd is not a registered project or live worktree path"})
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// We enforce origin ourselves above (stricter than coder's check), so
		// the library gate is opened; the loopback bind is the outer fence.
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("warn: term: accept: %v", err)
		return
	}
	c.SetReadLimit(termReadLimit)
	defer c.Close(websocket.StatusInternalError, "server error")

	sess, err := termMgr.Start(cwd, 0, 0)
	if err != nil {
		if errors.Is(err, term.ErrTooManySessions) {
			// 1013 Try Again Later — the browser surfaces a "too many terminals".
			c.Close(websocket.StatusCode(1013), "too many terminals")
			return
		}
		log.Printf("warn: term: start pty (%s): %v", cwd, err)
		c.Close(websocket.StatusInternalError, "cannot start terminal")
		return
	}
	bridgeTermSession(r.Context(), c, sess)
}

// bridgeTermSession pumps bytes both ways until either side closes, then tears
// the PTY down (process-group kill). Split into its own function so the pump
// logic is unit-testable with an in-memory session.
func bridgeTermSession(ctx context.Context, c *websocket.Conn, sess termSession) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Closing the PTY on the way out is the no-orphan guarantee.
	defer sess.Close()

	// PTY → WS: stream master output as binary frames.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				wctx, wcancel := context.WithTimeout(ctx, termWriteTimeout)
				werr := c.Write(wctx, websocket.MessageBinary, buf[:n])
				wcancel()
				if werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// WS → PTY: binary frames are raw keystrokes; text frames are JSON control.
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := sess.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			applyTermControl(sess, data)
		}
	}
}

// termSession is the slice of *term.Session the bridge needs — a seam so the
// pump can be tested without a real PTY.
type termSession interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Resize(cols, rows uint16) error
	Close()
}

// termControl is the JSON text-frame control protocol. Only resize in v1.
type termControl struct {
	Resize *term.Resize `json:"resize,omitempty"`
}

func applyTermControl(sess termSession, data []byte) {
	var ctl termControl
	if err := json.Unmarshal(data, &ctl); err != nil {
		return // ignore malformed control frames; keystrokes are unaffected
	}
	if ctl.Resize != nil {
		_ = sess.Resize(ctl.Resize.Cols, ctl.Resize.Rows)
	}
}

// resolveTermCwd validates a requested cwd against the allow-list: it must
// EvalSymlinks to a registered project path or a live task worktree_path.
// Returns the RESOLVED absolute path (what the PTY should chdir into) and ok.
func (h *Handler) resolveTermCwd(reqCwd string) (string, bool) {
	if reqCwd == "" || !filepath.IsAbs(reqCwd) {
		return "", false
	}
	// Resolve symlinks so /etc/../<project> or a symlink INTO an allowed dir
	// can't smuggle a path past the string compare — and so a symlink escape OUT
	// of an allowed dir fails too.
	real, err := filepath.EvalSymlinks(reqCwd)
	if err != nil {
		return "", false
	}
	for _, allowed := range h.termAllowedRoots() {
		resolvedAllowed, err := filepath.EvalSymlinks(allowed)
		if err != nil {
			continue // path vanished (stale worktree row) — skip, don't match
		}
		if real == resolvedAllowed {
			return real, true
		}
	}
	return "", false
}

// termAllowedRoots is every path a terminal may open in: all registered project
// roots plus every live task worktree_path (a task currently holding a worktree).
func (h *Handler) termAllowedRoots() []string {
	var roots []string
	roots = append(roots, h.scanColumn(`SELECT path FROM projects WHERE path IS NOT NULL AND path <> ''`)...)
	roots = append(roots, h.scanColumn(`SELECT worktree_path FROM tasks WHERE worktree_path IS NOT NULL AND worktree_path <> ''`)...)
	return roots
}

// scanColumn runs a single-column string query and returns the non-null rows.
func (h *Handler) scanColumn(query string) []string {
	rows, err := h.DB.Query(query)
	if err != nil {
		log.Printf("warn: term: allow-list query: %v", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			continue
		}
		if v.Valid && v.String != "" {
			out = append(out, v.String)
		}
	}
	return out
}

// isStrictLocalOrigin requires a present, parseable, http/https, localhost Origin.
// Unlike isLocalOrigin (which lets an ABSENT origin through for the hook shim),
// an empty Origin is rejected: the terminal is only ever opened from the SPA.
func isStrictLocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}
