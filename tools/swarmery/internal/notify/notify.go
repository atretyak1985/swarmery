// Package notify is the outbound webhook dispatcher of the control plane:
// one Notifier POSTs notifications about approval and session lifecycle
// events to a single configured URL (generic JSON, ntfy.sh, or a Telegram
// bot). Fire-and-forget by design: a buffered queue + one worker goroutine,
// a short per-POST timeout, NO retries, drop-with-log on overflow — a slow
// or dead receiver must never back-pressure the approvals path (KISS).
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Event types — the --notify-events vocabulary.
const (
	EventApprovalRequested = "approval_requested"
	EventApprovalExpired   = "approval_expired"
	EventSessionCompleted  = "session_completed"
	EventSessionError      = "session_error"
)

// KnownEvents lists every valid --notify-events entry.
var KnownEvents = []string{
	EventApprovalRequested, EventApprovalExpired, EventSessionCompleted, EventSessionError,
}

// Body templates (--notify-template).
const (
	TemplateGeneric  = "generic"  // raw Event JSON
	TemplateNtfy     = "ntfy"     // plain-text body + Title/Priority/Tags headers (ntfy.sh)
	TemplateTelegram = "telegram" // Bot API sendMessage JSON {chat_id, text}
)

// tsFormat matches the millisecond-Z timestamp style used across the daemon.
const tsFormat = "2006-01-02T15:04:05.000Z"

// Event is one outbound notification. Title/Body are the human lines every
// template renders; the id fields ride along for generic JSON consumers.
type Event struct {
	Type      string `json:"type"`
	TS        string `json:"ts"`
	Project   string `json:"project,omitempty"`
	SessionID int64  `json:"sessionId,omitempty"`
	RequestID int64  `json:"requestId,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

// Config tunes a Notifier; zero values fall back to defaults.
type Config struct {
	URL          string        // receiver URL (required)
	Events       []string      // enabled event types (default: approval_requested)
	Template     string        // generic | ntfy | telegram (default: generic)
	TelegramChat string        // chat_id — required when Template == telegram
	Timeout      time.Duration // per-POST timeout (default 5s)
	QueueSize    int           // buffered queue length (default 64)
	Client       *http.Client  // test seam (default: &http.Client{Timeout: Timeout})
}

func (c Config) withDefaults() Config {
	if c.Template == "" {
		c.Template = TemplateGeneric
	}
	// Tolerate "a, b" flag input: trim entries, drop empties. The default set
	// applies AFTER trimming so whitespace-only input (`--notify-events " , "`)
	// falls back to the default instead of silently disabling everything.
	events := make([]string, 0, len(c.Events))
	for _, e := range c.Events {
		if e = strings.TrimSpace(e); e != "" {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		events = []string{EventApprovalRequested}
	}
	c.Events = events
	if c.Timeout <= 0 {
		c.Timeout = 5 * time.Second
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 64
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: c.Timeout}
	}
	return c
}

func (c Config) validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || c.URL == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("notify URL must be an http(s) URL, got %q", c.URL)
	}
	switch c.Template {
	case TemplateGeneric, TemplateNtfy:
	case TemplateTelegram:
		if c.TelegramChat == "" {
			return fmt.Errorf("template %q requires --notify-telegram-chat", TemplateTelegram)
		}
	default:
		return fmt.Errorf("unknown notify template %q (want %s|%s|%s)",
			c.Template, TemplateGeneric, TemplateNtfy, TemplateTelegram)
	}
	known := map[string]bool{}
	for _, e := range KnownEvents {
		known[e] = true
	}
	for _, e := range c.Events {
		if !known[e] {
			return fmt.Errorf("unknown notify event %q (want any of %s)",
				e, strings.Join(KnownEvents, ", "))
		}
	}
	return nil
}

// Notifier posts events from a buffered queue on one worker goroutine.
type Notifier struct {
	cfg     Config
	enabled map[string]bool
	ch      chan Event
	done    chan struct{}
}

// New validates cfg and starts the worker goroutine.
func New(cfg Config) (*Notifier, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	n := &Notifier{
		cfg:     cfg,
		enabled: map[string]bool{},
		ch:      make(chan Event, cfg.QueueSize),
		done:    make(chan struct{}),
	}
	for _, e := range cfg.Events {
		n.enabled[e] = true
	}
	go n.run()
	return n, nil
}

// Emit enqueues one event. Nil-receiver safe (a disabled notifier is a nil
// pointer, mirroring the nil-guarded bus). Events outside --notify-events are
// filtered here; a full queue drops with a log line — Emit NEVER blocks.
func (n *Notifier) Emit(e Event) {
	if n == nil || !n.enabled[e.Type] {
		return
	}
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(tsFormat)
	}
	select {
	case n.ch <- e:
	default:
		log.Printf("warn: notify: queue full — dropping %s event", e.Type)
	}
}

// Close stops the worker after the queue drains. Tests only — the daemon
// keeps its notifier for the whole process lifetime.
func (n *Notifier) Close() {
	close(n.ch)
	<-n.done
}

func (n *Notifier) run() {
	defer close(n.done)
	for e := range n.ch {
		n.send(e)
	}
}

// send performs one POST. One attempt, no retries (KISS): a webhook is a
// hint, the dashboard is the source of truth.
func (n *Notifier) send(e Event) {
	req, err := buildRequest(n.cfg, e)
	if err != nil {
		log.Printf("warn: notify: build %s request: %v", e.Type, err)
		return
	}
	resp, err := n.cfg.Client.Do(req)
	if err != nil {
		log.Printf("warn: notify: POST %s: %v", e.Type, err)
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Printf("warn: notify: POST %s: receiver answered %d", e.Type, resp.StatusCode)
	}
}

// ntfyTags / ntfyPriority render the per-event ntfy.sh decorations
// (https://docs.ntfy.sh/publish/ — Title/Priority/Tags headers).
var ntfyTags = map[string]string{
	EventApprovalRequested: "raised_hand",
	EventApprovalExpired:   "hourglass",
	EventSessionCompleted:  "white_check_mark",
	EventSessionError:      "rotating_light",
}

func ntfyPriority(eventType string) string {
	if eventType == EventApprovalRequested {
		return "high"
	}
	return "default"
}

// buildRequest renders one Event into an *http.Request per template.
// Pure — unit-tested directly.
func buildRequest(cfg Config, e Event) (*http.Request, error) {
	switch cfg.Template {
	case TemplateNtfy:
		req, err := http.NewRequest(http.MethodPost, cfg.URL, strings.NewReader(e.Body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		req.Header.Set("Title", e.Title)
		req.Header.Set("Priority", ntfyPriority(e.Type))
		if tag, ok := ntfyTags[e.Type]; ok {
			req.Header.Set("Tags", tag)
		}
		return req, nil
	case TemplateTelegram:
		body, err := json.Marshal(map[string]any{
			"chat_id":                  cfg.TelegramChat,
			"text":                     e.Title + "\n" + e.Body,
			"disable_web_page_preview": true,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	default: // TemplateGeneric
		body, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, cfg.URL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
}
