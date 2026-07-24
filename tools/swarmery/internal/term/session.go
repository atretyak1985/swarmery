// Package term runs interactive PTY sessions for the embedded terminal
// (fusion phase 15). A Manager owns every live PTY, enforces a hard concurrency
// cap and an idle timeout, and guarantees no orphaned shell processes: each PTY
// is started in its own process group and, on close, the whole group is signalled
// (kill(-pgid)) after a short grace so children spawned by the shell are reaped
// too — the same exec+pty gotcha the phase-7 executor hit.
//
// The OS-level PTY spawn sits behind the ptyStarter interface so unit tests can
// drive the full lifecycle against a stub command without spawning a real shell.
package term

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const (
	// DefaultMaxSessions caps concurrent PTYs. The HTTP layer maps an
	// ErrTooManySessions to 429.
	DefaultMaxSessions = 5
	// DefaultIdleTimeout reaps a PTY with no I/O for this long.
	DefaultIdleTimeout = 4 * time.Hour
	// killGrace is how long a closing PTY has to exit after SIGHUP before the
	// process group is SIGKILLed.
	killGrace = 5 * time.Second
	// fallbackShell is used when $SHELL is unset (login shells only in v1).
	fallbackShell = "/bin/zsh"
)

// ErrTooManySessions is returned by Start when the concurrency cap is reached.
var ErrTooManySessions = errors.New("term: too many concurrent sessions")

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
}

// ptyStarter spawns a shell attached to a PTY. The production implementation is
// osPTYStarter; tests inject a stub that never touches the OS.
type ptyStarter interface {
	// start launches `shell -l` in cwd on a PTY sized cols×rows and returns the
	// master file plus a handle to the process group.
	start(shell, cwd string, cols, rows uint16) (ptyFile, process, error)
}

// Resize is a control message from the client (JSON text frame).
type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Session is one live PTY. Read/Write proxy the master; Resize adjusts the
// window; Close tears the process group down. It is safe for one reader
// goroutine and one writer goroutine to use concurrently (the WS bridge model).
type Session struct {
	pty  ptyFile
	proc process

	closeOnce sync.Once
	closed    chan struct{}

	mu       sync.Mutex
	lastIO   time.Time
	resizeFn func(cols, rows uint16) error
}

// Read proxies the PTY master and stamps activity for the idle reaper.
func (s *Session) Read(p []byte) (int, error) {
	n, err := s.pty.Read(p)
	if n > 0 {
		s.touch()
	}
	return n, err
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

// Close signals the PTY's process group (SIGHUP, then SIGKILL after the grace)
// and closes the master. Idempotent.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		// Hang up the controlling terminal first so a well-behaved shell exits.
		_ = s.proc.SignalGroup(syscall.SIGHUP)

		done := make(chan struct{})
		go func() {
			_ = s.proc.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(killGrace):
			// Grace elapsed — SIGKILL the whole group so no child survives.
			_ = s.proc.SignalGroup(syscall.SIGKILL)
			<-done
		}
		_ = s.pty.Close()
		close(s.closed)
	})
}

// Config tunes a Manager. Zero values fall back to the package defaults.
type Config struct {
	MaxSessions int
	IdleTimeout time.Duration
	// Shell overrides $SHELL / the fallback (tests point it at a stub).
	Shell string
	// now and starter are test seams; nil selects the real implementations.
	now     func() time.Time
	starter ptyStarter
}

// Manager owns the live PTY set, the concurrency cap, and the idle reaper.
type Manager struct {
	cfg     Config
	starter ptyStarter
	now     func() time.Time

	mu   sync.Mutex
	live map[*Session]struct{}
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
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	starter := cfg.starter
	if starter == nil {
		starter = osPTYStarter{}
	}
	m := &Manager{cfg: cfg, starter: starter, now: now, live: map[*Session]struct{}{}}
	return m
}

// Count reports the number of live sessions (for the 429 decision and health).
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.live)
}

// Start spawns a new PTY in cwd sized cols×rows. Returns ErrTooManySessions when
// the cap is reached. The caller owns the returned Session and MUST Close it.
func (m *Manager) Start(cwd string, cols, rows uint16) (*Session, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	m.mu.Lock()
	if len(m.live) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return nil, ErrTooManySessions
	}
	// Reserve the slot before the (slow) spawn so a burst can't overshoot.
	placeholder := &Session{}
	m.live[placeholder] = struct{}{}
	m.mu.Unlock()

	f, proc, err := m.starter.start(m.shell(), cwd, cols, rows)
	if err != nil {
		m.mu.Lock()
		delete(m.live, placeholder)
		m.mu.Unlock()
		return nil, err
	}

	s := &Session{
		pty:    f,
		proc:   proc,
		closed: make(chan struct{}),
		lastIO: m.now(),
	}
	if rf, ok := f.(interface{ Resize(cols, rows uint16) error }); ok {
		s.resizeFn = rf.Resize
	} else if osFile, ok := f.(*os.File); ok {
		s.resizeFn = func(c, r uint16) error {
			return pty.Setsize(osFile, &pty.Winsize{Cols: c, Rows: r})
		}
	}

	m.mu.Lock()
	delete(m.live, placeholder)
	m.live[s] = struct{}{}
	m.mu.Unlock()

	// Untrack the session when it closes so Count stays honest.
	go func() {
		<-s.closed
		m.mu.Lock()
		delete(m.live, s)
		m.mu.Unlock()
	}()
	return s, nil
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
	for s := range m.live {
		if s.pty == nil { // placeholder mid-Start
			continue
		}
		if s.idleSince().Before(cutoff) {
			stale = append(stale, s)
		}
	}
	m.mu.Unlock()
	for _, s := range stale {
		s.Close()
	}
}

// Reap runs the idle reaper until stop is closed. The daemon runs this in a
// goroutine; tests call reapIdle directly and skip it.
func (m *Manager) Reap(stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.reapIdle()
		}
	}
}

// CloseAll tears down every live session (daemon shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	var all []*Session
	for s := range m.live {
		if s.pty != nil {
			all = append(all, s)
		}
	}
	m.mu.Unlock()
	for _, s := range all {
		s.Close()
	}
}

// ── real OS implementation ───────────────────────────────────────────────────

// osPTYStarter spawns a login shell on a real PTY in its own process group.
type osPTYStarter struct{}

// osProcess wraps an *exec.Cmd so the whole process group can be signalled.
type osProcess struct{ cmd *exec.Cmd }

func (p osProcess) SignalGroup(sig syscall.Signal) error {
	if p.cmd.Process == nil {
		return nil
	}
	// Negative pid targets the group led by the child (Setpgid below made the
	// child its own group leader), so descendants receive the signal too.
	return syscall.Kill(-p.cmd.Process.Pid, sig)
}

func (p osProcess) Wait() error { return p.cmd.Wait() }

func (osPTYStarter) start(shell, cwd string, cols, rows uint16) (ptyFile, process, error) {
	cmd := exec.Command(shell, "-l")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// pty.StartWithSize forces Setsid=true + Setctty=true, so the child becomes a
	// session AND process-group leader (pgid == pid). kill(-pid) therefore reaps
	// the shell AND every descendant it spawned — no orphaned processes.
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, nil, err
	}
	return f, osProcess{cmd: cmd}, nil
}
