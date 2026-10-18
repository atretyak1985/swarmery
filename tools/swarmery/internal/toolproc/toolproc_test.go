package toolproc

// White-box tests: they shrink the unexported timing knobs and reach into the
// proc table for PIDs, so a stub bash script stands in for serena everywhere —
// no real binary, no real MCP port. Every test registers StopAll as cleanup so
// a failing assertion never leaks a process group.

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// writeStub drops an executable bash script into t.TempDir() and returns its path.
func writeStub(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stub.sh")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubManager wires a Manager to the stub script and registers full cleanup.
func stubManager(t *testing.T, stub string) *Manager {
	t.Helper()
	m := NewManager(Config{Command: func(projectDir string, mcpPort int) (string, []string) {
		return stub, nil
	}})
	t.Cleanup(m.StopAll)
	return m
}

// shrinkKnobs makes the package timing knobs test-sized and restores them on
// cleanup. Restores run after StopAll (LIFO) only when registered first, so
// call this before stubManager. Safe under -race: running procs only read the
// copies captured at Start.
func shrinkKnobs(t *testing.T) {
	t.Helper()
	oldURLWait, oldKillWait := urlWait, killWait
	oldPorts, oldTimeout := probePorts, probeTimeout
	t.Cleanup(func() {
		urlWait, killWait = oldURLWait, oldKillWait
		probePorts, probeTimeout = oldPorts, oldTimeout
	})
	urlWait = 150 * time.Millisecond
	killWait = 2 * time.Second
	probeTimeout = 500 * time.Millisecond
	// Never probe serena's real default ports from tests: a live serena on
	// this machine (e.g. :24282) would answer and beat the stub's stdout
	// parse. Tests that exercise the probe set their own port explicitly.
	probePorts = nil
}

// execCeiling bounds every wait on a stub this file spawned. It is a ceiling,
// not an expected duration: the waits return the moment their condition holds,
// so an idle machine never pays it. Exec'ing a freshly written script under
// full-suite load (CPU contention, plus macOS's first-exec assessment of a new
// file) is environmental latency these tests do not measure.
const execCeiling = 30 * time.Second

// waitDone blocks until the reaper closes p.done for projectID. readAndReap
// settles state and errMsg before close(p.done), so once this returns the
// final state is observable through Status without polling.
func waitDone(t *testing.T, m *Manager, projectID int64, timeout time.Duration) {
	t.Helper()
	m.mu.Lock()
	p := m.procs[projectID]
	m.mu.Unlock()
	if p == nil {
		t.Fatalf("no proc for project %d", projectID)
	}
	select {
	case <-p.done:
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for project %d to be reaped (tail: %v)", timeout, projectID, m.Status(projectID).LogTail)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func pidOf(t *testing.T, m *Manager, projectID int64) int {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.procs[projectID]
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		t.Fatalf("no live process for project %d", projectID)
	}
	return p.cmd.Process.Pid
}

// processGone reports whether pid no longer exists (kill 0 probe).
func processGone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}

func TestStartParsesDashboardURL(t *testing.T) {
	shrinkKnobs(t)
	const want = "http://127.0.0.1:19999/dashboard/index.html"
	stub := writeStub(t, fmt.Sprintf("echo 'Serena web dashboard started at %s'\nsleep 60\n", want))
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	// 20s, not 3s: the stub is a fresh /bin/sh, and spawning one costs ~300 ms on
	// macOS — under `go test ./...`, with dozens of package binaries competing, a 3s
	// budget was the only thing in the module that failed a full local run. waitFor
	// polls every 10 ms and returns the instant the condition holds, so a larger
	// budget costs nothing when the machine is idle.
	waitFor(t, 20*time.Second, "state=running via stdout parse", func() bool {
		return m.Status(1).State == StateRunning
	})
	st := m.Status(1)
	if st.DashboardURL != want {
		t.Errorf("DashboardURL = %q, want %q", st.DashboardURL, want)
	}
	if st.StartedAt.IsZero() {
		t.Error("StartedAt is zero")
	}
}

// TestStartParsesDashboardURLHostname pins that the URL regex accepts hostname
// hosts, not just numeric IPv4 — serena may print "http://localhost:…".
func TestStartParsesDashboardURLHostname(t *testing.T) {
	shrinkKnobs(t)
	const want = "http://localhost:24282/dashboard/index.html"
	stub := writeStub(t, fmt.Sprintf("echo 'Serena web dashboard started at %s'\nsleep 60\n", want))
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "state=running via localhost URL parse", func() bool { // see the budget note above
		return m.Status(1).State == StateRunning
	})
	if got := m.Status(1).DashboardURL; got != want {
		t.Errorf("DashboardURL = %q, want %q", got, want)
	}
}

