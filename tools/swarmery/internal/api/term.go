// Embedded terminal (fusion phase 15): GET /api/term/ws upgrades to a WebSocket
// bridged to an interactive PTY (internal/term). One PTY infra serves three
// surfaces — the workspace bottom dock (?cwd = project path), a task's
// worktree terminal (?cwd = worktree_path), and the connect flow's interactive
// login fallback (?account = account key: the PTY runs under that account's
// CLAUDE_CONFIG_DIR, in the operator's home).
//
// Phase 4 (registry + reattach): a dropped socket no longer kills the PTY —
// the daemon-side Session keeps running, detached, for term.Config.DetachGrace
// (internal/term). GET /api/term/ws?attach=<id> reattaches to it, replaying
// whatever accumulated while nobody was listening. The handler tells the
// client which session it got with a "hello" text frame — {"type":"hello",
// "id":"<id>"} — the FIRST frame on every successful upgrade, new session or
// reattach alike, so the client always knows the id to reattach to later.
//
// Security contract (normative, phase-15 spec):
//   - The endpoint is browser-originated, so the origin gate is STRICTER than
//     requireLocalOrigin: a MISSING Origin is rejected too (only the daemon's own
//     origin, or one opted in via SWARMERY_TRUSTED_ORIGINS, passes — origin.go).
//     This closes a raw ws:// dial that would ride the no-Origin exemption.
//   - The Origin alone authorises nothing — any local process can send the
//     daemon's own — so the upgrade must also carry a dashboard session
//     (subprotocol swarmery.session.<token>) or link auth (session.go). The
//     101 selects exactly swarmery.v1.
//   - cwd MUST EvalSymlinks to either a registered project path or a live task
//     worktree_path — anything else (e.g. /etc, a symlink escape) is 403. An
//     ?attach=<id> request skips cwd/account resolution entirely — the id
//     alone names an already-admitted session — but runs every OTHER gate
//     above (origin, loopback, dashboard auth) exactly like a fresh ws does.
//   - account MUST be a well-formed key (claudeacct.ValidKey) naming an account
//     the registry actually reports; it is NEVER used as a path, and it is
//     mutually exclusive with cwd. All of it runs BEFORE the upgrade.
//   - The PTY runs in its own process group; the Session's own readLoop is its
//     one and only reader (internal/term) and outlives a dropped socket; the
//     bridge Detaches on a plain socket drop and only Closes (SIGHUP, then
//     SIGKILL) when the PTY itself ended, the reaper decided DetachGrace
//     elapsed, or the explicit DELETE below asked for it — no orphans either way.
//   - GET /api/term/sessions and DELETE /api/term/sessions/{id} are
//     requireLocalOrigin (same fence as every other daemon-only write/list —
//     a browser dashboard session is not accepted there, only a local caller).
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
	// Loopback-only (D7): a browser cannot sign a WebSocket, and on a listener
	// beyond loopback an Origin header proves nothing — any client on that
	// network can send the daemon's own. So an exposed daemon has no terminal.
	if isExposedListener(r) {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{
			"error": "the terminal is available only when the daemon listens on loopback",
		})
		return
	}
	// Strict origin gate: browser-originated ⇒ the daemon's own Origin is REQUIRED.
	if !isStrictLocalOrigin(r) {
		writeJSONStatus(w, http.StatusForbidden,
			map[string]string{"error": "cross-origin or origin-less terminal upgrade rejected"})
		return
	}
	// The Origin is no credential (any local process can send the daemon's
	// own): the upgrade must carry a dashboard session in its subprotocol
	// list, or link auth (session.go).
	r, ok := authenticateDashboard(w, r)
	if !ok {
		return
	}
	attachID := strings.TrimSpace(r.URL.Query().Get("attach"))
	reqCwd := r.URL.Query().Get("cwd")
	reqAccount := strings.TrimSpace(r.URL.Query().Get("account"))
	label := r.URL.Query().Get("label")

	var cwd string
	var env []string
	if attachID == "" {
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
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Origin is enforced above (stricter than the library's Host-derived
		// check, and aware of the opt-in list), so the library gate is opened;
		// the Host fence (requireLocalHost, routes.go) ran before the handler.
		OriginPatterns: []string{"*"},
		// Select exactly swarmery.v1: the session entry the client offered
		// alongside it is never echoed back.
		Subprotocols: wsAcceptProtocols,
	})
	if err != nil {
		log.Printf("warn: term: accept: %v", err)
		return
	}
	c.SetReadLimit(termReadLimit)
	defer c.Close(websocket.StatusInternalError, "server error")

	// Captured once, not read back off the package global inside the bridge:
	// a concurrent AttachTermManager(nil) (daemon shutdown, test teardown)
	// must never turn a live connection's own Detach/Close call into a nil
	// pointer dereference.
	mgr := termMgr

	if attachID != "" {
		sess, snapshot, data, evicted, err := mgr.Attach(attachID)
		if err != nil {
			// 4404: a private-use WS close code for "no such terminal" — the
			// client shows "Термінал уже закрито" + a Start button (closeReason.ts).
			c.Close(websocket.StatusCode(4404), termCloseNotFound)
			return
		}
		if err := sendTermHello(r.Context(), c, attachID); err != nil {
			return
		}
		bridgeTermStream(r.Context(), mgr, attachID, c, sess, snapshot, data, evicted)
		return
	}

	sess, err := mgr.StartWithLabel(cwd, env, 0, 0, label)
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
	// Attach this very request as the session's first listener: it owns the
	// PTY's output from the moment it's created, same as before this phase.
	snapshot, data, evicted := sess.Attach()
	if err := sendTermHello(r.Context(), c, sess.ID); err != nil {
		return
	}
	bridgeTermStream(r.Context(), mgr, sess.ID, c, sess, snapshot, data, evicted)
}

