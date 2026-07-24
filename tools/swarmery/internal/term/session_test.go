package term

import (
	"io"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fakePTY is an in-memory stand-in for the PTY master: what the test writes to
// the "client" side (in) is readable by Session.Read; what Session.Write sends
// lands in out. Closing unblocks a pending Read so the io pump can drain.
type fakePTY struct {
	in         chan []byte
	out        chan []byte
	closed     chan struct{}
	once       sync.Once
	buf        []byte
	lastResize Resize
}

func newFakePTY() *fakePTY {
	return &fakePTY{in: make(chan []byte, 16), out: make(chan []byte, 16), closed: make(chan struct{})}
}

func (f *fakePTY) Read(p []byte) (int, error) {
	if len(f.buf) == 0 {
		select {
		case b := <-f.in:
			f.buf = b
		case <-f.closed:
			return 0, io.EOF
		}
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

func (f *fakePTY) Write(p []byte) (int, error) {
	select {
	case f.out <- append([]byte(nil), p...):
		return len(p), nil
	case <-f.closed:
		return 0, io.ErrClosedPipe
	}
}

func (f *fakePTY) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// Resize records the last requested size so tests can assert control handling.
func (f *fakePTY) Resize(cols, rows uint16) error {
	f.lastResize = Resize{Cols: cols, Rows: rows}
	return nil
}

var _ interface{ Resize(uint16, uint16) error } = (*fakePTY)(nil)

// fakeProc records the signals delivered to the process group and blocks Wait
// until the group is hung up — proving Close reaps rather than leaks.
type fakeProc struct {
	mu      sync.Mutex
	signals []syscall.Signal
	exited  chan struct{}
	// hangOnSIGHUP, when false, ignores SIGHUP so the grace path (SIGKILL) runs.
	hangOnSIGHUP bool
}

func newFakeProc(exitOnSIGHUP bool) *fakeProc {
	return &fakeProc{exited: make(chan struct{}), hangOnSIGHUP: exitOnSIGHUP}
}

func (p *fakeProc) SignalGroup(sig syscall.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	p.mu.Unlock()
	if (sig == syscall.SIGHUP && p.hangOnSIGHUP) || sig == syscall.SIGKILL {
		select {
		case <-p.exited:
		default:
			close(p.exited)
		}
	}
	return nil
}

func (p *fakeProc) Wait() error {
	<-p.exited
	return nil
}

func (p *fakeProc) got() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]syscall.Signal(nil), p.signals...)
}

// stubStarter hands out a fresh fakePTY+fakeProc per Start and records them so
// tests can drive/observe each session. Never spawns a real shell.
type stubStarter struct {
	mu           sync.Mutex
	ptys         []*fakePTY
	procs        []*fakeProc
	exitOnSIGHUP bool
	startErr     error
}

func (s *stubStarter) start(_, _ string, _, _ uint16) (ptyFile, process, error) {
	if s.startErr != nil {
		return nil, nil, s.startErr
	}
	f := newFakePTY()
	p := newFakeProc(s.exitOnSIGHUP)
	s.mu.Lock()
	s.ptys = append(s.ptys, f)
	s.procs = append(s.procs, p)
	s.mu.Unlock()
	return f, p, nil
}

