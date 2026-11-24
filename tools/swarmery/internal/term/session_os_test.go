package term

import (
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// A real bash on a real PTY: the job it starts lands in a process group of its
// own and ignores SIGHUP, the way claude outlived a closed dock terminal and
// was left stopped on it. Close must still take that group down.
func TestCloseReapsForegroundJobOnRealPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell and waits out the kill grace")
	}
	m := NewManager(Config{Shell: "/bin/bash"})
	// HOME points at an empty dir so the login shell reads no profile.
	s, err := m.Start(t.TempDir(), []string{"HOME=" + t.TempDir(), "PS1=$ "}, 80, 24)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if _, err := s.Write([]byte("sh -c 'trap \"\" HUP; echo JOB=$$; exec sleep 300'\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	pgid := readJobPID(t, s)

	start := time.Now()
	s.Close()
	// A killed job is a zombie until its new parent reaps it; kill -0 reports
	// EPERM meanwhile, ESRCH once it is gone.
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err = syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
	}
	if !errors.Is(err, syscall.ESRCH) {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		t.Fatalf("job group %d survived Close (kill -0: %v)", pgid, err)
	}
	if took := time.Since(start); took > killGrace+2*time.Second {
		t.Errorf("Close took %v, want at most the grace", took)
	}
}

var jobLine = regexp.MustCompile(`JOB=(\d+)`)

// readJobPID reads the PTY until the job prints its pid (the leader of its
// group), then waits for the shell to have handed it the foreground.
func readJobPID(t *testing.T, s *Session) int {
	t.Helper()
	found := make(chan int, 1)
	go func() {
		var out bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, err := s.Read(buf)
			out.Write(buf[:n])
			if m := jobLine.FindSubmatch(out.Bytes()); m != nil {
				pid, _ := strconv.Atoi(string(m[1]))
				found <- pid
				return
			}
			if err != nil {
				return
			}
		}
	}()
	select {
	case pid := <-found:
		deadline := time.Now().Add(5 * time.Second)
		for s.proc.ForegroundJob() != pid {
			if time.Now().After(deadline) {
				t.Fatalf("PTY foreground = %d, want the job %d", s.proc.ForegroundJob(), pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return pid
	case <-time.After(10 * time.Second):
		t.Fatal("the job never printed its pid")
		return 0
	}
}