// sendTermHello is the first frame on every successful upgrade — new session
// or reattach alike — so the client always knows the id to reattach to later.
func sendTermHello(ctx context.Context, c *websocket.Conn, id string) error {
	wctx, cancel := context.WithTimeout(ctx, termWriteTimeout)
	defer cancel()
	payload, err := json.Marshal(struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}{Type: "hello", ID: id})
	if err != nil {
		return err
	}
	return c.Write(wctx, websocket.MessageText, payload)
}

// Close reasons the bridge sends when the PTY side ends the socket, or when
// the attach layer itself refuses/evicts a connection. They are a contract
// with web/src/terminal/closeReason.ts, which turns them into the line the
// tab shows: keep the two in step.
const (
	termCloseShellExited       = "shell-exited"
	termCloseIdle              = "idle"
	termCloseDaemonStopping    = "daemon-stopping"
	termCloseDetachExpired     = "detach-expired"
	termCloseAttachedElsewhere = "attached-elsewhere"
	termCloseNotFound          = "not-found"
)

// termCloseFrame picks the close status and reason for a socket whose PTY ended
// first. The daemon stopping is 1001 Going Away; the shell exiting, the idle
// reaper and the detach-grace reaper are a normal 1000.
func termCloseFrame(r term.CloseReason) (websocket.StatusCode, string) {
	switch r {
	case term.ReasonShutdown:
		return websocket.StatusGoingAway, termCloseDaemonStopping
	case term.ReasonIdle:
		return websocket.StatusNormalClosure, termCloseIdle
	case term.ReasonDetachExpired:
		return websocket.StatusNormalClosure, termCloseDetachExpired
	default:
		return websocket.StatusNormalClosure, termCloseShellExited
	}
}

// bridgeTermStream pumps bytes both ways until either side ends, then either
// Detaches (a plain dropped socket — the PTY keeps running, detached) or lets
// the close frame explain why the PTY itself ended (shell exit, idle reap,
// detach-grace reap, daemon shutdown) or why this connection in particular
// was dropped (evicted by a newer attach). snapshot is written once, up
// front, before anything newly read from the PTY: it is everything the ring
// buffer accumulated before this connection attached.
func bridgeTermStream(ctx context.Context, mgr *term.Manager, id string, c *websocket.Conn, sess *term.Session, snapshot []byte, data <-chan []byte, evicted <-chan struct{}) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if len(snapshot) > 0 {
		wctx, wcancel := context.WithTimeout(ctx, termWriteTimeout)
		_ = c.Write(wctx, websocket.MessageBinary, snapshot)
		wcancel()
	}

	pumpDone := make(chan struct{})
	var evictedHere bool
	// PTY (via the Session's own reader) → WS: stream new output as binary
	// frames; react to eviction (a newer attach took over) or the Session
	// ending (shell exit / idle / detach-grace / shutdown).
	go func() {
		defer close(pumpDone)
		for {
			select {
			case chunk, ok := <-data:
				if !ok {
					return
				}
				wctx, wcancel := context.WithTimeout(ctx, termWriteTimeout)
				werr := c.Write(wctx, websocket.MessageBinary, chunk)
				wcancel()
				if werr != nil {
					return
				}
			case <-evicted:
				evictedHere = true
				_ = c.Close(websocket.StatusCode(4409), termCloseAttachedElsewhere)
				return
			case <-sess.Done():
				code, reason := termCloseFrame(sess.Reason())
				_ = c.Close(code, reason)
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	// WS → PTY: binary frames are raw keystrokes; text frames are JSON control.
readLoop:
	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			break readLoop
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := sess.Write(msg); err != nil {
				break readLoop
			}
		case websocket.MessageText:
			applyTermControl(sess, msg)
		}
	}
	cancel()
	<-pumpDone
	// A plain dropped socket detaches the PTY (it keeps running); an eviction
	// means a NEWER attach already owns the listener slot, so detaching here
	// would wrongly clear that newer attachment's state instead.
	if !evictedHere {
		mgr.Detach(id)
	}
}