func TestSessionLifecycleEcho(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})

	s, err := m.Start("/tmp", 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if m.Count() != 1 {
		t.Fatalf("Count = %d, want 1", m.Count())
	}

	// Client → PTY: Session.Write reaches the fake master's out channel.
	if _, err := s.Write([]byte("git status\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case got := <-st.ptys[0].out:
		if string(got) != "git status\n" {
			t.Errorf("PTY received %q, want %q", got, "git status\n")
		}
	case <-time.After(time.Second):
		t.Fatal("PTY never received the write")
	}

	// PTY → client: bytes pushed into the fake master surface through Read.
	st.ptys[0].in <- []byte("On branch main\n")
	out := make([]byte, 64)
	n, err := s.Read(out)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(out[:n]) != "On branch main\n" {
		t.Errorf("Read = %q, want %q", out[:n], "On branch main\n")
	}

	s.Close()
	// Untrack is async on the closed signal; wait briefly for Count to settle.
	waitFor(t, func() bool { return m.Count() == 0 }, "session untracked after Close")
	if sigs := st.procs[0].got(); len(sigs) == 0 || sigs[0] != syscall.SIGHUP {
		t.Errorf("first signal = %v, want SIGHUP first", sigs)
	}
}

func TestConcurrencyCap(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub", MaxSessions: 2})

	a, err := m.Start("/tmp", 80, 24)
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	if _, err := m.Start("/tmp", 80, 24); err != nil {
		t.Fatalf("start b: %v", err)
	}
	// Third exceeds the cap.
	if _, err := m.Start("/tmp", 80, 24); err != ErrTooManySessions {
		t.Fatalf("third Start err = %v, want ErrTooManySessions", err)
	}
	// Freeing a slot lets the next Start through.
	a.Close()
	waitFor(t, func() bool { return m.Count() == 1 }, "count drops after close")
	if _, err := m.Start("/tmp", 80, 24); err != nil {
		t.Fatalf("start after free: %v", err)
	}
}

func TestStartErrorReleasesSlot(t *testing.T) {
	st := &stubStarter{startErr: io.ErrUnexpectedEOF}
	m := NewManager(Config{starter: st, Shell: "/stub", MaxSessions: 1})
	if _, err := m.Start("/tmp", 0, 0); err != io.ErrUnexpectedEOF {
		t.Fatalf("Start err = %v, want the spawn error", err)
	}
	// The reserved slot must be released so the cap isn't permanently consumed.
	if m.Count() != 0 {
		t.Fatalf("Count = %d after failed Start, want 0 (slot leaked)", m.Count())
	}
}

func TestIdleReapWithInjectedClock(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	m := NewManager(Config{starter: st, Shell: "/stub", IdleTimeout: time.Hour, now: clock})

	s, err := m.Start("/tmp", 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Not yet idle: reaper leaves it alone.
	m.reapIdle()
	if m.Count() != 1 {
		t.Fatalf("session reaped too early (Count = %d)", m.Count())
	}
	// Advance past the idle timeout → reaped.
	now = now.Add(2 * time.Hour)
	m.reapIdle()
	waitFor(t, func() bool { return m.Count() == 0 }, "idle session reaped")
	if sigs := st.procs[0].got(); len(sigs) == 0 {
		t.Error("reaped session was never signalled")
	}
	_ = s
}

func TestCloseGraceEscalatesToSIGKILL(t *testing.T) {
	// A shell that ignores SIGHUP must be SIGKILLed after the grace. Shrink the
	// grace via a fast path: the fakeProc ignores SIGHUP, so Close's timer fires.
	st := &stubStarter{exitOnSIGHUP: false}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(killGrace + 3*time.Second):
		t.Fatal("Close did not return within the grace window")
	}
	sigs := st.procs[0].got()
	if len(sigs) < 2 || sigs[len(sigs)-1] != syscall.SIGKILL {
		t.Errorf("signals = %v, want SIGHUP then SIGKILL escalation", sigs)
	}
}

func TestResizeControl(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if got := st.ptys[0].lastResize; got.Cols != 120 || got.Rows != 40 {
		t.Errorf("PTY resize = %+v, want 120x40", got)
	}
	s.Close()
}

func TestCloseIsIdempotent(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Close()
	s.Close() // must not panic or double-close channels
	if n := len(st.procs[0].got()); n == 0 {
		t.Error("no signal delivered")
	}
}

func TestCloseAll(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub", MaxSessions: 5})
	for i := 0; i < 3; i++ {
		if _, err := m.Start("/tmp", 80, 24); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
	}
	m.CloseAll()
	waitFor(t, func() bool { return m.Count() == 0 }, "CloseAll drains every session")
}

func TestShellFallback(t *testing.T) {
	m := NewManager(Config{starter: &stubStarter{}})
	t.Setenv("SHELL", "")
	if got := m.shell(); got != fallbackShell {
		t.Errorf("shell() = %q, want %q", got, fallbackShell)
	}
	t.Setenv("SHELL", "/usr/bin/fish")
	if got := m.shell(); got != "/usr/bin/fish" {
		t.Errorf("shell() = %q, want /usr/bin/fish", got)
	}
	explicit := NewManager(Config{starter: &stubStarter{}, Shell: "/bin/bash"})
	if got := explicit.shell(); got != "/bin/bash" {
		t.Errorf("explicit shell() = %q, want /bin/bash", got)
	}
}

// waitFor polls cond up to 2s; fails the test with msg on timeout. Used because
// session untracking happens on an async goroutine keyed off the closed signal.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}
