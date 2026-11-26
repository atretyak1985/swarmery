// Package term runs interactive PTY sessions for the embedded terminal
// (fusion phase 15). A Manager owns every live PTY, enforces a hard concurrency
// cap and an idle timeout, and guarantees no orphaned shell processes: each PTY
// is started in its own process group and, on close, that group and the job in
// the terminal's foreground (a group of its own under a job-control shell) are
// signalled, then SIGKILLed after a short grace, so children spawned by the
// shell are reaped too — the same exec+pty gotcha the phase-7 executor hit.
//
// The OS-level PTY spawn sits behind the ptyStarter interface so unit tests can
// drive the full lifecycle against a stub command without spawning a real shell.
package term

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

const (
	// DefaultMaxSessions caps concurrent PTYs, attached and detached alike —
	// a session that nobody has reattached still holds its slot until the
	// reaper (DefaultDetachGrace) or the operator closes it.
	DefaultMaxSessions = 5
	// DefaultIdleTimeout reaps a PTY with no I/O for this long.
	DefaultIdleTimeout = 4 * time.Hour
	// DefaultScrollbackBytes bounds the ring buffer each Session keeps so a
	// reattach can replay what it missed.
	DefaultScrollbackBytes = 256 * 1024
	// DefaultDetachGrace is how long a detached (socket dropped, nobody
	// reattached) session is kept alive before the reaper closes it.
	DefaultDetachGrace = 15 * time.Minute
	// killGrace is how long a closing PTY has to exit after SIGHUP before the
	// process group is SIGKILLed.
	killGrace = 5 * time.Second
	// closeWaitAfterMaster bounds the last wait for the shell once its master
	// is closed. A shell that is still not gone is logged and its slot
	// released anyway: a slot must never outlive its tab.
	closeWaitAfterMaster = 2 * time.Second
	// fallbackShell is used when $SHELL is unset (login shells only in v1).
	fallbackShell = "/bin/zsh"
	// maxLabelRunes bounds the client-supplied Label.
	maxLabelRunes = 64
)

// ErrTooManySessions is returned by Start when the concurrency cap is reached.
var ErrTooManySessions = errors.New("term: too many concurrent sessions")

// ErrSessionNotFound is returned by Attach (and reported by Get) when no live
// Session carries the given ID — reattach has nothing to resume, and the
// reaper or an explicit Close already won the race.
var ErrSessionNotFound = errors.New("term: session not found")

// CloseReason says who ended a Session, so the WebSocket bridge can tell the
// browser why its terminal went away instead of a bare "ended".
type CloseReason string

const (
	// ReasonNone: nobody closed it from this side — the shell exited on its
	// own, or the client hung up.
	ReasonNone CloseReason = ""
	// ReasonIdle: the idle reaper closed it after DefaultIdleTimeout with no I/O.
	ReasonIdle CloseReason = "idle"
	// ReasonShutdown: the daemon is stopping (CloseAll).
	ReasonShutdown CloseReason = "shutdown"
	// ReasonDetachExpired: nobody reattached within DetachGrace of the socket
	// dropping, so the reaper closed it.
	ReasonDetachExpired CloseReason = "detach-expired"
)

// ptyFile is the subset of *os.File a live PTY master needs. Abstracted so the
// mock starter can hand back an in-memory pipe pair instead of a real device.
type ptyFile interface {
	io.ReadWriteCloser
}

// process is the subset of the spawned child a Session must control. On a real
// PTY it is the *exec.Cmd's Process; the mock supplies a fake.
type process interface {
	// SignalGroup sends sig to the whole process group (kill(-pgid)).
	SignalGroup(sig syscall.Signal) error
	// Wait blocks until the child exits.
	Wait() error
	// ForegroundJob returns the process group in the PTY's foreground when it
	// is not the shell's own, or 0. A job-control shell (bash, zsh) runs each
	// command it starts — a `claude` typed into the dock — in a group of its
	// own, which kill(-shell) never reaches.
	ForegroundJob() int
	// SignalJob sends sig to process group pgid; sig 0 probes it, and an error
	// means the group is gone.
	SignalJob(pgid int, sig syscall.Signal) error
}

