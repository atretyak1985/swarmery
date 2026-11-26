package term

import (
	"io"
	"strings"
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
	// neverExits, when true, makes Wait block forever regardless of any signal
	// delivered — the model of a shell Close can never wait out, only bound.
	neverExits bool

	// job is the foreground job's process group (0: none); jobSignals records
	// what reached it. jobExitsOnHUP: the job leaves on SIGHUP, else only on SIGKILL.
	job           int
	jobExitsOnHUP bool
	jobGone       bool
	jobSignals    []syscall.Signal
}

func (p *fakeProc) ForegroundJob() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.job
}

func (p *fakeProc) SignalJob(pgid int, sig syscall.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pgid != p.job {
		return syscall.ESRCH
	}
	// every attempt is recorded, as a real kill(2) is made even to a group that left
	p.jobSignals = append(p.jobSignals, sig)
	if p.jobGone {
		return syscall.ESRCH
	}
	if sig == syscall.SIGKILL || (sig == syscall.SIGHUP && p.jobExitsOnHUP) {
		p.jobGone = true
	}
	return nil
}

// gotJob returns the signals the foreground job received, probes (0) left out.
func (p *fakeProc) gotJob() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []syscall.Signal
	for _, sig := range p.jobSignals {
		if sig != 0 {
			out = append(out, sig)
		}
	}
	return out
}

func newFakeProc(exitOnSIGHUP bool) *fakeProc {
	return &fakeProc{exited: make(chan struct{}), hangOnSIGHUP: exitOnSIGHUP}
}

func (p *fakeProc) SignalGroup(sig syscall.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	neverExits := p.neverExits
	p.mu.Unlock()
	if neverExits {
		return nil
	}
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
	p.mu.Lock()
	neverExits := p.neverExits
	p.mu.Unlock()
	if neverExits {
		select {}
	}
	<-p.exited
	return nil
}

func (p *fakeProc) got() []syscall.Signal {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]syscall.Signal(nil), p.signals...)
}

// stubStarter hands out a fresh fakePTY+fakeProc per Start and records them so
// tests can drive/observe each session. Never spawns a real shell. envs records
// the environment delta Manager.Start handed down, per call, so the account
// plumbing is observable without spawning a process.
type stubStarter struct {
	mu           sync.Mutex
	ptys         []*fakePTY
	procs        []*fakeProc
	envs         [][]string
	exitOnSIGHUP bool
	startErr     error
}

func (s *stubStarter) start(_, _ string, env []string, _, _ uint16) (ptyFile, process, error) {
	if s.startErr != nil {
		return nil, nil, s.startErr
	}
	f := newFakePTY()
	p := newFakeProc(s.exitOnSIGHUP)
	s.mu.Lock()
	s.ptys = append(s.ptys, f)
	s.procs = append(s.procs, p)
	s.envs = append(s.envs, env)
	s.mu.Unlock()
	return f, p, nil
}

// lastEnv returns the delta recorded for the most recent start call.
func (s *stubStarter) lastEnv() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.envs) == 0 {
		return nil
	}
	return s.envs[len(s.envs)-1]
}

func TestSessionLifecycleEcho(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})

	s, err := m.Start("/tmp", nil, 80, 24)
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

	// PTY → client: bytes pushed into the fake master surface arrive on the
	// attached listener's channel (the Session's own readLoop is the one and
	// only reader of the PTY now).
	_, data, _ := s.Attach()
	st.ptys[0].in <- []byte("On branch main\n")
	select {
	case got := <-data:
		if string(got) != "On branch main\n" {
			t.Errorf("attached read = %q, want %q", got, "On branch main\n")
		}
	case <-time.After(time.Second):
		t.Fatal("attached listener never received the PTY output")
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

	a, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("start a: %v", err)
	}
	if _, err := m.Start("/tmp", nil, 80, 24); err != nil {
		t.Fatalf("start b: %v", err)
	}
	// Third exceeds the cap.
	if _, err := m.Start("/tmp", nil, 80, 24); err != ErrTooManySessions {
		t.Fatalf("third Start err = %v, want ErrTooManySessions", err)
	}
	// Freeing a slot lets the next Start through.
	a.Close()
	waitFor(t, func() bool { return m.Count() == 1 }, "count drops after close")
	if _, err := m.Start("/tmp", nil, 80, 24); err != nil {
		t.Fatalf("start after free: %v", err)
	}
}

