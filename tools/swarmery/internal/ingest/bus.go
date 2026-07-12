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
// Publish never blocks: a subscriber whose buffer fills up is disconnected
// (channel closed) so its consumer performs a full REST resync.
type Bus struct {
	mu   sync.Mutex
	subs map[chan Notification]struct{}
}

func NewBus() *Bus {
	return &Bus{subs: make(map[chan Notification]struct{})}
}

// remove unregisters and closes ch if it is still subscribed. Safe to call
// from both cancel and the Publish overflow path — the map check makes the
// close happen exactly once. Caller must hold b.mu.
func (b *Bus) remove(ch chan Notification) {
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
}

// Subscribe registers a buffered subscriber channel. Call cancel to
// unsubscribe; the channel is closed by cancel (or by Publish on overflow —
// consumers must treat a closed channel as "lost sync, resync via REST").
func (b *Bus) Subscribe(buffer int) (<-chan Notification, func()) {
	ch := make(chan Notification, buffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.remove(ch)
	}
	return ch, cancel
}

// Publish fans n out to all subscribers. A subscriber whose buffer is full
// has lost sync — silently dropping frames here left dashboards showing
// stale session statuses forever (sessions stuck "active" after high-traffic
// bursts overflowed the 256-frame buffer and ate the demotion updates). We
// close the laggard's channel instead: the WS handler sees the close, ends
// the connection, and the client's reconnect refetches full state.
func (b *Bus) Publish(n Notification) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- n:
		default: // subscriber too slow — force a resync via disconnect
			b.remove(ch)
		}
	}
}