// ptyStarter spawns a shell attached to a PTY. The production implementation is
// osPTYStarter; tests inject a stub that never touches the OS.
type ptyStarter interface {
	// start launches `shell -l` in cwd on a PTY sized cols×rows and returns the
	// master file plus a handle to the process group. env is an environment
	// DELTA appended after the daemon's own environment — the caller's account
	// binding (CLAUDE_CONFIG_DIR), so a `claude` typed into the dock runs under
	// the same account the daemon would spawn for that project. nil = no delta.
	start(shell, cwd string, env []string, cols, rows uint16) (ptyFile, process, error)
}

// Resize is a control message from the client (JSON text frame).
type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Info is the read-only snapshot Manager.List reports for one Session — what
// the dashboard shows to let the operator pick a PTY to reattach.
type Info struct {
	ID         string
	Cwd        string
	Label      string
	StartedAt  time.Time
	Attached   bool
	DetachedAt time.Time
	// Foreground is the comm of the process group in the PTY's foreground,
	// empty when that is the shell itself (nothing distinct is running).
	Foreground string
}

// sessionListener is the one live reattach a Session can have at a time.
// Attach replaces it (closing the previous one's evicted channel); Detach
// (or the Session closing) clears it.
type sessionListener struct {
	data    chan []byte
	evicted chan struct{}
}

// ringBuffer keeps the most recent output a Session produced, bounded to cap
// bytes, so a reattach can replay what happened while nobody was listening.
// Implemented as a trimmed slice rather than a true circular buffer: at the
// default 256 KiB cap the occasional O(n) trim is cheap, and a slice is far
// simpler to get right than wraparound indexing for a buffer this size.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingBuffer(capacity int) *ringBuffer {
	if capacity <= 0 {
		capacity = DefaultScrollbackBytes
	}
	return &ringBuffer{cap: capacity}
}

func (r *ringBuffer) write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.cap; over > 0 {
		r.buf = r.buf[over:]
	}
}

// snapshot returns a copy of everything currently buffered.
func (r *ringBuffer) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]byte, len(r.buf))
	copy(out, r.buf)
	return out
}

// Session is one live PTY. Write sends keystrokes; Resize adjusts the window;
// Close tears the process group down. Output is read exclusively by the
// Session's own readLoop goroutine (started by Manager.Start) — never by the
// caller directly — and fanned out through Attach/Detach so a dropped socket
// never starves the PTY of a reader.
type Session struct {
	// ID, Cwd, Label and StartedAt are set once at construction and never
	// change, so they need no lock to read.
	ID        string
	Cwd       string
	Label     string
	StartedAt time.Time

	pty  ptyFile
	proc process

	// nowFn is the Manager's clock (real or injected); nil (the test-literal
	// seam used by the Close-focused tests, which never call Attach/Detach)
	// falls back to time.Now.
	nowFn func() time.Time

	ring *ringBuffer

	closeOnce sync.Once
	closed    chan struct{}

	// killGrace and closeWaitAfterMaster bound Close; zero (the test seam
	// struct literal) falls back to the package defaults in Close itself.
	killGrace            time.Duration
	closeWaitAfterMaster time.Duration

	mu       sync.Mutex
	lastIO   time.Time
	reason   CloseReason
	resizeFn func(cols, rows uint16) error

	attachMu   sync.Mutex
	listener   *sessionListener
	attached   bool
	detachedAt time.Time
}

// now returns the Session's clock, defaulting to time.Now for the Close-test
// literals that never populate nowFn.
func (s *Session) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// Done is closed once Close has fully torn the Session down — the signal the
// WS bridge waits on to learn the PTY side ended and send a close frame.
func (s *Session) Done() <-chan struct{} { return s.closed }