func TestStartErrorReleasesSlot(t *testing.T) {
	st := &stubStarter{startErr: io.ErrUnexpectedEOF}
	m := NewManager(Config{starter: st, Shell: "/stub", MaxSessions: 1})
	if _, err := m.Start("/tmp", nil, 0, 0); err != io.ErrUnexpectedEOF {
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

	s, err := m.Start("/tmp", nil, 80, 24)
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
	if got := s.Reason(); got != ReasonIdle {
		t.Errorf("Reason() = %q, want %q", got, ReasonIdle)
	}
}

func TestCloseGraceEscalatesToSIGKILL(t *testing.T) {
	// A shell that ignores SIGHUP must be SIGKILLed after the grace. Shrink the
	// grace via a fast path: the fakeProc ignores SIGHUP, so Close's timer fires.
	st := &stubStarter{exitOnSIGHUP: false}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", nil, 80, 24)
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

// The shell exits on the hang-up but its foreground job sits in a process
// group of its own: it must get the hang-up (and SIGCONT, in case it is
// stopped) itself, and no SIGKILL once it leaves.
func TestCloseHangsUpForegroundJob(t *testing.T) {
	p := newFakeProc(true)
	p.job, p.jobExitsOnHUP = 4242, true
	s := &Session{pty: newFakePTY(), proc: p, closed: make(chan struct{})}
	s.Close()
	got := p.gotJob()
	if len(got) != 2 || got[0] != syscall.SIGHUP || got[1] != syscall.SIGCONT {
		t.Errorf("job signals = %v, want [SIGHUP SIGCONT]", got)
	}
}

// A foreground job that outlives the hang-up (claude does) is SIGKILLed after
// the grace, even though the shell itself already left.
func TestCloseKillsForegroundJobThatSurvivesHangUp(t *testing.T) {
	p := newFakeProc(true)
	p.job = 4242
	s := &Session{pty: newFakePTY(), proc: p, closed: make(chan struct{})}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(killGrace + 3*time.Second):
		t.Fatal("Close did not return within the grace window")
	}
	got := p.gotJob()
	if len(got) == 0 || got[len(got)-1] != syscall.SIGKILL {
		t.Errorf("job signals = %v, want SIGHUP, SIGCONT, then SIGKILL", got)
	}
	if sigs := p.got(); len(sigs) != 1 || sigs[0] != syscall.SIGHUP {
		t.Errorf("shell signals = %v, want only SIGHUP (it exited)", sigs)
	}
}

func TestResizeControl(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", nil, 80, 24)
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
	s, err := m.Start("/tmp", nil, 80, 24)
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
		if _, err := m.Start("/tmp", nil, 80, 24); err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
	}
	m.CloseAll()
	waitFor(t, func() bool { return m.Count() == 0 }, "CloseAll drains every session")
}

// TestCloseReason: a session the caller closes carries no reason; one the
// Manager closes says why, and the first reason sticks.
func TestCloseReason(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub", MaxSessions: 5})

	plain, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	plain.Close()
	if got := plain.Reason(); got != ReasonNone {
		t.Errorf("caller-closed Reason() = %q, want none", got)
	}

	s, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	m.CloseAll()
	if got := s.Reason(); got != ReasonShutdown {
		t.Errorf("CloseAll Reason() = %q, want %q", got, ReasonShutdown)
	}
	s.closeWith(ReasonIdle)
	if got := s.Reason(); got != ReasonShutdown {
		t.Errorf("Reason() after a second close = %q, want the first (%q)", got, ReasonShutdown)
	}
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

// closeAfterMasterProc is a fakeProc whose Wait only returns after the PTY
// master is closed — the model of a shell stuck reading/writing its own
// controlling terminal, which only unsticks once nothing holds the master
// open. It never responds to signals.
type closeAfterMasterProc struct {
	pty      *fakePTY
	signals  []syscall.Signal
	mu       sync.Mutex
	returned chan struct{}
}

func newCloseAfterMasterProc(pty *fakePTY) *closeAfterMasterProc {
	return &closeAfterMasterProc{pty: pty, returned: make(chan struct{})}
}

func (p *closeAfterMasterProc) SignalGroup(sig syscall.Signal) error {
	p.mu.Lock()
	p.signals = append(p.signals, sig)
	p.mu.Unlock()
	return nil
}

func (p *closeAfterMasterProc) Wait() error {
	<-p.pty.closed
	close(p.returned)
	return nil
}

func (p *closeAfterMasterProc) ForegroundJob() int { return 0 }

func (p *closeAfterMasterProc) SignalJob(int, syscall.Signal) error { return syscall.ESRCH }

// TestCloseClosesMasterBeforeWaitingOnShell proves Close closes the PTY master
// before (or without needing) the shell to exit on its own — a shell stuck on
// its own tty only lets go once nothing holds the master open. On the old code
// (master closed only after the final, unbounded wait) this deadlocks; bound
// the assertion itself with a timeout so a regression fails fast instead of
// hanging the whole `go test` run.
func TestCloseClosesMasterBeforeWaitingOnShell(t *testing.T) {
	pty := newFakePTY()
	proc := newCloseAfterMasterProc(pty)
	// Shrink killGrace so a regression (master closed only after this unbounded
	// wait) deadlocks well inside the assertion's own timeout below, instead of
	// racing the package default (5s) and passing by accident.
	s := &Session{pty: pty, proc: proc, closed: make(chan struct{}), killGrace: 200 * time.Millisecond}

	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close deadlocked: the shell's Wait only returns after the master closes, " +
			"so Close must close the master before (or without) waiting for it unbounded")
	}
	select {
	case <-proc.returned:
	default:
		t.Error("shell's Wait never observed the master close")
	}
}

