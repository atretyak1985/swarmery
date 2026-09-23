package console

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// recorder is a programSender that records every Msg sent to it.
type recorder struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (r *recorder) Send(m tea.Msg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
}
func (r *recorder) snapshot() []tea.Msg {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tea.Msg(nil), r.msgs...)
}
func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

// streamStub is a Client that also implements streamer, with a scripted stream
// behavior for the reconnect test.
type streamStub struct {
	stubClient
	mu        sync.Mutex
	frames    []WSEvent
	failFirst bool
	calls     int
}

func (s *streamStub) StreamEvents(ctx context.Context, out chan<- WSEvent) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	fail := s.failFirst && call == 1
	frames := append([]WSEvent(nil), s.frames...)
	s.mu.Unlock()

	if fail {
		return errors.New("simulated drop")
	}
	for _, f := range frames {
		select {
		case out <- f:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Block until cancelled so the loop stays "connected".
	<-ctx.Done()
	return ctx.Err()
}

func (s *streamStub) streamCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestPollLoopPushesSnapshotAndLogs(t *testing.T) {
	sc := &stubClient{
		snap: Snapshot{Reachable: true},
		logs: []LogEntry{{ID: 1, Tag: "boot", Msg: "ready"}},
	}
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { pollLoop(ctx, sc, rec); close(done) }()

	// The immediate refresh sends a snapshotMsg and a logsMsg; wait for them.
	waitFor(t, func() bool {
		var gotSnap, gotLogs bool
		for _, m := range rec.snapshot() {
			switch m.(type) {
			case snapshotMsg:
				gotSnap = true
			case logsMsg:
				gotLogs = true
			}
		}
		return gotSnap && gotLogs
	})
	cancel()
	<-done

	// The snapshot carried the client's URL.
	for _, m := range rec.snapshot() {
		if sm, ok := m.(snapshotMsg); ok {
			if sm.snap.url == "" {
				t.Errorf("snapshotMsg.snap.url not set by pollLoop")
			}
		}
	}
}

func TestStreamLoopConnectsAndReconnectsAfterDrop(t *testing.T) {
	ss := &streamStub{failFirst: true, frames: []WSEvent{{Type: "task_updated"}}}
	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() { streamLoop(ctx, ss, rec); close(done) }()

	// First attempt fails → we should see a connected(true) then connected(false),
	// then a reconnect (second StreamEvents call) that delivers the frame.
	waitFor(t, func() bool { return ss.streamCalls() >= 2 })
	waitFor(t, func() bool {
		for _, m := range rec.snapshot() {
			if wm, ok := m.(wsMsg); ok && wm.evt.Type == "task_updated" {
				return true
			}
		}
		return false
	})

	// At least one disconnect (connected:false) was reported for the header chip.
	var sawDisconnect bool
	for _, m := range rec.snapshot() {
		if cm, ok := m.(wsConnMsg); ok && !cm.connected {
			sawDisconnect = true
		}
	}
	if !sawDisconnect {
		t.Errorf("expected a wsConnMsg{connected:false} after the simulated drop")
	}
	cancel()
	<-done
}

func TestOpenDashboardCmdRuns(t *testing.T) {
	// openDashboardCmd returns a Cmd that best-effort opens the browser; running
	// it must not panic and returns nil (no feed spam), even when the opener
	// fails (a headless box). The opener is stubbed: the real one would open a
	// browser tab on the developer's machine on every test run.
	var opened []string
	orig := openBrowser
	openBrowser = func(url string) error {
		opened = append(opened, url)
		return errors.New("no browser")
	}
	t.Cleanup(func() { openBrowser = orig })

	m := NewModel(&stubClient{base: "http://localhost:7777"})
	_, cmd := m.Update(key('o'))
	if cmd == nil {
		t.Fatalf("'o' produced no Cmd")
	}
	if msg := runCmd(cmd); msg != nil {
		t.Errorf("openDashboard Cmd msg = %v, want nil", msg)
	}
	if len(opened) != 1 || opened[0] != "http://localhost:7777" {
		t.Errorf("opened = %v, want [http://localhost:7777]", opened)
	}
}

func TestRenderEventsWrapOffTruncates(t *testing.T) {
	m := NewModel(&stubClient{})
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	m = step(m, logsMsg{entries: []LogEntry{{ID: 1, Tag: "ingest", Msg: long}}})
	m.wrap = false
	m = step(m, tea.WindowSizeMsg{Width: 40, Height: 20})
	out := m.View()
	if out == "" {
		t.Errorf("wrap-off View rendered empty")
	}
}

// waitFor polls cond up to 2s (the loops run in goroutines).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within 2s")
}