// readLoop is the Session's one and only PTY reader (fusion phase 15's "one
// reader per session" rule): it runs for the Session's whole life, feeding
// every byte into the ring buffer and, while someone is attached, onto their
// channel too. Started once by Manager.Start/StartWithLabel.
func (s *Session) readLoop() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.touch()
			s.ring.write(chunk)
			s.deliver(chunk)
		}
		if err != nil {
			// The PTY ended on its own (shell exited) — the Manager-level
			// Close/CloseAll/reaper paths close it too, but this is the only
			// path that observes exit on a live, nobody's-fault read.
			s.closeWith(ReasonNone)
			return
		}
	}
}

// deliver hands chunk to the current listener, if any, blocking (the same
// backpressure the old direct-read bridge had via its websocket write
// timeout) until it's consumed, the listener is evicted, or the Session
// closes — whichever comes first, so a stalled browser can never wedge the
// one PTY reader.
func (s *Session) deliver(chunk []byte) {
	s.attachMu.Lock()
	l := s.listener
	s.attachMu.Unlock()
	if l == nil {
		return
	}
	select {
	case l.data <- chunk:
	case <-l.evicted:
	case <-s.closed:
	}
}

// Attach registers the caller as the Session's one live listener, replacing
// (and waking, on its evicted channel, with ReasonAttachedElsewhere-shaped
// semantics the HTTP layer names) any previous one. Returns everything the
// ring buffer has accumulated — including while nobody was listening — plus
// the channel new output will arrive on from here.
func (s *Session) Attach() (snapshot []byte, data <-chan []byte, evicted <-chan struct{}) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	if s.listener != nil {
		close(s.listener.evicted)
	}
	l := &sessionListener{data: make(chan []byte, 256), evicted: make(chan struct{})}
	s.listener = l
	s.attached = true
	s.detachedAt = time.Time{}
	return s.ring.snapshot(), l.data, l.evicted
}

// Detach drops the live listener and starts the detach-grace countdown (the
// reaper closes the Session if nobody Attaches again within Config.DetachGrace).
func (s *Session) Detach() {
	s.attachMu.Lock()
	s.listener = nil
	s.attached = false
	s.detachedAt = s.now()
	s.attachMu.Unlock()
}

// snapshotAttach reports the Attached/DetachedAt pair for Info/the reaper.
func (s *Session) snapshotAttach() (attached bool, detachedAt time.Time) {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	return s.attached, s.detachedAt
}

// info builds the Info the dashboard sees for this Session.
func (s *Session) info() Info {
	attached, detachedAt := s.snapshotAttach()
	return Info{
		ID:         s.ID,
		Cwd:        s.Cwd,
		Label:      s.Label,
		StartedAt:  s.StartedAt,
		Attached:   attached,
		DetachedAt: detachedAt,
		Foreground: foregroundComm(s.proc),
	}
}

// foregroundComm reports the comm of the process group in the PTY's
// foreground (ForegroundJob), or "" when that is the shell itself (nothing
// distinct running) or the lookup fails — e.g. in unit tests against a stub
// process, or a job that already exited between the check and the ps call.
func foregroundComm(proc process) string {
	pgid := proc.ForegroundJob()
	if pgid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pgid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// trimLabel bounds a client-supplied Label to maxLabelRunes.
func trimLabel(label string) string {
	if utf8.RuneCountInString(label) <= maxLabelRunes {
		return label
	}
	runes := []rune(label)
	return string(runes[:maxLabelRunes])
}

// Write proxies to the PTY master and stamps activity.
func (s *Session) Write(p []byte) (int, error) {
	n, err := s.pty.Write(p)
	if n > 0 {
		s.touch()
	}
	return n, err
}

// Resize applies a window-size control message.
func (s *Session) Resize(cols, rows uint16) error {
	s.touch()
	if s.resizeFn == nil {
		return nil
	}
	return s.resizeFn(cols, rows)
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastIO = time.Now()
	s.mu.Unlock()
}

func (s *Session) idleSince() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastIO
}