// TestCloseReleasesSlotWhenShellNeverExits proves the Manager slot is freed
// even when the shell never exits at all (Wait blocks forever): Close must
// still return within a bounded time and the session must untrack.
func TestCloseReleasesSlotWhenShellNeverExits(t *testing.T) {
	st := &stubStarter{} // hangOnSIGHUP defaults false, SIGKILL never arrives either: Wait blocks forever below
	m := NewManager(Config{starter: st, Shell: "/stub", killGrace: 50 * time.Millisecond, closeWaitAfterMaster: 50 * time.Millisecond})
	s, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Replace proc.Wait with one that never returns, regardless of signals.
	st.procs[0].neverExits = true

	start := time.Now()
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(m.cfg.killGrace + m.cfg.closeWaitAfterMaster + time.Second):
		t.Fatal("Close did not return even though the shell never exits")
	}
	if took := time.Since(start); took > m.cfg.killGrace+m.cfg.closeWaitAfterMaster+time.Second {
		t.Errorf("Close took %v, want bounded by killGrace+closeWaitAfterMaster", took)
	}
	waitFor(t, func() bool { return m.Count() == 0 }, "slot released though the shell never exits")
	if _, err := m.Start("/tmp", nil, 80, 24); err != nil {
		t.Fatalf("Start after the stuck session's Close: %v", err)
	}
}