func TestStartFallbackProbe(t *testing.T) {
	shrinkKnobs(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	probePorts = []int{port}

	stub := writeStub(t, "sleep 60\n") // silent: no URL on stdout, probe must win
	m := stubManager(t, stub)
	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, execCeiling, "state=running via port probe", func() bool {
		return m.Status(1).State == StateRunning
	})
	want := fmt.Sprintf("http://127.0.0.1:%d/dashboard/index.html", port)
	if got := m.Status(1).DashboardURL; got != want {
		t.Errorf("DashboardURL = %q, want %q", got, want)
	}
}

func TestProcessExitBecomesFailed(t *testing.T) {
	shrinkKnobs(t)
	stub := writeStub(t, "echo 'boom: cannot bind port' >&2\nexit 1\n")
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m, 1, execCeiling)
	st := m.Status(1)
	if st.State != StateFailed {
		t.Fatalf("state after exit 1 = %q, want %q", st.State, StateFailed)
	}
	if st.Err == "" {
		t.Error("Err is empty for failed process")
	}
	found := false
	for _, line := range st.LogTail {
		if strings.Contains(line, "boom: cannot bind port") {
			found = true
		}
	}
	if !found {
		t.Errorf("log tail %v does not contain the stderr line", st.LogTail)
	}
}

func TestStopTerminatesGroup(t *testing.T) {
	shrinkKnobs(t)
	stub := writeStub(t, "sleep 60\n")
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	pid := pidOf(t, m, 1)

	if err := m.Stop(1); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := m.Status(1).State; got != StateStopped {
		t.Errorf("state after Stop = %q, want %q", got, StateStopped)
	}
	waitFor(t, execCeiling, "process group to die", func() bool {
		return processGone(pid)
	})

	if err := m.Stop(1); !errors.Is(err, ErrNotRunning) {
		t.Errorf("second Stop = %v, want ErrNotRunning", err)
	}
}

// TestRestartAfterStop pins that a stopped entry is replaceable: Start → Stop
// → Start again succeeds with a fresh process.
func TestRestartAfterStop(t *testing.T) {
	shrinkKnobs(t)
	stub := writeStub(t, "sleep 60\n")
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	pid1 := pidOf(t, m, 1)
	if err := m.Stop(1); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatalf("restart after Stop: %v", err)
	}
	pid2 := pidOf(t, m, 1)
	if pid1 == pid2 {
		t.Errorf("restart reused pid %d, want a fresh process", pid1)
	}
	if got := m.Status(1).State; got != StateStarting && got != StateRunning {
		t.Errorf("state after restart = %q, want starting or running", got)
	}
}

// TestLogTailCap pins the ring buffer: 60 printed lines keep only the LAST 40.
func TestLogTailCap(t *testing.T) {
	shrinkKnobs(t)
	stub := writeStub(t, "for i in $(seq 1 60); do echo \"line $i\"; done\nsleep 60\n")
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, execCeiling, "all 60 lines to be scanned", func() bool {
		tail := m.Status(1).LogTail
		return len(tail) > 0 && tail[len(tail)-1] == "line 60"
	})
	tail := m.Status(1).LogTail
	if len(tail) != logTailCap {
		t.Fatalf("LogTail length = %d, want %d", len(tail), logTailCap)
	}
	for i, line := range tail {
		if want := fmt.Sprintf("line %d", 21+i); line != want {
			t.Errorf("LogTail[%d] = %q, want %q", i, line, want)
		}
	}
}

func TestDoubleStartRejected(t *testing.T) {
	shrinkKnobs(t)
	stub := writeStub(t, "sleep 60\n")
	m := stubManager(t, stub)

	if err := m.Start(1, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(1, t.TempDir()); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start = %v, want ErrAlreadyRunning", err)
	}
}

func TestStopAll(t *testing.T) {
	shrinkKnobs(t)
	stub := writeStub(t, "sleep 60\n")
	m := stubManager(t, stub)

	for _, id := range []int64{1, 2} {
		if err := m.Start(id, t.TempDir()); err != nil {
			t.Fatalf("Start(%d): %v", id, err)
		}
	}
	pid1, pid2 := pidOf(t, m, 1), pidOf(t, m, 2)

	m.StopAll()

	for _, id := range []int64{1, 2} {
		if got := m.Status(id).State; got != StateStopped {
			t.Errorf("project %d state after StopAll = %q, want %q", id, got, StateStopped)
		}
	}
	waitFor(t, execCeiling, "both process groups to die", func() bool {
		return processGone(pid1) && processGone(pid2)
	})
}

// TestStatusUnknownProject pins the zero-value contract step 02 relies on.
func TestStatusUnknownProject(t *testing.T) {
	m := NewManager(Config{})
	if got := m.Status(42).State; got != StateStopped {
		t.Errorf("Status of unknown project = %q, want %q", got, StateStopped)
	}
}

func TestDefaultCommand(t *testing.T) {
	bin, args := DefaultCommand("/tmp/proj", 12345)
	if bin != "serena" {
		t.Errorf("bin = %q, want serena", bin)
	}
	want := "start-mcp-server --project /tmp/proj --transport sse --host 127.0.0.1 --port 12345 --enable-web-dashboard true --open-web-dashboard false"
	if got := strings.Join(args, " "); got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
}