// Reason reports why the session was closed by the Manager (ReasonNone when it
// was not — the shell exited or the caller closed it).
func (s *Session) Reason() CloseReason {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reason
}

// closeWith records the reason before tearing the session down, so a reader
// that wakes on the closed master already sees it. The first reason wins.
func (s *Session) closeWith(r CloseReason) {
	s.mu.Lock()
	if s.reason == ReasonNone {
		s.reason = r
	}
	s.mu.Unlock()
	s.Close()
}

// Close signals the PTY's process group (SIGHUP, then SIGKILL after the grace)
// and the job in its foreground, closes the master, then waits for the shell
// itself for at most closeWaitAfterMaster more before giving up on it too.
// Idempotent, and always returns within a bounded time and always releases
// the Manager's slot — no shell's behavior can hold either hostage.
//
// The foreground job needs its own signals: the shell leaves it in a separate
// process group, and once the shell has exited on the hang-up, a job that
// outlived it (claude does) is left on a dead terminal, stopped by SIGTTIN or
// SIGTTOU at its next read or write — a process that never exits and never
// answers.
//
// The master is closed before the final wait, not after: a session leader
// stuck reading or writing its own controlling terminal only lets go once
// nothing holds the master open, so waiting on it with the master still open
// can hang forever. Closing it first turns an unbounded wait into one that is
// also bounded, just in case the shell still doesn't leave.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		killGraceD := s.killGrace
		if killGraceD <= 0 {
			killGraceD = killGrace
		}
		waitAfterMaster := s.closeWaitAfterMaster
		if waitAfterMaster <= 0 {
			waitAfterMaster = closeWaitAfterMaster
		}

		// Read before the hang-up: once the shell is gone the PTY no longer names it.
		job := s.proc.ForegroundJob()
		deadline := time.Now().Add(killGraceD)

		// Hang up the controlling terminal first so a well-behaved shell exits.
		_ = s.proc.SignalGroup(syscall.SIGHUP)
		if job > 0 {
			_ = s.proc.SignalJob(job, syscall.SIGHUP)
			// A stopped job acts on the hang-up only once it runs again.
			_ = s.proc.SignalJob(job, syscall.SIGCONT)
		}

		done := make(chan struct{})
		go func() {
			_ = s.proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Until(deadline)):
			// Grace elapsed — SIGKILL the whole group so no child survives.
			_ = s.proc.SignalGroup(syscall.SIGKILL)
			if job > 0 {
				_ = s.proc.SignalJob(job, syscall.SIGKILL)
			}
		}
		if job > 0 {
			s.reapJob(job, deadline)
		}
		// Close the master before the last wait: a session leader blocked in
		// exit on its terminal only finishes once nothing holds the master open.
		_ = s.pty.Close()
		select {
		case <-done:
		case <-time.After(waitAfterMaster):
			log.Printf("warn: term: shell did not exit %s after its master closed; releasing the slot", waitAfterMaster)
		}
		close(s.closed)
	})
}

// jobPollInterval is how often Close checks whether the foreground job left.
const jobPollInterval = 50 * time.Millisecond

// reapJob waits until the deadline for process group pgid to go, then SIGKILLs it.
func (s *Session) reapJob(pgid int, deadline time.Time) {
	for time.Now().Before(deadline) {
		if s.proc.SignalJob(pgid, 0) != nil {
			return
		}
		time.Sleep(jobPollInterval)
	}
	_ = s.proc.SignalJob(pgid, syscall.SIGKILL)
}