// termControl is the JSON text-frame control protocol. Only resize in v1.
type termControl struct {
	Resize *term.Resize `json:"resize,omitempty"`
}

func applyTermControl(sess *term.Session, data []byte) {
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
//
// A key-only composition site (D5): SpawnEnv(key) has no rung, so a ROOTED
// account store is NOT released here — its roots can admit no path this
// terminal has — while a rootless one is (today's behaviour). On a machine
// whose account store carries `# swarmery-root:` lines, the account terminal
// therefore carries none of that account's credentials; open a project-scoped
// terminal, which resolves (and is admitted) from the project path.
//
// KNOWN LIMITATION — this terminal carries NO ESTATE. An account-scoped
// terminal belongs to no project and starts in $HOME, so there is no project
// path to resolve an estate from (and $HOME is never an estate candidate): it
// carries the account's config dir and the account-keyed store only, never an
// estate's credential store. That is deliberate — inventing a project here
// would mean guessing whose credentials to hand the shell. A project-scoped
// terminal (termAccountEnv) resolves its estate normally.
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
	return home, claudeacct.SpawnEnv(os.Environ(), acct.Key), true
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
// Returns the shell's WHOLE environment — claudeacct.SpawnEnvFor, the same
// composition every other swarmery spawn uses: the config dir AND the account's
// secret store, so a dock shell sees the MCP credentials a dashboard-dispatched
// run and `swarmery account exec` see; and, for a project bound explicitly to
// the default account, the daemon's inherited CLAUDE_CONFIG_DIR REMOVED. nil
// (no project) tells the PTY layer to use the daemon's own environment.
func termAccountEnv(projectPath string) []string {
	if projectPath == "" {
		return nil
	}
	return claudeacct.SpawnEnvFor(os.Environ(), projectPath)
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

// termSessionDTO is the JSON shape of term.Info for GET /api/term/sessions.
type termSessionDTO struct {
	ID         string `json:"id"`
	Cwd        string `json:"cwd"`
	Label      string `json:"label,omitempty"`
	StartedAt  string `json:"startedAt"`
	Attached   bool   `json:"attached"`
	DetachedAt string `json:"detachedAt,omitempty"`
	Foreground string `json:"foreground,omitempty"`
}

// listTermSessions handles GET /api/term/sessions?cwdPrefix=<path> —
// requireLocalOrigin (routes.go), the same fence as every other daemon-only
// list: the dock dashboard calls this from the daemon's own origin, which
// requireLocalOrigin accepts, but it is not the stricter dashboard-session
// gate the ws upgrade itself needs. cwdPrefix narrows the list to one
// project and its worktrees (a task's worktree_path lives under the
// project's own path); omitted or empty lists every live session.
func (h *Handler) listTermSessions(w http.ResponseWriter, r *http.Request) {
	if termMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "terminal service not attached"})
		return
	}
	prefix := r.URL.Query().Get("cwdPrefix")
	out := make([]termSessionDTO, 0)
	for _, info := range termMgr.List() {
		if prefix != "" && !strings.HasPrefix(info.Cwd, prefix) {
			continue
		}
		dto := termSessionDTO{
			ID:         info.ID,
			Cwd:        info.Cwd,
			Label:      info.Label,
			StartedAt:  info.StartedAt.UTC().Format(time.RFC3339),
			Attached:   info.Attached,
			Foreground: info.Foreground,
		}
		if !info.DetachedAt.IsZero() {
			dto.DetachedAt = info.DetachedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, dto)
	}
	writeJSON(w, out, nil)
}

// deleteTermSession handles DELETE /api/term/sessions/{id} —
// requireLocalOrigin (routes.go), mirroring POST /api/sessions/{id}/kill.
// Closes the PTY outright (SIGHUP, then SIGKILL) regardless of whether it is
// currently attached or detached; an unknown id is a 404.
func (h *Handler) deleteTermSession(w http.ResponseWriter, r *http.Request) {
	if termMgr == nil {
		writeJSONStatus(w, http.StatusServiceUnavailable,
			map[string]string{"error": "terminal service not attached"})
		return
	}
	id := r.PathValue("id")
	sess, ok := termMgr.Get(id)
	if !ok {
		http.Error(w, `{"error":"terminal not found"}`, http.StatusNotFound)
		return
	}
	sess.Close()
	writeJSON(w, map[string]bool{"closed": true}, nil)
}
