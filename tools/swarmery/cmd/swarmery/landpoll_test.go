package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLandpollInterval is the SWARMERY_LANDPOLL_INTERVAL contract: unset ⇒
// every 10m, `0` disables, a Go duration is honoured, and an invalid value
// falls back to 10m with a warning rather than switching the poller off. The
// boot line is asserted beside each answer.
func TestLandpollInterval(t *testing.T) {
	for _, tc := range []struct {
		env      string
		every    time.Duration
		on, warn bool
		line     string
	}{
		{"", 10 * time.Minute, true, false, "landpoll: every 10m0s"},
		{"0", 0, false, false, "landpoll: disabled"},
		{"0s", 0, false, false, "landpoll: disabled"},
		{"5m", 5 * time.Minute, true, false, "landpoll: every 5m0s"},
		{" 90s ", 90 * time.Second, true, false, "landpoll: every 1m30s"},
		{"soon", 10 * time.Minute, true, true, "landpoll: every 10m0s"},
		{"-5m", 10 * time.Minute, true, true, "landpoll: every 10m0s"},
	} {
		t.Run(fmt.Sprintf("%q", tc.env), func(t *testing.T) {
			getenv := func(k string) string {
				if k != landpollEnv {
					t.Errorf("read env %q, want %q", k, landpollEnv)
				}
				return tc.env
			}
			every, on, warn := landpollInterval(getenv)
			if every != tc.every || on != tc.on {
				t.Errorf("landpollInterval = %s, %v; want %s, %v", every, on, tc.every, tc.on)
			}
			if (warn != nil) != tc.warn {
				t.Errorf("warn = %v, want warning: %v", warn, tc.warn)
			}
			if warn != nil && !strings.Contains(warn.Error(), landpollEnv) {
				t.Errorf("warning %q does not name %s", warn, landpollEnv)
			}
			if got := landpollBootLine(every, on); got != tc.line {
				t.Errorf("boot line = %q, want %q", got, tc.line)
			}
		})
	}
}

// TestLandpollLoopRunsUntilShutdown: the loop waits for the first pass, keeps
// ticking after a failed pass, logs it, and returns when the daemon's ctx ends.
func TestLandpollLoopRunsUntilShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var (
		mu    sync.Mutex
		calls int
		logs  []string
	)
	pass := func(context.Context) (int, int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1:
			return 0, 0, errors.New("store gone")
		case 2:
			return 3, 1, nil
		default:
			cancel()
			return 0, 0, nil
		}
	}
	logf := func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	done := make(chan struct{})
	go func() {
		runLandpoll(ctx, pass, time.Millisecond, time.Millisecond, logf)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runLandpoll did not return after the shutdown ctx ended")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Errorf("passes = %d, want 3 (a failed pass must not stop the loop)", calls)
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"warning: landpoll: store gone", "landpoll: read 3 change requests, 1 changed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("logs %q do not carry %q", joined, want)
		}
	}
}

// TestLandpollLoopStopsBeforeFirstPass: a daemon shut down inside the boot
// delay never reads a single change request.
func TestLandpollLoopStopsBeforeFirstPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	runLandpoll(ctx, func(context.Context) (int, int, error) { ran = true; return 0, 0, nil },
		time.Hour, time.Hour, func(string, ...any) {})
	if ran {
		t.Error("a pass ran after shutdown")
	}
}