// Config tunes a Manager. Zero values fall back to the package defaults.
type Config struct {
	MaxSessions int
	IdleTimeout time.Duration
	// ScrollbackBytes bounds each Session's ring buffer (DefaultScrollbackBytes
	// when zero).
	ScrollbackBytes int
	// DetachGrace is how long a detached session is kept before the reaper
	// closes it (DefaultDetachGrace when zero).
	DetachGrace time.Duration
	// Shell overrides $SHELL / the fallback (tests point it at a stub).
	Shell string
	// killGrace and closeWaitAfterMaster override the package defaults (5s,
	// 2s) for every Session this Manager starts. Zero keeps the default —
	// tests shrink them so a hung shell doesn't make `go test` slow.
	killGrace            time.Duration
	closeWaitAfterMaster time.Duration
	// now and starter are test seams; nil selects the real implementations.
	now     func() time.Time
	starter ptyStarter
}

// Manager owns the live PTY set, the concurrency cap, and the idle/detach
// reapers. Sessions are keyed by their random ID so a reattach can look one
// up without holding a *Session from before the socket dropped.
type Manager struct {
	cfg     Config
	starter ptyStarter
	now     func() time.Time

	mu   sync.Mutex
	live map[string]*Session
}

// NewManager builds a Manager and starts its idle reaper. Call Close to stop the
// reaper and tear down every live session at daemon shutdown.
func NewManager(cfg Config) *Manager {
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = DefaultIdleTimeout
	}
	if cfg.ScrollbackBytes <= 0 {
		cfg.ScrollbackBytes = DefaultScrollbackBytes
	}
	if cfg.DetachGrace <= 0 {
		cfg.DetachGrace = DefaultDetachGrace
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	starter := cfg.starter
	if starter == nil {
		starter = osPTYStarter{}
	}
	m := &Manager{cfg: cfg, starter: starter, now: now, live: map[string]*Session{}}
	return m
}

// Count reports the number of live sessions, attached and detached alike (for
// the 429 decision and health).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.live)
}

// Get returns the live Session for id, or ok=false when none exists (a
// reattach racing the reaper, a typo, or an id that never existed).
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.live[id]
	if !ok || s.pty == nil { // pty == nil: placeholder mid-Start, not yet real
		return nil, false
	}
	return s, true
}

