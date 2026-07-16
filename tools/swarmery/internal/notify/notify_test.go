package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"generic ok", Config{URL: "http://localhost:9/hook"}, true},
		{"ntfy ok", Config{URL: "https://ntfy.sh/my-topic", Template: TemplateNtfy}, true},
		{"telegram ok", Config{URL: "https://api.telegram.org/bot123:abc/sendMessage", Template: TemplateTelegram, TelegramChat: "42"}, true},
		{"empty url", Config{}, false},
		{"bad scheme", Config{URL: "ftp://x"}, false},
		{"unknown template", Config{URL: "http://x", Template: "carrier-pigeon"}, false},
		{"telegram without chat", Config{URL: "http://x", Template: TemplateTelegram}, false},
		{"unknown event", Config{URL: "http://x", Events: []string{"session_completed", "nope"}}, false},
	}
	for _, c := range cases {
		if err := c.cfg.withDefaults().validate(); (err == nil) != c.ok {
			t.Errorf("%s: validate() err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestBuildRequestGeneric(t *testing.T) {
	cfg := Config{URL: "http://receiver.local/hook"}.withDefaults()
	e := Event{Type: EventApprovalRequested, TS: "2026-07-16T10:00:00.000Z",
		Project: "proj", SessionID: 3, RequestID: 7, Tool: "Bash",
		Title: "Approval needed: Bash", Body: "proj — Bash: git push"}
	req, err := buildRequest(cfg, e)
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != http.MethodPost || req.URL.String() != cfg.URL {
		t.Fatalf("req = %s %s", req.Method, req.URL)
	}
	if ct := req.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	body, _ := io.ReadAll(req.Body)
	var got Event
	if err := json.Unmarshal(body, &got); err != nil || got != e {
		t.Errorf("body = %s (err %v), want the Event verbatim", body, err)
	}
}

func TestBuildRequestNtfy(t *testing.T) {
	cfg := Config{URL: "https://ntfy.sh/topic", Template: TemplateNtfy}.withDefaults()
	e := Event{Type: EventApprovalRequested, Title: "Approval needed: Bash", Body: "proj — git push"}
	req, err := buildRequest(cfg, e)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Title"); got != e.Title {
		t.Errorf("Title header = %q", got)
	}
	if got := req.Header.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q, want high for approval_requested", got)
	}
	if got := req.Header.Get("Tags"); got == "" {
		t.Error("Tags header missing")
	}
	body, _ := io.ReadAll(req.Body)
	if string(body) != e.Body {
		t.Errorf("body = %q, want plain-text Body", body)
	}
}

func TestBuildRequestTelegram(t *testing.T) {
	cfg := Config{URL: "https://api.telegram.org/bot1:a/sendMessage",
		Template: TemplateTelegram, TelegramChat: "-100777"}.withDefaults()
	e := Event{Type: EventSessionCompleted, Title: "Session finished", Body: "proj — fix the build"}
	req, err := buildRequest(cfg, e)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(req.Body)
	var got struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body = %s: %v", body, err)
	}
	if got.ChatID != "-100777" || !strings.Contains(got.Text, "Session finished") ||
		!strings.Contains(got.Text, "fix the build") {
		t.Errorf("telegram body = %+v", got)
	}
}

// TestNotifierDelivers: end-to-end — Emit → worker → HTTP POST arrives.
func TestNotifierDelivers(t *testing.T) {
	got := make(chan Event, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		json.NewDecoder(r.Body).Decode(&e)
		got <- e
	}))
	defer srv.Close()

	n, err := New(Config{URL: srv.URL, Events: []string{EventSessionError}})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()

	n.Emit(Event{Type: EventSessionError, Title: "t", Body: "b"})
	select {
	case e := <-got:
		if e.Type != EventSessionError || e.TS == "" {
			t.Errorf("delivered = %+v (TS must be stamped)", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("event not delivered within 3s")
	}
}

// TestNotifierFiltersAndNilSafe: events outside --notify-events are dropped
// before the queue; a nil Notifier ignores Emit.
func TestNotifierFiltersAndNilSafe(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer srv.Close()

	n, err := New(Config{URL: srv.URL, Events: []string{EventApprovalRequested}})
	if err != nil {
		t.Fatal(err)
	}
	n.Emit(Event{Type: EventSessionCompleted}) // filtered
	n.Emit(Event{Type: EventApprovalRequested})
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	n.Close()
	if hits.Load() != 1 {
		t.Errorf("receiver hits = %d, want exactly 1 (filtered event must not POST)", hits.Load())
	}

	var nilN *Notifier
	nilN.Emit(Event{Type: EventApprovalRequested}) // must not panic
}

// TestNotifierNeverBlocks: with the worker wedged on a slow receiver and the
// queue full, Emit returns immediately (drop-with-log).
func TestNotifierNeverBlocks(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	n, err := New(Config{URL: srv.URL, Events: KnownEvents, QueueSize: 1, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			n.Emit(Event{Type: EventSessionCompleted})
		}
		close(done)
	}()
	select {
	case <-done: // Emit dropped the overflow — good
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on a full queue")
	}
}
