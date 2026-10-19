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

func TestRunStoreOptimizeTicksAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var logs []string
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(f, a...))
	}
	calls := 0
	pass := func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 1 {
			return errors.New("busy")
		}
		if calls == 2 {
			cancel()
		}
		return nil
	}
	done := make(chan struct{})
	go func() { runStoreOptimize(ctx, pass, time.Millisecond, logf); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runStoreOptimize did not stop after ctx was cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (a failed tick must not stop the loop)", calls)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "warning: store: optimize (periodic): busy") {
		t.Errorf("failed tick not logged: %q", joined)
	}
}