// List reports every live session, oldest first, for the dashboard's "open
// terminals" view.
func (m *Manager) List() []Info {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.live))
	for _, s := range m.live {
		if s.pty == nil {
			continue
		}
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	out := make([]Info, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Attach reattaches the caller to the live session named by id, evicting any
// previous attachment. Returns ErrSessionNotFound when no such session is
// live (closed already, or the id never existed).
func (m *Manager) Attach(id string) (*Session, []byte, <-chan []byte, <-chan struct{}, error) {
	s, ok := m.Get(id)
	if !ok {
		return nil, nil, nil, nil, ErrSessionNotFound
	}
	snapshot, data, evicted := s.Attach()
	return s, snapshot, data, evicted, nil
}

// Detach marks the session named by id as detached (a no-op when the id is
// unknown — the caller is tearing down a socket for a session that is
// already gone, which is not an error).
func (m *Manager) Detach(id string) {
	if s, ok := m.Get(id); ok {
		s.Detach()
	}
}

// Start spawns a new PTY in cwd sized cols×rows. Returns ErrTooManySessions when
// the cap is reached. The caller owns the returned Session and MUST Close it.
//
// env is the shell's WHOLE environment (TERM is added on top). The HTTP handler
// fills it with claudeacct.SpawnEnvFor(os.Environ(), projectPath) so the dock
// session runs under the project's Claude account with that account's secret
// store — and, for a project bound explicitly to the default account, WITHOUT
// a CLAUDE_CONFIG_DIR the daemon itself inherited, which a delta could never
// express. nil means the daemon's own environment, exactly what the child got
// before this parameter existed.
//
// The Manager takes the env rather than resolving it, because only the caller
// knows what its cwd IS. The dock's cwd is a registered project path (settings
// file present ⇒ resolves) or a live task worktree (no settings file ⇒ resolves
// to the default account, exactly like dispatch's Cwd would — plan A3). Keeping
// the resolve out of here means that gap is closable at the handler without
// touching the PTY layer.
func (m *Manager) Start(cwd string, env []string, cols, rows uint16) (*Session, error) {
	return m.StartWithLabel(cwd, env, cols, rows, "")
}

// StartWithLabel is Start plus a client-supplied Label (trimmed to
// maxLabelRunes) shown alongside Cwd in Manager.List.
func (m *Manager) StartWithLabel(cwd string, env []string, cols, rows uint16, label string) (*Session, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	id := newSessionID()
	m.mu.Lock()
	if len(m.live) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return nil, ErrTooManySessions
	}
	// Reserve the slot before the (slow) spawn so a burst can't overshoot.
	placeholder := &Session{}
	m.live[id] = placeholder
	m.mu.Unlock()

	f, proc, err := m.starter.start(m.shell(), cwd, env, cols, rows)
	if err != nil {
		m.mu.Lock()
		delete(m.live, id)
		m.mu.Unlock()
		return nil, err
	}

	s := &Session{
		ID:                   id,
		Cwd:                  cwd,
		Label:                trimLabel(label),
		StartedAt:            m.now(),
		pty:                  f,
		proc:                 proc,
		nowFn:                m.now,
		ring:                 newRingBuffer(m.cfg.ScrollbackBytes),
		closed:               make(chan struct{}),
		lastIO:               m.now(),
		killGrace:            m.cfg.killGrace,
		closeWaitAfterMaster: m.cfg.closeWaitAfterMaster,
	}
	if rf, ok := f.(interface{ Resize(cols, rows uint16) error }); ok {
		s.resizeFn = rf.Resize
	} else if osFile, ok := f.(*os.File); ok {
		s.resizeFn = func(c, r uint16) error {
			return pty.Setsize(osFile, &pty.Winsize{Cols: c, Rows: r})
		}
	}

	m.mu.Lock()
	m.live[id] = s
	m.mu.Unlock()

	// The Session's own, sole PTY reader — started here so a session started
	// outside a test (or a Close-focused test literal) never loses output.
	go s.readLoop()

	// Untrack the session when it closes so Count stays honest.
	go func() {
		<-s.closed
		m.mu.Lock()
		delete(m.live, id)
		m.mu.Unlock()
	}()
	return s, nil
}

// newSessionID returns 16 random bytes, hex-encoded — crypto/rand.Read never
// errors on the platforms Go supports, so no fallback path is needed.
func newSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Manager) shell() string {
	if m.cfg.Shell != "" {
		return m.cfg.Shell
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return fallbackShell
}

// reapIdle closes every session idle longer than the timeout. Exposed for the
// reaper loop and for deterministic tests (inject Config.now, call directly).
func (m *Manager) reapIdle() {
	cutoff := m.now().Add(-m.cfg.IdleTimeout)
	m.mu.Lock()
	var stale []*Session
	for _, s := range m.live {
		if s.pty == nil { // placeholder mid-Start
			continue
		}
		if s.idleSince().Before(cutoff) {
			stale = append(stale, s)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		s.closeWith(ReasonIdle)
	}
}

// reapDetached closes every session that has been detached longer than
// Config.DetachGrace — nobody came back to reattach it in time. Exposed for
// the reaper loop and for deterministic tests (inject Config.now).
func (m *Manager) reapDetached() {
	cutoff := m.now().Add(-m.cfg.DetachGrace)
	m.mu.Lock()
	var stale []*Session
	for _, s := range m.live {
		if s.pty == nil { // placeholder mid-Start
			continue
		}
		if attached, detachedAt := s.snapshotAttach(); !attached && !detachedAt.IsZero() && detachedAt.Before(cutoff) {
			stale = append(stale, s)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		s.closeWith(ReasonDetachExpired)
	}
}

// Reap runs the idle and detach-grace reapers until stop is closed. The
// daemon runs this in a goroutine; tests call reapIdle/reapDetached directly
// and skip it.
func (m *Manager) Reap(stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.reapIdle()
			m.reapDetached()
		}
	}
}

// CloseAll tears down every live session (daemon shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	var all []*Session
	for _, s := range m.live {
		if s.pty != nil {
			all = append(all, s)
		}
	}
	m.mu.Unlock()
	for _, s := range all {
		s.closeWith(ReasonShutdown)
	}
}