// TestFiveOpenCloseCyclesNeverHitCap proves a hung shell never permanently
// consumes a slot: five Start→Close cycles against a shell that never exits
// must each release their slot, so a sixth Start never sees the cap.
func TestFiveOpenCloseCyclesNeverHitCap(t *testing.T) {
	st := &stubStarter{}
	m := NewManager(Config{
		starter:              st,
		Shell:                "/stub",
		MaxSessions:          5,
		killGrace:            20 * time.Millisecond,
		closeWaitAfterMaster: 20 * time.Millisecond,
	})
	for i := 0; i < 5; i++ {
		s, err := m.Start("/tmp", nil, 80, 24)
		if err != nil {
			t.Fatalf("cycle %d Start: %v", i, err)
		}
		st.procs[i].neverExits = true
		s.Close()
		waitFor(t, func() bool { return m.Count() == 0 }, "cycle slot released")
	}
	if _, err := m.Start("/tmp", nil, 80, 24); err != nil {
		t.Fatalf("sixth Start after five hung cycles: %v, want success (no dead slots)", err)
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

// ── phase 4: registry, detach-grace, reattach ───────────────────────────────

// TestListReportsIDCwdLabelAttached proves List() surfaces every field the
// dashboard needs to show an open terminal and let the operator pick one.
func TestListReportsIDCwdLabelAttached(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.StartWithLabel("/tmp/proj", nil, 80, 24, "build")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	infos := m.List()
	if len(infos) != 1 {
		t.Fatalf("List() = %d entries, want 1", len(infos))
	}
	got := infos[0]
	if got.ID != s.ID || got.Cwd != "/tmp/proj" || got.Label != "build" {
		t.Errorf("List()[0] = %+v, want ID=%s Cwd=/tmp/proj Label=build", got, s.ID)
	}
	if got.Attached {
		t.Error("a freshly started, never-attached session reports Attached=true")
	}
	if !got.DetachedAt.IsZero() {
		t.Errorf("DetachedAt = %v, want zero (never attached, so never detached either)", got.DetachedAt)
	}

	// Get by id finds the same Session.
	byID, ok := m.Get(s.ID)
	if !ok || byID != s {
		t.Errorf("Get(%q) = %v, %v, want the Session Start returned", s.ID, byID, ok)
	}
}

// TestDetachGraceReaperClosesAfterTimeout proves a detached session survives
// until Config.DetachGrace elapses with nobody reattaching, then the reaper
// closes it — the Go-test half of SC "detach-expired".
func TestDetachGraceReaperClosesAfterTimeout(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	now := time.Unix(1_000_000, 0)
	clock := func() time.Time { return now }
	m := NewManager(Config{starter: st, Shell: "/stub", DetachGrace: 15 * time.Minute, now: clock})

	s, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s.Attach() // the creating connection's own attach
	s.Detach() // then the socket drops

	if infos := m.List(); len(infos) != 1 || infos[0].Attached || infos[0].DetachedAt.IsZero() {
		t.Fatalf("List() after Detach = %+v, want one entry, Attached=false, DetachedAt set", infos)
	}

	// Not yet past the grace: the reaper leaves it alone.
	m.reapDetached()
	if m.Count() != 1 {
		t.Fatalf("session reaped before DetachGrace elapsed (Count = %d)", m.Count())
	}

	// Advance past the grace → reaped, with the detach-expired reason.
	now = now.Add(20 * time.Minute)
	m.reapDetached()
	waitFor(t, func() bool { return m.Count() == 0 }, "detached session reaped after DetachGrace")
	if got := s.Reason(); got != ReasonDetachExpired {
		t.Errorf("Reason() = %q, want %q", got, ReasonDetachExpired)
	}
}

// TestAttachReplaysOutputWrittenWhileDetached proves the ring buffer keeps
// what the shell wrote between a Detach and the next Attach, so a reattach
// never loses output.
func TestAttachReplaysOutputWrittenWhileDetached(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	_, data1, _ := s.Attach()
	s.Detach()

	// The shell keeps writing while nobody is attached — the Session's own
	// readLoop is still the one reader, so this lands in the ring buffer.
	st.ptys[0].in <- []byte("missed this\n")
	// Give the readLoop a moment to drain it into the ring before reattaching.
	waitFor(t, func() bool { return len(s.ring.snapshot()) > 0 }, "ring buffer received the missed output")

	snapshot, data2, _ := s.Attach()
	if !strings.Contains(string(snapshot), "missed this\n") {
		t.Errorf("snapshot = %q, want it to contain %q", snapshot, "missed this\n")
	}
	// The old listener is dead; draining it must not block forever.
	select {
	case <-data1:
	default:
	}
	_ = data2
}

// TestSecondAttachEvictsFirstWithAttachedElsewhere proves only one listener
// can be live at a time: a second Attach wakes the first on its evicted
// channel instead of letting two connections race over the same PTY.
func TestSecondAttachEvictsFirstWithAttachedElsewhere(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub"})
	s, err := m.Start("/tmp", nil, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	_, _, evicted1 := s.Attach()
	select {
	case <-evicted1:
		t.Fatal("the first attachment was evicted before a second one ever happened")
	default:
	}

	_, _, _ = s.Attach()
	select {
	case <-evicted1:
	case <-time.After(time.Second):
		t.Fatal("the second Attach never evicted the first")
	}
}

// TestRingBufferStaysBounded proves the ring buffer never grows past its cap,
// trimming the oldest bytes first.
func TestRingBufferStaysBounded(t *testing.T) {
	r := newRingBuffer(16)
	r.write([]byte("0123456789"))
	r.write([]byte("abcdefghij"))
	got := r.snapshot()
	if len(got) != 16 {
		t.Fatalf("len(snapshot) = %d, want 16", len(got))
	}
	if string(got) != "456789abcdefghij" {
		t.Errorf("snapshot = %q, want the most recent 16 bytes", got)
	}
}

// TestDetachedSessionsCountTowardTheCap proves DefaultMaxSessions counts
// every live session, attached or detached — a detached session still holds
// its slot until the reaper or an explicit Close frees it.
func TestDetachedSessionsCountTowardTheCap(t *testing.T) {
	st := &stubStarter{exitOnSIGHUP: true}
	m := NewManager(Config{starter: st, Shell: "/stub", MaxSessions: 5})
	for i := 0; i < 5; i++ {
		s, err := m.Start("/tmp", nil, 80, 24)
		if err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		defer s.Close()
		s.Attach()
		s.Detach()
	}
	if _, err := m.Start("/tmp", nil, 80, 24); err != ErrTooManySessions {
		t.Fatalf("6th Start with 5 detached sessions = %v, want ErrTooManySessions", err)
	}
}

// TestAttachUnknownIDReturnsNotFound proves Manager.Attach reports
// ErrSessionNotFound for an id that is not live — closed already, or never
// existed.
func TestAttachUnknownIDReturnsNotFound(t *testing.T) {
	m := NewManager(Config{starter: &stubStarter{}, Shell: "/stub"})
	if _, _, _, _, err := m.Attach("no-such-id"); err != ErrSessionNotFound {
		t.Errorf("Attach(unknown) err = %v, want ErrSessionNotFound", err)
	}
}
