// Embedded terminal (fusion phase 15): GET /api/term/ws upgrades to a WebSocket
// bridged to an interactive PTY (internal/term). One PTY infra serves three
// surfaces — the workspace bottom dock (?cwd = project path), a task's
// worktree terminal (?cwd = worktree_path), and the connect flow's interactive
// login fallback (?account = account key: the PTY runs under that account's
// CLAUDE_CONFIG_DIR, in the operator's home).
//
// Security contract (normative, phase-15 spec):
//   - The endpoint is browser-originated, so the origin gate is STRICTER than
//     requireLocalOrigin: a MISSING Origin is rejected too (only a local http/https
//     Origin passes). This closes DNS-rebinding on a raw ws:// dial.
//   - cwd MUST EvalSymlinks to either a registered project path or a live task
//     worktree_path — anything else (e.g. /etc, a symlink escape) is 403.
//   - account MUST be a well-formed key (claudeacct.ValidKey) naming an account
//     the registry actually reports; it is NEVER used as a path, and it is
//     mutually exclusive with cwd. All of it runs BEFORE the upgrade.
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/atretyak1985/swarmery/tools/swarmery/internal/claudeacct"
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

// term handles GET /api/term/ws?cwd=<abs path> and GET /api/term/ws?account=<key>.
// Security gates run BEFORE the upgrade so a rejected request gets a plain JSON
// status, not a half-open socket.
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
	reqAccount := strings.TrimSpace(r.URL.Query().Get("account"))

	var cwd string
	var env []string
	switch {
	case reqAccount != "" && reqCwd != "":
		// Two targets is no target: the caller must say which surface it wants.
		writeJSONStatus(w, http.StatusBadRequest,
			map[string]string{"error": "account and cwd are mutually exclusive"})
		return
	case reqAccount != "":
		var ok bool
		cwd, env, ok = resolveTermAccount(reqAccount)
		if !ok {
			writeJSONStatus(w, http.StatusNotFound,
				map[string]string{"error": "unknown account"})
			return
		}
	default:
		var projectPath string
		var ok bool
		cwd, projectPath, ok = h.resolveTermCwd(reqCwd)
		if !ok {
			writeJSONStatus(w, http.StatusForbidden,
				map[string]string{"error": "cwd is not a registered project or live worktree path"})
			return
		}
		// Run the dock shell under the project's Claude account. The account is
		// resolved from projectPath, NEVER from cwd: a task worktree_path carries no
		// .claude/settings.local.json of its own (that file lives at the PROJECT
		// root), so resolving from cwd would silently fall back to the default
		// account for every worktree terminal — the same A3 trap dispatch/verify
		// guard against by resolving from the project instead of the spawn cwd. cwd
		// still chdirs the PTY into the worktree; only the account lookup moves.
		env = termAccountEnv(projectPath)
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

	sess, err := termMgr.Start(cwd, env, 0, 0)
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
// Returns the RESOLVED absolute path (what the PTY should chdir into), the
// PROJECT path whose Claude account should govern the session, and ok.
//
// projectPath is the same as cwd when the match was a project root; when the
// match was a task's worktree_path, projectPath is that task's PROJECT (via
// tasks.project_id), never the worktree itself — see termAccountEnv for why.
// It is "" when the matched worktree task's project_id does not resolve (an
// orphaned row), which callers must treat as "no project", not an error.
func (h *Handler) resolveTermCwd(reqCwd string) (cwd string, projectPath string, ok bool) {
	if reqCwd == "" || !filepath.IsAbs(reqCwd) {
		return "", "", false
	}
	// Resolve symlinks so /etc/../<project> or a symlink INTO an allowed dir
	// can't smuggle a path past the string compare — and so a symlink escape OUT
	// of an allowed dir fails too.
	real, err := filepath.EvalSymlinks(reqCwd)
	if err != nil {
		return "", "", false
	}
	for _, allowed := range h.termAllowedRoots() {
		resolvedAllowed, err := filepath.EvalSymlinks(allowed.path)
		if err != nil {
			continue // path vanished (stale worktree row) — skip, don't match
		}
		if real == resolvedAllowed {
			return real, allowed.projectPath, true
		}
	}
	return "", "", false
}

// termAllowedRoot is one path a terminal may open in, paired with the project
// whose Claude account should govern that session.
type termAllowedRoot struct {
	path        string // the allow-listed path itself (EvalSymlinks'd against cwd)
	projectPath string // "" when no project is associated with this root
}

// termAllowedRoots is every path a terminal may open in: all registered project
// roots (paired with themselves) plus every live task worktree_path (paired
// with that task's project — see termWorktreeRoots).
func (h *Handler) termAllowedRoots() []termAllowedRoot {
	var roots []termAllowedRoot
	for _, p := range h.scanColumn(`SELECT path FROM projects WHERE path IS NOT NULL AND path <> ''`) {
		roots = append(roots, termAllowedRoot{path: p, projectPath: p})
	}
	roots = append(roots, h.termWorktreeRoots()...)
	return roots
}

// termWorktreeRoots is every live task worktree_path, LEFT JOINed to its
// project. The join is what lets resolveTermCwd hand back a PROJECT path for a
// worktree cwd instead of the worktree itself: worktree_path is a fresh git
// worktree with no .claude/settings.local.json of its own, so an account bound
// to the project would otherwise be invisible from there (plan A3). A task
// whose project_id no longer resolves to a row (an orphaned task) yields a
// zero-value (empty) projectPath via the LEFT JOIN, exactly like "no project".
func (h *Handler) termWorktreeRoots() []termAllowedRoot {
	rows, err := h.DB.Query(`
		SELECT tasks.worktree_path, projects.path
		FROM tasks
		LEFT JOIN projects ON tasks.project_id = projects.id
		WHERE tasks.worktree_path IS NOT NULL AND tasks.worktree_path <> ''`)
	if err != nil {
		log.Printf("warn: term: worktree allow-list query: %v", err)
		return nil
	}
	defer rows.Close()
	var out []termAllowedRoot
	for rows.Next() {
		var worktreePath string
		var projectPath sql.NullString
		if err := rows.Scan(&worktreePath, &projectPath); err != nil {
			continue
		}
		out = append(out, termAllowedRoot{path: worktreePath, projectPath: projectPath.String})
	}
	return out
}

// resolveTermAccount validates an ACCOUNT-scoped terminal request — the
// connect flow's interactive login fallback. The key must be well-formed
// (claudeacct.ValidKey) AND name an account the registry actually reports
// (findAccount) — the same allow-list the account routes enforce, so the
// query cannot aim a PTY env at an arbitrary name; the key is never used as
// a path. The env delta comes from claudeacct.EnvForAccount — the very call
// dispatch and verify spawn with — which yields nil for the default account
// (absence of CLAUDE_CONFIG_DIR selects the default, by contract).
//
// The PTY starts in the operator's HOME: an account terminal belongs to no
// project, and home is the one cwd that is safe, always present, and carries
// no .claude/settings.local.json trap of its own (a binding is only read from
// a PROJECT path, and home is not one).
func resolveTermAccount(key string) (cwd string, env []string, ok bool) {
	if !claudeacct.ValidKey(key) {
		return "", nil, false
	}
	acct, found := findAccount(key)
	if !found {
		return "", nil, false
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", nil, false
	}
	return home, claudeacct.SpawnDelta(acct.Key), true
}

// termAccountEnv resolves the CLAUDE_CONFIG_DIR env delta for a terminal
// session from a PROJECT path — never call claudeacct.EnvFor with a raw cwd
// (see resolveTermCwd/termWorktreeRoots for why a worktree cwd can't carry a
// binding of its own). The empty-projectPath guard is mandatory, not
// defensive style: claudeacct.Binding joins its argument with
// ".claude/settings.local.json" unconditionally, so EnvFor("") would resolve
// that RELATIVE path against the daemon's own process working directory and
// read whatever unrelated settings file happens to sit there — silently
// binding the session to a stranger's account instead of correctly reporting
// "no project". "" must short-circuit to nil before EnvFor is ever called.
//
// The delta is claudeacct.SpawnDelta — the config dir AND the account's secret
// store — so a dock shell sees the same MCP credentials a dashboard-dispatched
// run and `swarmery account exec` see. Delta-shaped (not SpawnEnv) because the
// PTY layer appends it after its own environment; the one thing a delta cannot
// express is REMOVING an inherited CLAUDE_CONFIG_DIR for a project bound
// explicitly to the default account, so the dock inherits the daemon's baked
// dir in that single case where every whole-env spawn drops it.
func termAccountEnv(projectPath string) []string {
	if projectPath == "" {
		return nil
	}
	return claudeacct.SpawnDelta(claudeacct.Binding(projectPath))
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
