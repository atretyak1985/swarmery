package ingest

import "sync"

// Notification types — MUST stay in sync with WSMessageType in
// web/src/api/types.ts (frozen contract) and docs/ws-protocol.md.
const (
	NoteSessionStarted = "session_started"
	NoteSessionUpdated = "session_updated"
	NoteEventAppended  = "event_appended"
	// phase 2 — approvals (frozen at gate 2.2): published by internal/approvals.
	NotePermissionRequested = "permission_requested"
	NotePermissionResolved  = "permission_resolved"
	// phase 4 — system registry (Stage 1): published by internal/sysscan when a
	// config item (agent/skill/hook/command) is created, changes content, or is
	// soft-deleted. Additive — nothing above this line may change.
	NoteSystemItemUpdated = "system_item_updated"
	// fusion phase 1 — task board (additive): published by internal/api when a
	// board task row is created or patched. The ONE message type the
	// fusion-orchestration program adds; nothing above this line may change.
	NoteTaskUpdated = "task_updated"
	// plans-page-lifecycle phase 1 — epics (additive): published by wsingest
	// when a task's plan/ content hash changes, and by the epic lifecycle
	// endpoint after a pause/resume/archive/restore. Reuses Notification.TaskID.
	NotePlanUpdated = "plan_updated"
	// board task delete (additive): published by internal/api when a board row is
	// permanently removed. Unlike task_updated the payload CANNOT be hydrated —
	// the row is gone by the time the frame is built — so it carries ids only
	// (Notification.TaskID + ProjectID) and the client drops the card by id.
	NoteTaskDeleted = "task_deleted"
	// learning loop phase 13 — attention (additive): published by the daemon's
	// surprise scorer when a finished phase run's surprise score first reaches
	// SWARMERY_SURPRISE_NOTIFY (at most once per run). Carries TaskID (the
	// workspace task) + PhaseID; the WS layer hydrates the stored score.
	NotePhaseSurprise = "phase_surprise"
)

// Notification is one ingest event on the internal bus. It carries row ids
// only; subscribers (the WS layer) hydrate the DTO payloads from the DB so
// the JSON shapes stay defined in exactly one place (internal/api).
type Notification struct {
	Type      string // one of the Note* constants above
	SessionID int64  // sessions.id — always set
	EventID   int64  // events.id — set for event_appended only
	RequestID int64  // permission_requests.id — set for permission_* only
	// phase 4 — system registry (additive): set for system_item_updated only.
	Kind   string // agent | skill | hook | command
	ItemID int64  // row id in the corresponding registry table
	// fusion phase 1 — task board (additive): set for task_updated only.
	TaskID int64 // tasks.id
	// board task delete (additive): set for task_deleted only. The deleted row
	// cannot be re-read, so its owning project rides along on the notification
	// instead of being looked up by the WS layer.
	ProjectID int64 // projects.id
	// learning loop phase 13 (additive): set for phase_surprise only.
	PhaseID int64 // epic_phases.id
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

// SubscriberCount reports the number of live subscribers (WS clients attached
// to the fan-out). Additive read used by GET /api/health's wsClients field
// (fusion phase 9); the mutex keeps it consistent with Subscribe/Publish.
func (b *Bus) SubscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
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