// ── real OS implementation ───────────────────────────────────────────────────

// osPTYStarter spawns a login shell on a real PTY in its own process group.
type osPTYStarter struct{}

// osProcess wraps an *exec.Cmd so the whole process group can be signalled,
// and the PTY master it runs on so the terminal's foreground job can be read.
type osProcess struct {
	cmd *exec.Cmd
	tty *os.File
}

func (p osProcess) SignalGroup(sig syscall.Signal) error {
	if p.cmd.Process == nil {
		return nil
	}
	// Negative pid targets the group led by the child (Setpgid below made the
	// child its own group leader), so descendants receive the signal too.
	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

func (p osProcess) Wait() error { return p.cmd.Wait() }

func (p osProcess) ForegroundJob() int {
	if p.cmd.Process == nil || p.tty == nil {
		return 0
	}
	// SyscallConn, not Fd: Fd would switch the master to blocking mode.
	rc, err := p.tty.SyscallConn()
	if err != nil {
		return 0
	}
	pgid, ierr := 0, error(nil)
	if err := rc.Control(func(fd uintptr) { pgid, ierr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP) }); err != nil || ierr != nil {
		return 0
	}
	if pgid <= 0 || pgid == p.cmd.Process.Pid {
		return 0
	}
	return pgid
}

func (p osProcess) SignalJob(pgid int, sig syscall.Signal) error {
	if pgid <= 0 {
		return syscall.ESRCH
	}
	return syscall.Kill(-pgid, sig)
}

// ptyEnv composes the child environment for a dock session: the caller's whole
// environment (nil ⇒ the daemon's own) plus the terminal type the frontend's
// xterm expects, appended last so it wins over anything inherited — os/exec
// keeps the last occurrence of a duplicated key.
//
// When the environment names no locale at all — the norm for a daemon started
// by launchd or systemd, which pass none — a UTF-8 LANG is added, the same
// default Terminal.app gives a shell. Without it the login shell runs in the C
// locale and bash's readline echoes every non-ASCII keystroke (Cyrillic, say)
// as octal escapes such as \320\277. A locale the operator did set wins.
func ptyEnv(env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := append(env[:len(env):len(env)], "TERM=xterm-256color")
	if !hasLocale(env) {
		out = append(out, defaultLocale)
	}
	return out
}

// defaultLocale is what a dock shell gets when nothing upstream chose a locale.
const defaultLocale = "LANG=en_US.UTF-8"

// hasLocale reports whether env sets any variable that decides the character
// encoding (LC_ALL, LC_CTYPE or LANG) to a non-empty value.
func hasLocale(env []string) bool {
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if ok && v != "" && (k == "LC_ALL" || k == "LC_CTYPE" || k == "LANG") {
			return true
		}
	}
	return false
}

func (osPTYStarter) start(shell, cwd string, env []string, cols, rows uint16) (ptyFile, process, error) {
	cmd := exec.Command(shell, "-l")
	cmd.Dir = cwd
	cmd.Env = ptyEnv(env)
	// pty.StartWithSize forces Setsid=true + Setctty=true, so the child becomes a
	// session AND process-group leader (pgid == pid). kill(-pid) reaches the
	// shell and what stays in its group; a job-control shell moves each job it
	// starts into a group of its own, which Close reaches through ForegroundJob.
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, nil, err
	}
	return f, osProcess{cmd: cmd, tty: f}, nil
}
