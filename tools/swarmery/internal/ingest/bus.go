package ingest

import "sync"

// Notification types — MUST stay in sync with WSMessageType in
// web/src/api/types.ts (frozen contract) and docs/ws-protocol.md.
const (
	NoteSessionStarted = "session_started"
	NoteSessionUpdated = "session_updated"
	NoteEventAppended  = "event_appended"
)

// Notification is one ingest event on the internal bus. It carries row ids
// only; subscribers (the WS layer) hydrate the DTO payloads from the DB so
// the JSON shapes stay defined in exactly one place (internal/api).
type Notification struct {
	Type      string // NoteSessionStarted | NoteSessionUpdated | NoteEventAppended
	SessionID int64  // sessions.id — always set
	EventID   int64  // events.id — set for event_appended only
}

// Bus is a minimal fan-out pub/sub channel for ingest notifications.
// Publish never blocks: slow subscribers drop messages once their buffer is
// full (the dashboard resyncs via REST, WS is a live-update hint stream).
type Bus struct {
	mu   sync.Mutex
	subs map[chan Notification]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Notification]struct{})}
}

// Subscribe registers a buffered subscriber channel. Call cancel to
// unsubscribe; the channel is closed by cancel.
func (b *Bus) Subscribe(buffer int) (<-chan Notification, func()) {
	ch := make(chan Notification, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, ch)
			b.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}

// Publish fans n out to all subscribers, dropping on full buffers.
func (b *Bus) Publish(n Notification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- n:
		default: // subscriber too slow — drop, REST resync covers it
		}
	}
}
