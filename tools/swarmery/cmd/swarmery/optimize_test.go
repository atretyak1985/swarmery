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

// The first pass is the startup one (optimize=0x10002), every later pass the
// plain periodic one, and a failed pass does not stop the loop.
func TestRunStoreOptimizeStartupThenPeriodic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var logs []string
	var kinds []bool
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(f, a...))
	}
	pass := func(_ context.Context, startup bool) error {
		mu.Lock()
		defer mu.Unlock()
		kinds = append(kinds, startup)
		switch len(kinds) {
		case 2:
			return errors.New("busy")
		case 3:
			cancel()
		}
		return nil
	}
	done := make(chan struct{})
	go func() { runStoreOptimize(ctx, pass, time.Millisecond, time.Millisecond, logf); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runStoreOptimize did not stop after ctx was cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []bool{true, false, false}; fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("startup flags = %v, want %v (a failed pass must not stop the loop)", kinds, want)
	}
	joined := strings.Join(logs, "\n")
	for _, w := range []string{"store: optimize (startup) ", "warning: store: optimize (periodic): busy"} {
		if !strings.Contains(joined, w) {
			t.Errorf("log %q missing from %q", w, joined)
		}
	}
}

// Boot must not wait on the first pass: nothing runs until first elapses, and
// a daemon shut down before then never runs one.
func TestRunStoreOptimizeWaitsFirstDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan struct{})
	go func() {
		runStoreOptimize(ctx, func(context.Context, bool) error { calls++; return nil },
			time.Hour, time.Hour, func(string, ...any) {})
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-done
	if calls != 0 {
		t.Fatalf("pass ran %d times before the first delay elapsed", calls)
	}
}
